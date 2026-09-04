package appdata

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBookingEventDrainReturnsBoundedPageAndHighWaterCursor(t *testing.T) {
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

	var bookingID, clientID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id,client_id FROM bookings ORDER BY created_at LIMIT 1`).Scan(&bookingID, &clientID); err != nil {
		t.Skipf("no booking fixture is available: %v", err)
	}
	repo := NewRepository(pool)
	before, err := repo.LatestBookingEventCursor(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	inserted := make([]uuid.UUID, 0, bookingEventDrainLimit+1)
	for range bookingEventDrainLimit + 1 {
		eventID := uuid.New()
		inserted = append(inserted, eventID)
		if _, err := pool.Exec(ctx, `
			INSERT INTO booking_domain_events (id,client_id,booking_id,event_type,dedupe_key,payload)
			VALUES ($1,$2,$3,'realtime.test',$4,'{}'::jsonb)
		`, eventID, clientID, bookingID, "realtime-test:"+eventID.String()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM booking_domain_events WHERE id=ANY($1::uuid[])`, inserted)
	})

	drain, err := repo.ListBookingEventsAfter(ctx, clientID, before, bookingEventDrainLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !drain.HasMore || len(drain.Events) != bookingEventDrainLimit {
		t.Fatalf("booking catch-up page has_more=%v events=%d", drain.HasMore, len(drain.Events))
	}
	latest, err := strconv.ParseInt(drain.LatestCursor, 10, 64)
	if err != nil || latest < before+bookingEventDrainLimit+1 {
		t.Fatalf("booking high-water cursor=%q before=%d error=%v", drain.LatestCursor, before, err)
	}
}
