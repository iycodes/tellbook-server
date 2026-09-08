package seed

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProviderSeedMaintainsCanonicalEmailIdentity(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	providerID := uuid.New()
	email := "Seed-" + providerID.String() + "@Example.Test"
	now := time.Now().UTC()
	if err := upsertUser(ctx, tx, Input{
		ClientID: providerID,
		Email:    email,
		FullName: "Seed Provider",
	}, []byte("password-hash"), now, false); err != nil {
		t.Fatal(err)
	}
	var identity string
	if err := tx.QueryRow(ctx, `
		SELECT normalized_identifier
		FROM provider_auth_identities
		WHERE client_id=$1 AND identity_type='email'
	`, providerID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if want := "seed-" + providerID.String() + "@example.test"; identity != want {
		t.Fatalf("seed identity = %q, want %q", identity, want)
	}

	phoneOnlyID := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO clients (id,full_name) VALUES ($1,'Phone Provider')`, phoneOnlyID); err != nil {
		t.Fatal(err)
	}
	phoneOnlyEmail, exists, err := getExistingClientEmail(ctx, tx, phoneOnlyID)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || phoneOnlyEmail != "" {
		t.Fatalf("nullable provider email = %q, exists=%t", phoneOnlyEmail, exists)
	}
}
