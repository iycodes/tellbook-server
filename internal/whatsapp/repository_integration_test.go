package whatsapp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWebhookRepositoryStoresReceiptsIdempotently(t *testing.T) {
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

	timestamp := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	statusKey := receiptDedupeKey("333", "wamid.integration-status", "sent", "1788523200", "")
	inboundKey := receiptDedupeKey("333", "wamid.integration-inbound")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key IN ($1,$2)`, statusKey, inboundKey)
	})
	repository := NewWebhookRepository(pool, nil)
	receipts := []WebhookReceipt{
		{
			DedupeKey: statusKey, BusinessID: "222", PhoneNumberID: "333", EventKind: "status",
			MessageID: "wamid.integration-status", MessageStatus: "sent",
			ProviderTimestamp: &timestamp, ProcessingStatus: "pending",
		},
		{
			DedupeKey: inboundKey, BusinessID: "222", PhoneNumberID: "333", EventKind: "inbound_message",
			MessageID: "wamid.integration-inbound", ProviderTimestamp: &timestamp, ProcessingStatus: "completed",
		},
	}
	if err := repository.StoreWebhookReceipts(ctx, receipts); err != nil {
		t.Fatal(err)
	}
	if err := repository.StoreWebhookReceipts(ctx, receipts); err != nil {
		t.Fatalf("duplicate storage failed: %v", err)
	}
	var count int
	var pendingProcessedAt, inboundProcessedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*),
			MAX(processed_at) FILTER (WHERE dedupe_key=$1),
			MAX(processed_at) FILTER (WHERE dedupe_key=$2)
		FROM meta_whatsapp_webhook_receipts
		WHERE dedupe_key IN ($1,$2)
	`, statusKey, inboundKey).Scan(&count, &pendingProcessedAt, &inboundProcessedAt); err != nil {
		t.Fatal(err)
	}
	if count != 2 || pendingProcessedAt != nil || inboundProcessedAt == nil {
		t.Fatalf("stored rows count=%d pending_processed=%v inbound_processed=%v", count, pendingProcessedAt, inboundProcessedAt)
	}
}
