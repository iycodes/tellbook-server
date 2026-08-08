package appdata

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBusinessLocationLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var clientID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM clients ORDER BY created_at LIMIT 1`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	var originalPrimaryID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id
		FROM business_locations
		WHERE client_id = $1 AND is_active AND is_primary
	`, clientID).Scan(&originalPrimaryID); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	primary, err := repo.CreateBusinessLocation(ctx, clientID, UpsertBusinessLocationInput{
		Label:            "Location integration primary",
		FormattedAddress: "1 Test Avenue, Lagos",
		AddressSource:    "manual",
		Timezone:         "Africa/Lagos",
		IsPrimary:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := repo.CreateBusinessLocation(ctx, clientID, UpsertBusinessLocationInput{
		Label:            "Location integration secondary",
		FormattedAddress: "2 Test Avenue, Lagos",
		AddressSource:    "manual",
		Timezone:         "Africa/Lagos",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM business_locations WHERE id = ANY($1::uuid[])`, []uuid.UUID{
			uuid.MustParse(primary.ID),
			uuid.MustParse(secondary.ID),
		})
		_, _ = pool.Exec(ctx, `
			UPDATE business_locations
			SET is_primary = TRUE, updated_at = NOW()
			WHERE id = $1
		`, originalPrimaryID)
	})

	secondary, err = repo.UpdateBusinessLocation(ctx, clientID, uuid.MustParse(secondary.ID), UpsertBusinessLocationInput{
		Label:            secondary.Label,
		FormattedAddress: secondary.FormattedAddress,
		AddressSource:    secondary.AddressSource,
		Timezone:         secondary.Timezone,
		IsPrimary:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !secondary.IsPrimary {
		t.Fatal("expected updated location to be primary")
	}

	_, err = repo.UpdateBusinessLocation(ctx, uuid.New(), uuid.MustParse(secondary.ID), UpsertBusinessLocationInput{
		Label:            secondary.Label,
		FormattedAddress: secondary.FormattedAddress,
		AddressSource:    secondary.AddressSource,
		Timezone:         secondary.Timezone,
		IsPrimary:        true,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected tenant-safe not found error, got %v", err)
	}

	if err := repo.ArchiveBusinessLocation(ctx, clientID, uuid.MustParse(primary.ID)); err != nil {
		t.Fatal(err)
	}
	locations, err := repo.ListBusinessLocations(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	var archivedFound bool
	for _, location := range locations {
		if location.ID == primary.ID {
			archivedFound = !location.IsActive && !location.IsPrimary
		}
	}
	if !archivedFound {
		t.Fatal("expected archived location to remain listed as inactive")
	}
}
