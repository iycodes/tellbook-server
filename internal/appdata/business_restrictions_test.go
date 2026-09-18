package appdata

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAdminBusinessRestrictionBookingAndDiscovery(t *testing.T) {
	dsn := os.Getenv("ADMIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated admin database required")
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "tellbook_admin_test_") {
		t.Fatal("isolated admin database required", e)
	}
	ctx := context.Background()
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	category := uuid.New()
	if _, e = pool.Exec(ctx, `INSERT INTO marketplace_categories(id,slug,name) VALUES($1,$2,'Restriction test')`, category, "restriction-"+category.String()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := pool.Exec(ctx, `DELETE FROM marketplace_categories WHERE id=$1`, category); e != nil {
			t.Error(e)
		}
	})
	provider := insertMarketplaceTestProvider(t, ctx, pool, category, "approximate")
	service := insertMarketplaceTestService(t, ctx, pool, provider, uuid.Nil, "restricted-booking", "virtual", 0, nil)
	for day := time.Sunday; day <= time.Saturday; day++ {
		insertMarketplaceTestSchedule(t, ctx, pool, provider, day)
	}
	// These ephemeral domain records are removed before the provider helper cleanup.
	t.Cleanup(func() {
		for _, q := range []string{`UPDATE booking_quotes SET booking_id=NULL,consumed_at=NULL WHERE client_id=$1`, `DELETE FROM bookings WHERE client_id=$1`, `DELETE FROM booking_quotes WHERE client_id=$1`, `DELETE FROM customers WHERE client_id=$1`} {
			if _, e := pool.Exec(ctx, q, provider); e != nil {
				t.Error(e)
			}
		}
	})
	repo := NewRepository(pool)
	repo.ConfigureAgreementTokens(newTestAgreementTokenManager(t))
	slug := "marketplace-test-" + provider.String()[:12]
	availability, e := repo.BookingApplication().SearchAvailability(ctx, SearchBookingAvailabilityCommand{ProviderHandle: slug, ServiceID: service, Days: 7})
	if e != nil {
		t.Fatal(e)
	}
	var starts string
	for _, day := range availability.Dates {
		if len(day.Slots) > 0 {
			starts = day.Slots[0].StartAt
			break
		}
	}
	if starts == "" {
		t.Fatal("no test availability")
	}
	quote, e := repo.CreatePublicBookingQuote(ctx, slug, CreatePublicBookingQuoteInput{IdempotencyKey: uuid.NewString(), ServiceID: service.String(), StartsAt: starts, CustomerName: "Restriction Customer", CustomerEmail: "restriction@example.test", CustomerPhone: "+2348012345678"})
	if e != nil {
		t.Fatal(e)
	}
	input := CreatePublicBookingInput{QuoteToken: quote.QuoteToken, FullName: "Restriction Customer", Email: "restriction@example.test", Phone: "+2348012345678"}
	processMarketplaceProjectionProvider(t, ctx, pool, provider)
	setRestricted := func(restricted bool) {
		t.Helper()
		if _, e := pool.Exec(ctx, `UPDATE client_profiles SET platform_restricted=$2,updated_at=clock_timestamp() WHERE client_id=$1`, provider, restricted); e != nil {
			t.Fatal(e)
		}
	}
	before, e := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range before.Items {
		found = found || p.ID == provider.String()
	}
	if !found {
		t.Fatal("test provider was not discoverable before restriction")
	}
	setRestricted(true)
	// Leave the projection stale to prove public queries check authoritative state.
	results, e := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range results.Items {
		if p.ID == provider.String() {
			t.Fatal("restricted provider visible in stale discovery")
		}
	}
	home, e := repo.GetMarketplaceHome(ctx, "", nil, nil, &category, 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range home.Providers {
		if p.ID == provider.String() {
			t.Fatal("restricted provider remained in home discovery")
		}
	}
	for _, source := range []string{"", "marketplace", "direct_public_page"} {
		input.Source = source
		if _, e = repo.CreatePublicBooking(ctx, slug, input); !errors.Is(e, ErrBusinessRestricted) {
			t.Fatalf("source %q: %v", source, e)
		}
	}
	input.Source = ""
	setRestricted(false)
	booking, e := repo.CreatePublicBooking(ctx, slug, input)
	if e != nil {
		t.Fatal(e)
	}
	setRestricted(true)
	replay, e := repo.CreatePublicBooking(ctx, slug, input)
	if e != nil || replay.BookingID != booking.BookingID {
		t.Fatalf("restriction broke existing reservation replay: %v", e)
	}
	// The same lock used by final reservation creation excludes concurrent restriction.
	setRestricted(false)
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = ensureBusinessAcceptsNewBookings(ctx, tx, provider); e != nil {
		t.Fatal(e)
	}
	blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, e = pool.Exec(blocked, `UPDATE client_profiles SET platform_restricted=true WHERE client_id=$1`, provider)
	if e == nil {
		t.Fatal("restriction raced through reservation lock")
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	setRestricted(true)
	processMarketplaceProjectionProvider(t, ctx, pool, provider)
	var projected bool
	if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_provider_documents WHERE client_id=$1)`, provider).Scan(&projected); e != nil || projected {
		t.Fatal("restricted projection retained", e)
	}
	// Reuse the established full AI confirmation scenario with a real eligible
	// provider; do not let that scenario silently skip for missing seed data.
	setRestricted(false)
	t.Setenv("TEST_DATABASE_URL", dsn)
	t.Run("autopilot_confirmation", TestInboxAIAutopilotConfirmationCreatesOneExactReservationConcurrently)

}
