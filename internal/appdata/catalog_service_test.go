package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func TestCatalogTransactions(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	provider := insertTessaTestClient(t, ctx, pool)
	other := insertTessaTestClient(t, ctx, pool)
	for _, id := range []uuid.UUID{provider, other} {
		handle := "catalog-" + id.String()
		if _, err := pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,timezone,country_code,currency_code,locale,market_configured_at) VALUES($1,'Catalog Beta',$2,'Africa/Lagos','NG','NGN','en-NG',now())`, id, handle); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{repo: NewRepository(pool)}
	actor := CatalogActor{ProviderID: provider, SecurityRevision: 1}
	otherActor := CatalogActor{ProviderID: other, SecurityRevision: 1}
	must := func(a CatalogActor, op string, id uuid.UUID, revision int64, input string, key string) CatalogReceipt {
		t.Helper()
		r, e := h.MutateCatalog(ctx, a, op, id, json.RawMessage(input), revision, key)
		if e != nil {
			t.Fatalf("%s: %v", op, e)
		}
		return r
	}
	fail := func(a CatalogActor, op string, id uuid.UUID, revision int64, input, key, code string) {
		t.Helper()
		_, e := h.MutateCatalog(ctx, a, op, id, json.RawMessage(input), revision, key)
		var public *CatalogError
		if !errors.As(e, &public) || public.Code != code {
			t.Fatalf("%s wanted %s, got %v", op, code, e)
		}
	}
	section := must(actor, "create_service_section", uuid.Nil, 0, `{"name":"Consultations"}`, "section")
	destination := must(actor, "create_service_section", uuid.Nil, 0, `{"name":"Appointments"}`, "destination")
	foreign := must(otherActor, "create_service_section", uuid.Nil, 0, `{"name":"Other provider"}`, "section")
	input := `{"service_name":"Consultation","description":"Preserve me","duration_minutes":45,"pricing":{"price_amount_minor":"25000"},"fulfillment":{"mode":"virtual"},"availability":{"mode":"inherit_business_hours","minimum_notice_minutes":120},"publish_status":"publish_now"}`
	created := must(actor, "create_service", uuid.Nil, 0, input, "create")
	id := uuid.MustParse(created.ResourceID)
	var item ManagedServiceItem
	if e := json.Unmarshal(created.Data, &item); e != nil {
		t.Fatal(e)
	}
	if item.Status != "draft" || item.Revision < 1 {
		t.Fatalf("invalid draft %s", created.Data)
	}
	retry := must(actor, "create_service", uuid.Nil, 0, input, "create")
	if retry.ReceiptID != created.ReceiptID {
		t.Fatal("retry produced another receipt")
	}
	fail(actor, "create_service", uuid.Nil, 0, `{}`, "create", "idempotency_conflict")
	fail(otherActor, "update_service", id, item.Revision, `{"description":"stolen"}`, "other", "not_found")
	updated := must(actor, "update_service", id, item.Revision, `{"pricing":{"price_amount_minor":"31000"},"section_id":"`+section.ResourceID+`"}`, "patch")
	if e := json.Unmarshal(updated.Data, &item); e != nil {
		t.Fatal(e)
	}
	if item.Description != "Preserve me" || item.DurationMinutes != 45 || item.Pricing.PriceAmountMinor != 31000 || item.Availability.MinimumNoticeMinutes != 120 {
		t.Fatalf("patch lost fields: %s", updated.Data)
	}
	fail(actor, "update_service", id, created.Revision, `{"description":"stale"}`, "stale", "revision_conflict")
	fail(actor, "update_service", id, item.Revision, `{"publish_status":"published"}`, "bad-status", "invalid_request")
	fail(actor, "update_service", id, item.Revision, `{"section_id":"`+foreign.ResourceID+`"}`, "foreign-section", "not_found")
	fail(actor, "update_service", id, item.Revision, `{"duration_minutes":0}`, "invalid-duration", "validation_failed")
	// A failed mutation must change neither the resource nor the receipt log.
	var afterRevision int64
	var count int
	if e := pool.QueryRow(ctx, `SELECT revision FROM services WHERE id=$1`, id).Scan(&afterRevision); e != nil {
		t.Fatal(e)
	}
	if afterRevision != item.Revision {
		t.Fatal("failed edit changed revision")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM catalog_mutation_receipts WHERE provider_id=$1 AND idempotency_key='invalid-duration'`, provider).Scan(&count)
	if count != 0 {
		t.Fatal("failed edit committed a receipt")
	}
	published := must(actor, "set_service_status", id, item.Revision, `{"status":"published"}`, "publish")
	hidden := must(actor, "set_service_visibility", id, published.Revision, `{"is_hidden":true}`, "hidden")
	duplicate := must(actor, "duplicate_service", id, hidden.Revision, `{}`, "duplicate")
	var copyItem ManagedServiceItem
	_ = json.Unmarshal(duplicate.Data, &copyItem)
	if copyItem.Status != "draft" || copyItem.Pricing.PriceAmountMinor != 31000 {
		t.Fatal("duplicate did not preserve settings as draft")
	}
	// Section membership must update the section revision; foreign/self moves roll back.
	detail, e := h.repo.GetServiceSectionDetails(ctx, provider, uuid.MustParse(section.ResourceID))
	if e != nil {
		t.Fatal(e)
	}
	if detail.Section.Revision <= section.Revision {
		t.Fatal("section revision did not change with membership")
	}
	fail(actor, "delete_service_section", uuid.MustParse(section.ResourceID), detail.Section.Revision, `{"mode":"move","target_section_id":"`+foreign.ResourceID+`"}`, "bad-move", "not_found")
	fail(actor, "delete_service_section", uuid.MustParse(section.ResourceID), detail.Section.Revision, `{"mode":"move","target_section_id":"`+section.ResourceID+`"}`, "self-move", "validation_failed")
	must(actor, "delete_service_section", uuid.MustParse(section.ResourceID), detail.Section.Revision, `{"mode":"move","target_section_id":"`+destination.ResourceID+`"}`, "move")
	latest, e := h.repo.GetManagedServiceDetails(ctx, provider, id)
	if e != nil {
		t.Fatal(e)
	}
	if latest.SectionID != destination.ResourceID || latest.Revision <= hidden.Revision {
		t.Fatal("section move lost service or revision")
	}
	fail(actor, "update_service", id, hidden.Revision, `{"description":"old section"}`, "old-membership", "revision_conflict")
	// Concurrent editors reading the same revision: exactly one may commit.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"concurrent-a", "concurrent-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := h.MutateCatalog(context.Background(), actor, "update_service", id, []byte(`{"description":"Concurrent edit"}`), latest.Revision, key)
			errs <- err
		}(key)
	}
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			var ce *CatalogError
			if errors.As(err, &ce) && ce.Code == "revision_conflict" {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("concurrency: %d success %d conflicts", success, conflicts)
	}
	latest, e = h.repo.GetManagedServiceDetails(ctx, provider, id)
	if e != nil {
		t.Fatal(e)
	}
	paused := must(actor, "set_service_status", id, latest.Revision, `{"status":"paused"}`, "pause")
	draft := must(actor, "set_service_status", id, paused.Revision, `{"status":"draft"}`, "draft")
	must(actor, "delete_service", id, draft.Revision, `{}`, "delete")
	must(actor, "delete_service", uuid.MustParse(duplicate.ResourceID), copyItem.Revision+1, `{}`, "delete-copy") // moved with the section
}

func TestCatalogReorderPreconditionsAndReceipts(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	provider := insertTessaTestClient(t, ctx, pool)
	handle := "reorder-" + provider.String()
	if _, err := pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,timezone,country_code,currency_code,locale,market_configured_at) VALUES($1,'Synthetic order',$2,'Africa/Lagos','NG','NGN','en-NG',now())`, provider, handle); err != nil {
		t.Fatal(err)
	}
	h := &Handler{repo: NewRepository(pool)}
	actor := CatalogActor{ProviderID: provider, SecurityRevision: 1}
	input := `{"service_name":"Reorder service","duration_minutes":45,"pricing":{"price_amount_minor":"25000"},"fulfillment":{"mode":"virtual"},"availability":{"mode":"inherit_business_hours"}}`
	a, err := h.MutateCatalog(ctx, actor, "create_service", uuid.Nil, []byte(input), 0, "first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.MutateCatalog(ctx, actor, "create_service", uuid.Nil, []byte(input), 0, "second")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ReorderItemsInput{OrderedIDs: []string{b.ResourceID, a.ResourceID}, ExpectedRevisions: map[string]int64{a.ResourceID: a.Revision, b.ResourceID: b.Revision}})
	receipt, err := h.MutateCatalog(ctx, actor, "reorder_uncategorized_services", uuid.Nil, body, 0, "order")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := h.MutateCatalog(ctx, actor, "reorder_uncategorized_services", uuid.Nil, body, 0, "order")
	if err != nil || !retry.Replayed || retry.ReceiptID != receipt.ReceiptID {
		t.Fatal("order retry was not repeatable", err)
	}
	_, err = h.MutateCatalog(ctx, actor, "reorder_uncategorized_services", uuid.Nil, body, 0, "stale")
	var ce *CatalogError
	if !errors.As(err, &ce) || ce.Code != "revision_conflict" {
		t.Fatal("stale order accepted", err)
	}
	latest, err := h.repo.ListManagedServices(ctx, provider)
	if err != nil || latest[0].ID != b.ResourceID || latest[0].Revision <= b.Revision {
		t.Fatal("order or revision was lost", err)
	}
	body, _ = json.Marshal(ReorderItemsInput{OrderedIDs: []string{a.ResourceID, b.ResourceID}})
	_, err = h.MutateCatalog(ctx, actor, "reorder_uncategorized_services", uuid.Nil, body, 0, "missing")
	if !errors.As(err, &ce) || ce.Code != "revision_required" {
		t.Fatal("missing item revisions accepted", err)
	}
}
