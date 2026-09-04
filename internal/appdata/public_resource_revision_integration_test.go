package appdata

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublicProviderResourceRevisionTracksPublicMutations(t *testing.T) {
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

	clientID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	handle := "marketplace-test-" + clientID.String()[:12]
	repository := NewRepository(pool)
	revision := publicResourceRevisionForTest(t, ctx, repository, handle)

	if _, err := pool.Exec(ctx, `UPDATE client_profiles SET city = 'Ikeja', updated_at = NOW() WHERE client_id = $1`, clientID); err != nil {
		t.Fatal(err)
	}
	if unchanged := publicResourceRevisionForTest(t, ctx, repository, handle); unchanged != revision {
		t.Fatalf("non-public city update changed revision from %d to %d", revision, unchanged)
	}

	if _, err := pool.Exec(ctx, `UPDATE client_profiles SET headline = 'Updated public headline', updated_at = NOW() WHERE client_id = $1`, clientID); err != nil {
		t.Fatal(err)
	}
	revision = requirePublicResourceRevisionIncrease(t, ctx, repository, handle, revision)

	serviceID := insertMarketplaceTestService(t, ctx, pool, clientID, uuid.Nil, "revision-virtual-service", "virtual", 25000, nil)
	revision = requirePublicResourceRevisionIncrease(t, ctx, repository, handle, revision)

	if _, err := pool.Exec(ctx, `
		INSERT INTO service_short_notice_rules (
			id, service_id, threshold_minutes, surcharge_type,
			surcharge_amount_minor, surcharge_percentage_bps, created_at, updated_at
		) VALUES ($1,$2,120,'fixed_amount',1000,0,NOW(),NOW())
	`, uuid.New(), serviceID); err != nil {
		t.Fatal(err)
	}
	revision = requirePublicResourceRevisionIncrease(t, ctx, repository, handle, revision)

	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_portfolio_items (id, client_id, title, image_url, sort_order, created_at)
		VALUES ($1,$2,'Public work','https://example.test/work.webp',0,NOW())
	`, uuid.New(), clientID); err != nil {
		t.Fatal(err)
	}
	revision = requirePublicResourceRevisionIncrease(t, ctx, repository, handle, revision)

	pendingReviewID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_reviews (id, client_id, author_name, rating, review_text, status, created_at, updated_at)
		VALUES ($1,$2,'Pending reviewer',5,'Not public yet','pending',NOW(),NOW())
	`, pendingReviewID, clientID); err != nil {
		t.Fatal(err)
	}
	if unchanged := publicResourceRevisionForTest(t, ctx, repository, handle); unchanged != revision {
		t.Fatalf("pending review changed revision from %d to %d", revision, unchanged)
	}
	if _, err := pool.Exec(ctx, `UPDATE provider_reviews SET status = 'approved', updated_at = NOW() WHERE id = $1`, pendingReviewID); err != nil {
		t.Fatal(err)
	}
	revision = requirePublicResourceRevisionIncrease(t, ctx, repository, handle, revision)

	customerID := uuid.New()
	bookingID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id, client_id, full_name, email, created_at, updated_at)
		VALUES ($1,$2,'Revision customer','revision-customer@example.test',NOW(),NOW())
	`, customerID, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id, client_id, customer_id, service_id, title, status, start_at, end_at,
			currency_code, country_code, occupied_start_at, occupied_end_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,'Revision booking','confirmed',NOW() + INTERVAL '1 day',
			NOW() + INTERVAL '1 day 1 hour','NGN','NG',NOW() + INTERVAL '1 day',
			NOW() + INTERVAL '1 day 1 hour',NOW(),NOW())
	`, bookingID, clientID, customerID, serviceID); err != nil {
		t.Fatal(err)
	}
	if unchanged := publicResourceRevisionForTest(t, ctx, repository, handle); unchanged != revision {
		t.Fatalf("non-completed booking changed revision from %d to %d", revision, unchanged)
	}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET status = 'completed', updated_at = NOW() WHERE id = $1`, bookingID); err != nil {
		t.Fatal(err)
	}
	requirePublicResourceRevisionIncrease(t, ctx, repository, handle, revision)
}

func publicResourceRevisionForTest(t *testing.T, ctx context.Context, repository *Repository, handle string) int64 {
	t.Helper()
	revision, err := repository.PublicProviderResourceRevisionBySlug(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func requirePublicResourceRevisionIncrease(t *testing.T, ctx context.Context, repository *Repository, handle string, previous int64) int64 {
	t.Helper()
	revision := publicResourceRevisionForTest(t, ctx, repository, handle)
	if revision <= previous {
		t.Fatalf("public resource revision = %d, want greater than %d", revision, previous)
	}
	return revision
}
