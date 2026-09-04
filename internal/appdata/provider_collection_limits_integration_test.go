package appdata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProviderCollectionLimits(t *testing.T) {
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

	clientID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id, full_name, email, password_hash)
		VALUES ($1, 'Collection limit test', $2, 'test')
	`, clientID, fmt.Sprintf("%s@tellbook.test", clientID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM clients WHERE id = $1`, clientID); err != nil {
			t.Errorf("delete collection-limit client: %v", err)
		}
	})
	repo := NewRepository(pool)

	t.Run("serializes concurrent portfolio creates at the cap", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO provider_portfolio_items (id, client_id, title, image_url, sort_order)
			SELECT gen_random_uuid(), $1, 'Photo ' || position,
				'https://storage.tellbook.test/photo-' || position, position
			FROM generate_series(1, $2::int) AS position
		`, clientID, MaxProviderPortfolioItems-1); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		results := make(chan error, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for index := 0; index < 2; index++ {
			go func(index int) {
				ready.Done()
				<-start
				_, createErr := repo.CreatePortfolioItem(ctx, clientID, CreatePortfolioItemInput{
					Title:    fmt.Sprintf("Concurrent photo %d", index),
					ImageURL: fmt.Sprintf("https://storage.tellbook.test/concurrent-%d", index),
				})
				results <- createErr
			}(index)
		}
		ready.Wait()
		close(start)

		var created, limited int
		for range 2 {
			switch result := <-results; {
			case result == nil:
				created++
			case errors.Is(result, ErrPortfolioItemLimitReached):
				limited++
			default:
				t.Fatalf("unexpected concurrent create result: %v", result)
			}
		}
		if created != 1 || limited != 1 {
			t.Fatalf("created=%d limited=%d, want 1 and 1", created, limited)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM provider_portfolio_items WHERE client_id = $1`, clientID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != MaxProviderPortfolioItems {
			t.Fatalf("portfolio count=%d, want %d", count, MaxProviderPortfolioItems)
		}
	})

	t.Run("rejects a section above the cap", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO service_sections (id, client_id, name, slug, sort_order)
			SELECT gen_random_uuid(), $1, 'Section ' || position, 'section-' || position, position
			FROM generate_series(1, $2::int) AS position
		`, clientID, MaxProviderServiceSections); err != nil {
			t.Fatal(err)
		}
		_, err := repo.CreateServiceSection(ctx, clientID, CreateServiceSectionInput{Name: "One too many"})
		if !errors.Is(err, ErrServiceSectionLimitReached) {
			t.Fatalf("create section error=%v, want ErrServiceSectionLimitReached", err)
		}
	})

	t.Run("rejects an active location above the cap", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO business_locations (
				id, client_id, label, formatted_address, address_source,
				resolution_status, timezone, is_primary, is_active
			)
			SELECT gen_random_uuid(), $1, 'Location ' || position,
				position || ' Test Avenue', 'manual', 'text_only', 'Africa/Lagos',
				position = 1, TRUE
			FROM generate_series(1, $2::int) AS position
		`, clientID, MaxProviderBusinessLocations); err != nil {
			t.Fatal(err)
		}
		_, err := repo.CreateBusinessLocation(ctx, clientID, UpsertBusinessLocationInput{
			Label:            "One too many",
			FormattedAddress: "21 Test Avenue",
			AddressSource:    "manual",
			Timezone:         "Africa/Lagos",
		})
		if !errors.Is(err, ErrBusinessLocationLimitReached) {
			t.Fatalf("create location error=%v, want ErrBusinessLocationLimitReached", err)
		}
	})

	t.Run("rejects create and duplicate service paths above the cap", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO services (
				id, client_id, title, slug, currency_code, status,
				fulfillment_mode, agreement_timing
			)
			SELECT gen_random_uuid(), $1, 'Service ' || position, 'service-' || position,
				'NGN', 'draft', 'virtual', NULL
			FROM generate_series(1, $2::int) AS position
		`, clientID, MaxProviderServices); err != nil {
			t.Fatal(err)
		}
		input := CreateManagedServiceInput{
			ServiceName:     "One too many",
			DurationMinutes: 30,
			Fulfillment:     ServiceFulfillmentConfig{Mode: "virtual"},
			Availability:    ServiceAvailabilityConfig{Mode: "inherit_business_hours"},
			PublishStatus:   "draft",
		}
		if _, err := repo.CreateManagedService(ctx, clientID, input); !errors.Is(err, ErrServiceLimitReached) {
			t.Fatalf("create service error=%v, want ErrServiceLimitReached", err)
		}
		var sourceID uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM services WHERE client_id = $1 LIMIT 1`, clientID).Scan(&sourceID); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.DuplicateManagedService(ctx, clientID, sourceID); !errors.Is(err, ErrServiceLimitReached) {
			t.Fatalf("duplicate service error=%v, want ErrServiceLimitReached", err)
		}
	})
}
