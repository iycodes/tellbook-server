package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSystemTemplateSyncAndCopy(t *testing.T) {
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
	repository := New(pool)
	if err := repository.SyncSystemTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repository.SyncSystemTemplates(ctx); err != nil {
		t.Fatalf("second sync was not idempotent: %v", err)
	}
	items, err := repository.ListSystemTemplateFamilies(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 {
		t.Fatalf("system templates = %d, want at least 3", len(items))
	}
	var clientID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM clients ORDER BY created_at LIMIT 1`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	copiedID, err := repository.CopySystemTemplate(ctx, clientID, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agreement_template_families WHERE id = $1`, copiedID)
	})
	details, err := repository.GetClientTemplateFamily(ctx, clientID, copiedID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Draft == nil || details.Draft.Document == nil || details.Family.SourceFamilyID == nil {
		t.Fatalf("copied system template is incomplete: %#v", details)
	}
}
