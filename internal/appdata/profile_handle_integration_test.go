package appdata

import (
	"context"
	"errors"
	"os"
	"testing"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProfileHandleLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(adminPool.Close)

	schemaName := "profile_handle_test_" + uuid.NewString()[:8]
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA "+schemaName+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database config: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect isolated test schema: %v", err)
	}
	t.Cleanup(pool.Close)

	createProfileHandleTestSchema(t, ctx, pool)
	repository := NewRepository(pool)
	var scannedMinor money.Minor
	if err := pool.QueryRow(ctx, `SELECT 9007199254740993::bigint`).Scan(&scannedMinor); err != nil {
		t.Fatalf("scan minor amount: %v", err)
	}
	if scannedMinor != money.Minor(9007199254740993) {
		t.Fatalf("scanned minor amount = %d", scannedMinor)
	}
	firstClientID := uuid.New()
	secondClientID := uuid.New()
	for _, clientID := range []uuid.UUID{firstClientID, secondClientID} {
		if _, err := pool.Exec(ctx, `INSERT INTO clients (id) VALUES ($1)`, clientID); err != nil {
			t.Fatalf("insert client: %v", err)
		}
	}
	if err := repository.EnsureClientMarketConfigured(ctx, firstClientID); !errors.Is(err, ErrMarketNotConfigured) {
		t.Fatalf("unconfigured market error = %v, want ErrMarketNotConfigured", err)
	}

	firstHandle := "zenith-studio"
	if err := repository.UpdateClientProfile(ctx, firstClientID, profileHandleTestInput("Zenith Studio", &firstHandle)); err != nil {
		t.Fatalf("create first profile: %v", err)
	}
	if err := repository.UpdateClientMarket(ctx, firstClientID, UpdateClientMarketInput{
		CountryCode:  "NG",
		CurrencyCode: "NGN",
		Timezone:     "Africa/Lagos",
		Locale:       "en-NG",
	}); err != nil {
		t.Fatalf("configure first profile market: %v", err)
	}
	if err := repository.EnsureClientMarketConfigured(ctx, firstClientID); err != nil {
		t.Fatalf("configured market check: %v", err)
	}
	if err := repository.UpdateClientProfile(ctx, firstClientID, profileHandleTestInput("Zenith Studio Updated", nil)); err != nil {
		t.Fatalf("update profile without handle: %v", err)
	}
	assertCurrentProfileHandle(t, ctx, pool, firstClientID, firstHandle)

	secondHandle := "zenith-lash-studio"
	if err := repository.UpdateClientProfile(ctx, firstClientID, profileHandleTestInput("Zenith Lash Studio", &secondHandle)); err != nil {
		t.Fatalf("change profile handle: %v", err)
	}
	assertCurrentProfileHandle(t, ctx, pool, firstClientID, secondHandle)
	var firstClientClaimCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM client_profile_handles WHERE client_id = $1`,
		firstClientID,
	).Scan(&firstClientClaimCount); err != nil {
		t.Fatalf("count retained handles: %v", err)
	}
	if firstClientClaimCount != 2 {
		t.Fatalf("retained handle count = %d, want 2", firstClientClaimCount)
	}

	resolvedClientID, err := repository.GetClientIDByHandleSlug(ctx, firstHandle)
	if err != nil {
		t.Fatalf("resolve previous handle: %v", err)
	}
	if resolvedClientID != firstClientID {
		t.Fatalf("previous handle resolved to %s, want %s", resolvedClientID, firstClientID)
	}

	normalized, available, err := repository.CheckHandleSlugAvailability(ctx, firstClientID, firstHandle)
	if err != nil {
		t.Fatalf("check own previous handle: %v", err)
	}
	if normalized != firstHandle || !available {
		t.Fatalf("own previous handle availability = %q, %v", normalized, available)
	}

	if err := repository.UpdateClientProfile(ctx, secondClientID, profileHandleTestInput("Another Studio", &firstHandle)); !errors.Is(err, ErrHandleSlugTaken) {
		t.Fatalf("claim another client's handle error = %v, want ErrHandleSlugTaken", err)
	}

	_, available, err = repository.CheckHandleSlugAvailability(ctx, secondClientID, firstHandle)
	if err != nil {
		t.Fatalf("check another client's handle: %v", err)
	}
	if available {
		t.Fatal("another client's historical handle must remain unavailable")
	}
}

func createProfileHandleTestSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	schema := `
		CREATE TABLE clients (
			id UUID PRIMARY KEY
		);
		CREATE TABLE client_profile_handles (
			handle_slug TEXT PRIMARY KEY,
			client_id UUID NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (client_id, handle_slug)
		);
		CREATE TABLE client_profiles (
			client_id UUID PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
			business_name TEXT NOT NULL DEFAULT '',
			handle_slug TEXT NOT NULL UNIQUE,
			category TEXT NOT NULL DEFAULT '',
			headline TEXT NOT NULL DEFAULT '',
			short_bio TEXT NOT NULL DEFAULT '',
			public_profile_about TEXT NOT NULL DEFAULT '',
			booking_page_intro TEXT NOT NULL DEFAULT '',
			public_location_label TEXT NOT NULL DEFAULT '',
			city TEXT NOT NULL DEFAULT '',
			region TEXT NOT NULL DEFAULT '',
			hero_image_url TEXT,
			timezone TEXT,
			verified BOOLEAN NOT NULL DEFAULT FALSE,
			currency_code TEXT,
			country_code TEXT,
			locale TEXT,
			market_configured_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			FOREIGN KEY (client_id, handle_slug)
				REFERENCES client_profile_handles (client_id, handle_slug)
		);
		CREATE TABLE services (
			id UUID PRIMARY KEY,
			client_id UUID NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
			currency_code TEXT NOT NULL,
			status TEXT NOT NULL,
			is_hidden BOOLEAN NOT NULL DEFAULT FALSE
		);
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("create profile handle test tables: %v", err)
	}
}

func profileHandleTestInput(businessName string, handleSlug *string) UpdateClientProfileInput {
	return UpdateClientProfileInput{
		BusinessName: businessName,
		HandleSlug:   handleSlug,
	}
}

func assertCurrentProfileHandle(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	clientID uuid.UUID,
	want string,
) {
	t.Helper()

	var profileHandle string
	if err := pool.QueryRow(
		ctx,
		`SELECT handle_slug FROM client_profiles WHERE client_id = $1`,
		clientID,
	).Scan(&profileHandle); err != nil {
		t.Fatalf("load current profile handle: %v", err)
	}
	if profileHandle != want {
		t.Fatalf("profile handle = %q, want %q", profileHandle, want)
	}

	var registryOwner uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`SELECT client_id
		 FROM client_profile_handles
		 WHERE handle_slug = $1`,
		want,
	).Scan(&registryOwner); err != nil {
		t.Fatalf("load registry owner: %v", err)
	}
	if registryOwner != clientID {
		t.Fatalf("registry owner = %s, want %s", registryOwner, clientID)
	}

	var claimedCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM client_profile_handles WHERE client_id = $1`,
		clientID,
	).Scan(&claimedCount); err != nil {
		t.Fatalf("count claimed handles: %v", err)
	}
	if claimedCount < 1 {
		t.Fatalf("claimed handle count = %d, want at least 1", claimedCount)
	}
}
