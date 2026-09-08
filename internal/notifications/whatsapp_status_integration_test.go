package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWhatsAppStatusReceiptCorrelationAndOrdering(t *testing.T) {
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
	repository, err := NewRepository(pool, "whatsapp-status-integration-key-32-bytes", nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	clientID, bookingID, _, _ := insertWhatsAppOutboundFixture(t, ctx, pool)
	deliveryID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_deliveries (
			id,idempotency_key,client_id,booking_id,booking_event_sequence,audience_type,
			channel,notification_type,template_key,scheduled_for,destination_hmac,
			status,next_attempt_at,dispatch_authorized_at,authorized_booking_event_sequence,
			authorized_preference_revision,reconcile_after,provider_status,provider_status_at
		) VALUES ($1,$2,$3,$4,0,'provider','whatsapp','provider_new_booking',
			'provider_new_booking',NOW(),$5,'dispatching',NOW(),NOW(),0,0,
			NOW()+INTERVAL '15 minutes','dispatching',NOW())
	`, deliveryID, "whatsapp-status-test:"+deliveryID.String(), clientID, bookingID,
		repository.destinationFingerprint("whatsapp", "+19999999999")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notification_deliveries WHERE id=$1`, deliveryID)
		_, _ = pool.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE correlation_id=$1`, deliveryID.String())
	})
	if _, err := repository.BuildWhatsAppMessage(ctx, Delivery{
		ID: deliveryID, DestinationHMAC: repository.destinationFingerprint("whatsapp", "+19999999999"),
	}); !errors.Is(err, ErrWhatsAppDestinationMoved) {
		t.Fatalf("WhatsApp context query did not reach the destination fence: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	sent := insertWhatsAppStatusReceipt(t, ctx, pool, deliveryID, "wamid.integration-order", "sent", base, 1)
	outcome, err := repository.ApplyWhatsAppStatusReceipt(ctx, sent)
	if err != nil || outcome != "applied" {
		t.Fatalf("sent callback outcome=%q err=%v", outcome, err)
	}
	if err := repository.MarkWhatsAppAccepted(ctx, deliveryID, sent.WAMID); err != nil {
		t.Fatalf("callback-before-accepted race was not tolerated: %v", err)
	}
	assertWhatsAppDeliveryStatus(t, ctx, pool, deliveryID, "sent", sent.WAMID)
	var acceptedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT accepted_at FROM notification_deliveries WHERE id=$1`, deliveryID).Scan(&acceptedAt); err != nil {
		t.Fatal(err)
	}
	if acceptedAt == nil {
		t.Fatal("callback-before-accepted race left accepted_at empty")
	}

	olderDelivered := insertWhatsAppStatusReceipt(t, ctx, pool, deliveryID, sent.WAMID, "delivered", base.Add(-time.Second), 2)
	outcome, err = repository.ApplyWhatsAppStatusReceipt(ctx, olderDelivered)
	if err != nil || outcome != "stale" {
		t.Fatalf("older delivered callback outcome=%q err=%v", outcome, err)
	}
	assertWhatsAppDeliveryStatus(t, ctx, pool, deliveryID, "sent", sent.WAMID)

	delivered := insertWhatsAppStatusReceipt(t, ctx, pool, deliveryID, sent.WAMID, "delivered", base.Add(time.Second), 3)
	outcome, err = repository.ApplyWhatsAppStatusReceipt(ctx, delivered)
	if err != nil || outcome != "applied" {
		t.Fatalf("delivered callback outcome=%q err=%v", outcome, err)
	}

	failed := insertWhatsAppStatusReceipt(t, ctx, pool, deliveryID, sent.WAMID, "failed", base.Add(2*time.Second), 4)
	outcome, err = repository.ApplyWhatsAppStatusReceipt(ctx, failed)
	if err != nil || outcome != "applied" {
		t.Fatalf("failed callback outcome=%q err=%v", outcome, err)
	}
	read := insertWhatsAppStatusReceipt(t, ctx, pool, deliveryID, sent.WAMID, "read", base.Add(3*time.Second), 5)
	outcome, err = repository.ApplyWhatsAppStatusReceipt(ctx, read)
	if err != nil || outcome != "stale" {
		t.Fatalf("terminal regression callback outcome=%q err=%v", outcome, err)
	}
	assertWhatsAppDeliveryStatus(t, ctx, pool, deliveryID, "failed", sent.WAMID)

	exhausted := insertWhatsAppStatusReceipt(t, ctx, pool, deliveryID, sent.WAMID, "sent", base.Add(4*time.Second), maxWhatsAppStatusAttempts)
	if _, err := pool.Exec(ctx, `
		UPDATE meta_whatsapp_webhook_receipts SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1
	`, exhausted.ID); err != nil {
		t.Fatal(err)
	}
	claimable := insertWhatsAppStatusReceipt(t, ctx, pool, deliveryID, sent.WAMID, "sent", base.Add(5*time.Second), 0)
	if _, err := pool.Exec(ctx, `
		UPDATE meta_whatsapp_webhook_receipts
		SET processing_status='pending',available_at=NOW()-INTERVAL '1 second',
			lease_owner='',lease_token=NULL,lease_expires_at=NULL
		WHERE id=$1
	`, claimable.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimWhatsAppStatusReceipts(ctx, "exhaustion-check", 1, time.Minute)
	if err != nil {
		t.Fatalf("claim with an exhausted poison receipt failed the batch: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != claimable.ID {
		t.Fatalf("claim after exhausted receipt = %#v, want %s", claimed, claimable.ID)
	}
	var processingStatus, lastErrorCode string
	if err := pool.QueryRow(ctx, `
		SELECT processing_status,last_error_code FROM meta_whatsapp_webhook_receipts WHERE id=$1
	`, exhausted.ID).Scan(&processingStatus, &lastErrorCode); err != nil {
		t.Fatal(err)
	}
	if processingStatus != "dead_letter" || lastErrorCode != "processing_retry_exhausted" {
		t.Fatalf("exhausted receipt status/error=%q/%q", processingStatus, lastErrorCode)
	}
}

func insertWhatsAppStatusReceipt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	correlationID uuid.UUID,
	wamid, status string,
	providerTimestamp time.Time,
	sequence int,
) WhatsAppStatusReceipt {
	t.Helper()
	id := uuid.New()
	leaseToken := uuid.New()
	digest := sha256.Sum256([]byte(id.String()))
	if _, err := pool.Exec(ctx, `
		INSERT INTO meta_whatsapp_webhook_receipts (
			id,dedupe_key,waba_id,phone_number_id,event_kind,wamid,message_status,
			provider_timestamp,correlation_id,processing_status,attempt_count,
			lease_owner,lease_token,lease_expires_at
		) VALUES ($1,$2,'222','333','status',$3,$4,$5,$6,'processing',$7,
			'whatsapp-status-integration',$8,NOW()+INTERVAL '1 minute')
	`, id, hex.EncodeToString(digest[:]), wamid, status, providerTimestamp,
		correlationID.String(), sequence, leaseToken); err != nil {
		t.Fatal(err)
	}
	return WhatsAppStatusReceipt{
		ID: id, WAMID: wamid, Status: status, ProviderTimestamp: providerTimestamp,
		CorrelationID: &correlationID, AttemptCount: sequence,
		LeaseOwner: "whatsapp-status-integration", LeaseToken: leaseToken,
	}
}

func assertWhatsAppDeliveryStatus(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	deliveryID uuid.UUID,
	wantStatus, wantWAMID string,
) {
	t.Helper()
	var status, wamid string
	if err := pool.QueryRow(ctx, `
		SELECT status,provider_message_id FROM notification_deliveries WHERE id=$1
	`, deliveryID).Scan(&status, &wamid); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || wamid != wantWAMID {
		t.Fatalf("delivery status/wamid=%q/%q want %q/%q", status, wamid, wantStatus, wantWAMID)
	}
}
