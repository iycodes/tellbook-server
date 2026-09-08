package appdata

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

func TestCustomerContactReadPrivacy(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	provider := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	customer, owner, booking := uuid.New(), uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO marketplace_customers(id,email,email_verified_at) VALUES($1,$2,NOW())`, owner, owner.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, owner) })
	if _, err = pool.Exec(ctx, `INSERT INTO customers(id,client_id,full_name,email,phone) VALUES($1,$2,'Contact Customer','customer@example.com','+2348011111111')`, customer, provider); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM customers WHERE id=$1`, customer) })
	var token string
	if err = pool.QueryRow(ctx, `INSERT INTO bookings(id,client_id,customer_id,marketplace_customer_id,title,status,start_at,end_at,occupied_start_at,occupied_end_at,country_code,currency_code,timezone)
 VALUES($1,$2,$3,$4,'Test booking','confirmed',NOW()+INTERVAL '2 days',NOW()+INTERVAL '2 days 1 hour',NOW()+INTERVAL '2 days',NOW()+INTERVAL '2 days 1 hour','NG','NGN','Africa/Lagos') RETURNING public_token`, booking, provider, customer, owner).Scan(&token); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE id=$1`, booking) })
	repo := NewRepository(pool)
	handle := "marketplace-test-" + provider.String()[:12]
	for _, tc := range []struct{ public, booked bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		if _, err = pool.Exec(ctx, `UPDATE client_profiles SET customer_contact_phone='+2348098765432',customer_contact_verified_at=NOW(),
  show_contact_on_public_profile=$2,allow_booking_contact=$3 WHERE client_id=$1`, provider, tc.public, tc.booked); err != nil {
			t.Fatal(err)
		}
		profile, err := repo.getPublicProfile(ctx, handle)
		if err != nil {
			t.Fatal(err)
		}
		if (profile.CustomerContactPhone != "") != tc.public {
			t.Fatalf("public visibility=%v phone=%q", tc.public, profile.CustomerContactPhone)
		}
		summary, err := repo.GetPublicBookingSummary(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := repo.GetMarketplaceCustomerBooking(ctx, owner, booking)
		if err != nil {
			t.Fatal(err)
		}
		expected := ""
		if tc.booked {
			expected = "+2348098765432"
		}
		if summary.ProviderContactPhone != expected || detail.ProviderContactPhone != expected {
			t.Fatalf("booking visibility=%v summary=%q detail=%q", tc.booked, summary.ProviderContactPhone, detail.ProviderContactPhone)
		}
	}
	if _, err = repo.GetPublicBookingSummary(ctx, "invalid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad token: %v", err)
	}
	if _, err = repo.GetMarketplaceCustomerBooking(ctx, uuid.New(), booking); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner: %v", err)
	}
}
