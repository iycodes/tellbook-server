package appdata

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"testing"
)

func TestTessaNamedAvailabilityFiltersBeforeTheServiceLimit(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	client := insertTessaTestClient(t, ctx, pool)
	handle := "availability-" + client.String()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, client)
	exec(`INSERT INTO client_profiles(client_id,business_name,handle_slug,category,headline,short_bio,public_location_label,city,region,timezone,country_code,currency_code,locale,market_configured_at,marketplace_enabled)
	 VALUES($1,'Availability Studio',$2,'Consulting','Consultations','Planning','Lagos','Lagos','Lagos','Africa/Lagos','NG','NGN','en-NG',NOW(),TRUE)`, client, handle)
	target := uuid.New()
	for i := 0; i < 6; i++ {
		id, title := uuid.New(), fmt.Sprintf("Alpha %d", i)
		if i == 5 {
			id, title = target, "Zeta %_ Consultation"
		}
		exec(`INSERT INTO services(id,client_id,title,slug,description,duration_minutes,price_amount_minor,is_active,status,currency_code,fulfillment_mode,agreement_timing,standalone_signature_required)
		 VALUES($1,$2,$3,$4,'Planning session',60,2500000,TRUE,'published','NGN','virtual',NULL,FALSE)`, id, client, title, fmt.Sprintf("service-%d", i))
	}
	from, _, err := tessaDateRange("2026-10-12", "2026-10-12", "Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	result, err := repo.GetTessaAvailability(ctx, client, uuid.Nil, "%_", from, 1)
	if err != nil || len(result.Services) != 1 || result.Services[0].ServiceID != target.String() || result.HasMore {
		t.Fatal(result, err)
	}
	if _, err = repo.GetTessaAvailability(ctx, uuid.New(), uuid.Nil, "%_", from, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("named availability crossed tenants", err)
	}
	if _, err = repo.GetTessaAvailability(ctx, client, uuid.Nil, "absent", from, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing service was treated as empty slots", err)
	}
}
