package appdata

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDataMaintenanceQueriesAreValidAndBounded(t *testing.T) {
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

	// A historical cutoff exercises parsing and planning without deleting any
	// current development data. The transaction is rolled back regardless.
	cutoff := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, task := range dataMaintenanceTasks {
		if _, err := pruneMaintenanceTask(ctx, tx, task, cutoff, 1); err != nil {
			t.Fatalf("maintenance task %s failed validation: %v", task.name, err)
		}
	}
}
