package appdata

import (
	agreementrender "booking/go-server/internal/agreements/render"
	agreementseed "booking/go-server/internal/agreements/seed"
	"booking/go-server/internal/config"
	"booking/go-server/internal/storage"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCatalogIsolationDependenciesAndSharedImages(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	provider := insertTessaTestClient(t, ctx, pool)
	other := insertTessaTestClient(t, ctx, pool)
	for _, id := range []uuid.UUID{provider, other} {
		handle := "media-" + id.String()
		if _, e := pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, id); e != nil {
			t.Fatal(e)
		}
		if _, e := pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,timezone,country_code,currency_code,locale,market_configured_at) VALUES($1,'Synthetic media beta',$2,'Africa/Lagos','NG','NGN','en-NG',now())`, id, handle); e != nil {
			t.Fatal(e)
		}
	}
	var deleted atomic.Int64
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deleted.Add(1)
		}
		w.WriteHeader(204)
	}))
	defer media.Close()
	storageService, e := storage.NewR2Service(config.Config{R2Endpoint: media.URL, R2PublicBucketName: "public", R2AccessKeyID: "test", R2SecretAccessKey: "test", R2PublicBucketBaseURL: "https://images.tellbook.test"})
	if e != nil {
		t.Fatal(e)
	}
	h := &Handler{repo: NewRepository(pool), storage: storageService}
	actor := CatalogActor{ProviderID: provider, SecurityRevision: 1}
	outsider := CatalogActor{ProviderID: other, SecurityRevision: 1}
	image, e := storageService.GetStorageObjectURL("clients/"+provider.String()+"/services/shared.jpg", "public")
	if e != nil {
		t.Fatal(e)
	}
	mutate := func(op string, id uuid.UUID, rev int64, input, key string) CatalogReceipt {
		t.Helper()
		r, e := h.MutateCatalog(ctx, actor, op, id, []byte(input), rev, key)
		if e != nil {
			t.Fatalf("%s: %v", op, e)
		}
		return r
	}
	input := `{"service_name":"Synthetic media","duration_minutes":45,"pricing":{"price_amount_minor":"25000"},"fulfillment":{"mode":"virtual"},"availability":{"mode":"inherit_business_hours"},"image_url":"` + image + `"}`
	source := mutate("create_service", uuid.Nil, 0, input, "source")
	id := uuid.MustParse(source.ResourceID)
	grant := uuid.New()
	if _, e = pool.Exec(ctx, `INSERT INTO integration_grants(id,provider_id,platform,client_id,resource,scopes,security_revision,expires_at) VALUES($1,$2,'chatgpt','https://chatgpt.com/oauth/client.json','https://api.tellbook.test/mcp/chatgpt',ARRAY['catalog.read','catalog.write'],1,now()+interval '1 day')`, grant, provider); e != nil {
		t.Fatal(e)
	}
	connected := CatalogActor{ProviderID: provider, GrantID: grant, Resource: "https://api.tellbook.test/mcp/chatgpt", SecurityRevision: 1}
	// An owned-looking URL alone is insufficient: v1 connected apps reuse actual
	// catalog references while Tellbook's browser continues accepting new uploads.
	unknown := strings.Replace(input, "/shared.jpg", "/not-uploaded.jpg", 1)
	_, e = h.MutateCatalog(ctx, connected, "create_service", uuid.Nil, []byte(unknown), 0, "unknown-image")
	var imageError *CatalogError
	if !errors.As(e, &imageError) || imageError.Code != "invalid_image" {
		t.Fatal("unreferenced MCP image accepted", e)
	}
	imageCopy, e := h.MutateCatalog(ctx, connected, "create_service", uuid.Nil, []byte(input), 0, "existing-image")
	if e != nil {
		t.Fatal("existing MCP image rejected", e)
	}
	if _, e = pool.Exec(ctx, `DELETE FROM services WHERE id=$1`, imageCopy.ResourceID); e != nil {
		t.Fatal(e)
	}
	section := mutate("create_service_section", uuid.Nil, 0, `{"name":"Synthetic section"}`, "section")
	for _, op := range []string{"get_service", "get_service_section"} {
		target := id
		if op == "get_service_section" {
			target = uuid.MustParse(section.ResourceID)
		}
		_, e := h.ReadCatalog(ctx, other, op, target)
		var ce *CatalogError
		if !errors.As(e, &ce) || ce.Code != "not_found" {
			t.Fatalf("foreign read %s: %v", op, e)
		}
	}
	for _, op := range []string{"update_service", "replace_service", "duplicate_service", "set_service_status", "set_service_visibility", "delete_service", "update_service_section", "replace_service_section", "delete_service_section"} {
		target, rev := id, source.Revision
		if op == "update_service_section" || op == "replace_service_section" || op == "delete_service_section" {
			target, rev = uuid.MustParse(section.ResourceID), section.Revision
		}
		_, e := h.MutateCatalog(ctx, outsider, op, target, []byte(`{}`), rev, op)
		var ce *CatalogError
		if !errors.As(e, &ce) || ce.Code != "not_found" {
			t.Fatalf("foreign mutation %s: %v", op, e)
		}
	}
	foreignLocation := insertMarketplaceTestLocation(t, ctx, pool, other, "Synthetic address", "Test city", 6.4, 3.4)
	_, e = h.MutateCatalog(ctx, actor, "update_service", id, []byte(`{"fulfillment":{"mode":"provider_location","provider_location_id":"`+foreignLocation.String()+`"}}`), source.Revision, "foreign-location")
	if e == nil {
		t.Fatal("foreign location accepted")
	}
	_, e = h.MutateCatalog(ctx, outsider, "create_service", uuid.Nil, []byte(input), 0, "foreign-image")
	var ce *CatalogError
	if !errors.As(e, &ce) || ce.Code != "invalid_image" {
		t.Fatal("foreign image accepted", e)
	}
	templates, err := agreementseed.SystemTemplates()
	if err != nil {
		t.Fatal(err)
	}
	template := templates[0]
	publishTemplate := func(owner uuid.UUID) uuid.UUID {
		t.Helper()
		family, version := uuid.New(), uuid.New()
		document, _ := json.Marshal(template.Document)
		if _, err = pool.Exec(ctx, `INSERT INTO agreement_template_families(id,client_id,owner_type,title,description,category,tags,confirmation_method,status,created_by_client_id) VALUES($1,$2,'client','Synthetic template',$3,$4,$5,$6,'published',$2)`, family, owner, template.Description, template.Category, template.Tags, template.ConfirmationMethod); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO agreement_template_versions(id,family_id,version_number,state,document_schema,used_variable_keys,schema_version,renderer_version,source_kind,template_schema_hash,revision,published_at,created_by_client_id) VALUES($1,$2,1,'published',$3,$4,$5,$6,'system_seed',$7,1,now(),$8)`, version, family, document, template.UsedVariableKeys, template.Document.SchemaVersion, agreementrender.RendererVersion, template.TemplateSchemaHash, owner); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE agreement_template_families SET current_published_version_id=$2 WHERE id=$1`, family, version); err != nil {
			t.Fatal(err)
		}
		return family
	}
	ownTemplate, foreignTemplate := publishTemplate(provider), publishTemplate(other)
	_, e = h.MutateCatalog(ctx, actor, "update_service", id, []byte(`{"agreement_template_family_id":"`+foreignTemplate.String()+`","agreement_timing":"before_payment"}`), source.Revision, "foreign-template")
	if !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign published template accepted", e)
	}
	setup, e := h.CatalogSetup(ctx, provider)
	if e != nil {
		t.Fatal(e)
	}
	choices := setup["agreement_templates"].([]map[string]string)
	if len(choices) != 1 || choices[0]["id"] != ownTemplate.String() {
		t.Fatal("setup exposed another provider's templates")
	}
	if len(setup["locations"].([]BusinessLocationItem)) != 0 {
		t.Fatal("setup exposed another provider's locations")
	}
	// Array replacement and nested merge preserve unrelated settings; a child mutation bumps revision.
	changed := mutate("update_service", id, source.Revision, `{"short_notice_rules":[{"threshold_minutes":120,"surcharge_type":"fixed_amount","surcharge_amount_minor":"500"}]}`, "rules")
	var item ManagedServiceItem
	_ = json.Unmarshal(changed.Data, &item)
	if len(item.ShortNoticeRules) != 1 || item.DurationMinutes != 45 {
		t.Fatal("child patch lost fields")
	}
	if _, e = pool.Exec(ctx, `UPDATE service_short_notice_rules SET surcharge_amount_minor=600 WHERE service_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	item, e = h.repo.GetManagedServiceDetails(ctx, provider, id)
	if e != nil || item.Revision <= changed.Revision {
		t.Fatal("child edit did not invalidate revision", e)
	}
	cleared := mutate("update_service", id, item.Revision, `{"short_notice_rules":[]}`, "clear-rules")
	_ = json.Unmarshal(cleared.Data, &item)
	if len(item.ShortNoticeRules) != 0 {
		t.Fatal("array did not replace")
	}
	copy := mutate("duplicate_service", id, cleared.Revision, `{}`, "copy")
	// A quote prevents deletion; rollback preserves resource and creates no receipt.
	quote := uuid.New()
	_, e = pool.Exec(ctx, `INSERT INTO booking_quotes(id,public_token,client_id,service_id,service_title,business_name,duration_minutes,appointment_start_at,appointment_end_at,occupied_start_at,occupied_end_at,timezone,fulfillment_mode,location_label,country_code,currency_code,locale,base_service_amount_minor,discounted_service_amount_minor,total_amount_minor,deposit_amount_minor,remaining_amount_minor,customer_email_normalized,expires_at) VALUES($1,$2,$3,$4,'Synthetic media','Synthetic studio',45,now()+interval '1 day',now()+interval '1 day 45 minutes',now()+interval '1 day',now()+interval '1 day 45 minutes','Africa/Lagos','virtual','Online','NG','NGN','en-NG',25000,25000,25000,0,25000,'synthetic@example.invalid',now()+interval '1 hour')`, quote, uuid.NewString(), provider, id)
	if e != nil {
		t.Fatal(e)
	}
	_, e = h.MutateCatalog(ctx, actor, "delete_service", id, []byte(`{}`), cleared.Revision, "in-use")
	if !errors.As(e, &ce) || ce.Code != "service_in_use" {
		t.Fatal("dependency not preserved", e)
	}
	if _, e = pool.Exec(ctx, `DELETE FROM booking_quotes WHERE id=$1`, quote); e != nil {
		t.Fatal(e)
	}
	mutate("delete_service", id, cleared.Revision, `{}`, "delete-source")
	if deleted.Load() != 0 {
		t.Fatal("deleted shared image")
	}
	copyID := uuid.MustParse(copy.ResourceID)
	customer, booking := uuid.New(), uuid.New()
	if _, e = pool.Exec(ctx, `INSERT INTO customers(id,client_id,full_name) VALUES($1,$2,'Synthetic customer')`, customer, provider); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO bookings(id,client_id,customer_id,service_id,title,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,image_url) VALUES($1,$2,$3,$4,'Historical consultation',now()-interval '2 days',now()-interval '2 days'+interval '45 minutes',now()-interval '2 days',now()-interval '2 days'+interval '45 minutes','NGN','NG',$5)`, booking, provider, customer, copyID, image); e != nil {
		t.Fatal(e)
	}
	mutate("delete_service", copyID, copy.Revision, `{}`, "delete-copy")
	var serviceID *uuid.UUID
	var title string
	if e = pool.QueryRow(ctx, `SELECT service_id,title FROM bookings WHERE id=$1`, booking).Scan(&serviceID, &title); e != nil || serviceID != nil || title != "Historical consultation" {
		t.Fatal("booking history damaged", e)
	}
	if deleted.Load() != 0 {
		t.Fatal("deleted booking history image")
	}
	if _, e = pool.Exec(ctx, `DELETE FROM bookings WHERE id=$1`, booking); e != nil {
		t.Fatal(e)
	}
	h.deleteReplacedPublicImage(context.Background(), provider, image, "", "services")
	if deleted.Load() != 1 {
		t.Fatal("unreferenced image was not removed")
	}
	// Receipt retrieval is always scoped to the authenticated provider.
	var receiptCount int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM catalog_mutation_receipts WHERE id=$1 AND provider_id=$2`, source.ReceiptID, other).Scan(&receiptCount); e != nil || receiptCount != 0 {
		t.Fatal("foreign receipt visible", e)
	}
}
