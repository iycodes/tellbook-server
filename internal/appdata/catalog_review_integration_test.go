package appdata

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestCatalogPreservesUnchangedChildrenAndSectionRevisions(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	provider := insertTessaTestClient(t, ctx, pool)
	handle := "review-" + provider.String()
	if _, err := pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,timezone,country_code,currency_code,locale,market_configured_at) VALUES($1,'Synthetic review',$2,'Africa/Lagos','NG','NGN','en-NG',now())`, provider, handle); err != nil {
		t.Fatal(err)
	}
	h := &Handler{repo: NewRepository(pool)}
	actor := CatalogActor{ProviderID: provider, SecurityRevision: 1}
	mutate := func(op string, id uuid.UUID, revision int64, body string) CatalogReceipt {
		t.Helper()
		receipt, err := h.MutateCatalog(ctx, actor, op, id, []byte(body), revision, uuid.NewString())
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return receipt
	}
	section := mutate("create_service_section", uuid.Nil, 0, `{"name":"Appointments"}`)
	sectionID := uuid.MustParse(section.ResourceID)
	created := mutate("create_service", uuid.Nil, 0, `{"section_id":"`+section.ResourceID+`","service_name":"Consultation","duration_minutes":45,"pricing":{"price_amount_minor":"25000"},"fulfillment":{"mode":"virtual"},"availability":{"mode":"custom","custom_windows":[{"day_of_week":1,"start_time":"09:00","end_time":"17:00","slot_interval_minutes":60}]},"short_notice_rules":[{"threshold_minutes":120,"surcharge_type":"fixed_amount","surcharge_amount_minor":"500"}]}`)
	id := uuid.MustParse(created.ResourceID)
	var original ManagedServiceItem
	if err := json.Unmarshal(created.Data, &original); err != nil {
		t.Fatal(err)
	}
	before, err := h.repo.GetServiceSectionDetails(ctx, provider, sectionID)
	if err != nil {
		t.Fatal(err)
	}
	updated := mutate("update_service", id, original.Revision, `{"description":"Only this field changes"}`)
	var current ManagedServiceItem
	if err := json.Unmarshal(updated.Data, &current); err != nil {
		t.Fatal(err)
	}
	if current.Revision != original.Revision+1 || current.Availability.CustomWindows[0].ID != original.Availability.CustomWindows[0].ID || current.ShortNoticeRules[0].ID != original.ShortNoticeRules[0].ID {
		t.Fatal("ordinary edit replaced unchanged child settings or produced redundant revisions")
	}
	after, err := h.repo.GetServiceSectionDetails(ctx, provider, sectionID)
	if err != nil || after.Section.Revision != before.Section.Revision {
		t.Fatal("ordinary edit invalidated section membership revision", err)
	}
	mutate("update_service_section", sectionID, after.Section.Revision, `{"description":"Only section details change"}`)
	latest, err := h.repo.GetManagedServiceDetails(ctx, provider, id)
	if err != nil || latest.Revision != current.Revision {
		t.Fatal("section description unnecessarily invalidated service revision", err)
	}
	after, err = h.repo.GetServiceSectionDetails(ctx, provider, sectionID)
	if err != nil {
		t.Fatal(err)
	}
	mutate("update_service_section", sectionID, after.Section.Revision, `{"name":"Renamed appointments"}`)
	latest, err = h.repo.GetManagedServiceDetails(ctx, provider, id)
	if err != nil || latest.Revision != current.Revision+1 || latest.SectionName != "Renamed appointments" {
		t.Fatal("rename failed to invalidate dependent service details", err)
	}
	// Order changes still invalidate the section and service revisions.
	after, err = h.repo.GetServiceSectionDetails(ctx, provider, sectionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE services SET sort_order=sort_order+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	ordered, err := h.repo.GetServiceSectionDetails(ctx, provider, sectionID)
	if err != nil || ordered.Section.Revision <= after.Section.Revision {
		t.Fatal("order change did not invalidate section", err)
	}
}

func TestCatalogSummaryPaginationAndProviderIsolation(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	provider := insertTessaTestClient(t, ctx, pool)
	other := insertTessaTestClient(t, ctx, pool)
	// Equal order and creation time must still yield a stable, complete traversal.
	for _, owner := range []uuid.UUID{provider, other} {
		for _, name := range []string{"Beta one", "Beta two", "100% literal"} {
			if _, err := pool.Exec(ctx, `INSERT INTO services(id,client_id,title,slug,description,category,duration_minutes,price_amount_minor,currency_code,status,sort_order,created_at,agreement_timing) VALUES($1,$2,$3,$4,'','',45,250050,'NGN','draft',1,'2026-10-02T00:00:00Z',NULL)`, uuid.New(), owner, name, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := &Handler{repo: NewRepository(pool)}
	seen := map[string]bool{}
	cursor := ""
	for range 3 {
		page, err := h.ListCatalogServices(ctx, provider, 1, cursor, "", "draft", "")
		if err != nil || page.Total != 3 || len(page.Items) != 1 {
			t.Fatalf("page: %+v %v", page, err)
		}
		item := page.Items[0]
		if seen[item.ID] || item.PriceAmountMinor != 250050 {
			t.Fatal("duplicate row or imprecise amount")
		}
		seen[item.ID] = true
		cursor = page.NextCursor
	}
	if cursor != "" || len(seen) != 3 {
		t.Fatal("pagination did not end after all rows")
	}
	page, err := h.ListCatalogServices(ctx, provider, 25, "", "%", "draft", "")
	if err != nil || page.Total != 1 || page.Items[0].Name != "100% literal" {
		t.Fatal("search interpreted a literal as a wildcard", err)
	}
	empty, err := h.ListCatalogServices(ctx, provider, 25, "", "no matches", "", "")
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.Total != 0 {
		t.Fatal("empty page shape changed", err)
	}
	foreign, err := h.ListCatalogServices(ctx, other, 1, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []string{foreign.Items[0].ID, "invalid", page.Items[0].ID} {
		_, err = h.ListCatalogServices(ctx, provider, 1, cursor, "Beta", "", "")
		var public *CatalogError
		if !errors.As(err, &public) || public.Code != "invalid_cursor" {
			t.Fatal("foreign, invalid or filtered cursor accepted", err)
		}
	}
}
