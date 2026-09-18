package appdata

import (
	"booking/go-server/internal/emailtest"
	"context"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestAdditionalEmailDeduplicationSurvivesRetentionIntegration(t *testing.T) {
	ctx := context.Background()
	f := emailtest.New(t, true)
	tx, err := f.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, kind := range []string{"booking_step_reminder", "booking_completed", "customer_booking_received"} {
		id := uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO notification_deliveries(id,idempotency_key,client_id,booking_id,booking_event_sequence,audience_type,channel,notification_type,template_key,scheduled_for,next_attempt_at,destination_hmac,status,accepted_at,completed_at) VALUES($1,$1::uuid::text,$2,$3,1,'customer','email',$4,$4,NOW()-INTERVAL '100 days',NOW()-INTERVAL '100 days',decode(repeat('ab',32),'hex'),'accepted',NOW()-INTERVAL '100 days',NOW()-INTERVAL '100 days')`, id, f.Client, f.Booking, kind)
		if err != nil {
			t.Fatal(err)
		}
	}
	found := false
	for _, task := range dataMaintenanceTasks {
		if task.name == "terminal_notification_deliveries" {
			found = true
			if _, err = pruneMaintenanceTask(ctx, tx, task, time.Now().UTC(), 1000); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found {
		t.Fatal("retention task missing")
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE booking_id=$1`, f.Booking).Scan(&count); err != nil || count != 2 {
		t.Fatalf("retained %d rows, want the two lifetime deduplication records: %v", count, err)
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE booking_id=$1 AND notification_type='customer_booking_received'`, f.Booking).Scan(&count); err != nil || count != 0 {
		t.Fatal("ordinary queue retention changed", err)
	}
}
