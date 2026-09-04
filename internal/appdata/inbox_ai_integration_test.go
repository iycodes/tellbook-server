package appdata

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInboxAIDraftRunIsStableIdempotentLinkedAndStaleSafe(t *testing.T) {
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

	var clientID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM clients ORDER BY created_at LIMIT 1`).Scan(&clientID); err != nil {
		t.Skipf("no provider fixture is available: %v", err)
	}
	marketplaceCustomerID, customerID, conversationID := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_conversations WHERE id=$1`, conversationID)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE id=$1`, customerID)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID)
	})
	email := "ai-inbox-" + marketplaceCustomerID.String() + "@example.com"
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id,full_name,email,email_verified_at)
		VALUES ($1,'AI Inbox Customer',$2,NOW())
	`, marketplaceCustomerID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id,client_id,full_name,email)
		VALUES ($1,$2,'AI Inbox Customer',$3)
	`, customerID, clientID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inbox_conversations (id,client_id,customer_id,marketplace_customer_id)
		VALUES ($1,$2,$3,$4)
	`, conversationID, clientID, customerID, marketplaceCustomerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inbox_participant_states (conversation_id,participant_type,participant_id)
		VALUES ($1,'provider',$2),($1,'marketplace_customer',$3)
	`, conversationID, clientID, marketplaceCustomerID); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	first, err := repo.LoadProviderInboxAIDraftContext(ctx, clientID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := repo.LoadProviderInboxAIDraftContext(ctx, clientID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContextHash != second.ContextHash {
		t.Fatalf("stable context hashes differ: %s != %s", first.ContextHash, second.ContextHash)
	}
	if first.PromptInputHash == second.PromptInputHash {
		t.Fatal("exact prompt hash did not capture generation time")
	}

	requestID := uuid.New()
	runID, err := repo.StartProviderInboxAIDraftRun(
		ctx, clientID, conversationID, requestID, "self_hosted", "test-gemma",
		strings64("a"), first,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_runs SET available_at=NOW()-INTERVAL '100 years' WHERE id=$1
	`, runID); err != nil {
		t.Fatal(err)
	}
	claimWorker := &InboxAIDraftWorker{
		repo: repo,
		config: InboxAIDraftWorkerConfig{
			MaxConcurrency: 1,
			JobTimeout:     time.Minute,
		},
	}
	const firstWorkerID = "inbox-ai-integration"
	claimed, err := claimWorker.claim(ctx, firstWorkerID)
	if err != nil || claimed.ID != runID {
		t.Fatalf("claimed inbox AI draft = %s, error = %v; want %s", claimed.ID, err, runID)
	}
	if err := repo.CompleteProviderInboxAIDraftRun(ctx, runID, firstWorkerID, claimed.AttemptCount, aiapi.InboxReplyDraftResponse{
		Draft: "Hello, how can I help?", NeedsProviderInput: false,
	}, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	replay, err := repo.FindProviderInboxAIDraftRunByRequest(ctx, clientID, conversationID, requestID)
	if err != nil || replay == nil || replay.ID != runID || replay.Draft == "" {
		t.Fatalf("idempotent run lookup = %+v, error = %v", replay, err)
	}

	sent, err := repo.SendProviderMessage(
		ctx, clientID, conversationID, uuid.New(), "Hello, how can I help?", uuid.NullUUID{},
		uuid.NullUUID{UUID: runID, Valid: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	var outcome string
	var linkedMessageID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT provider_outcome, provider_message_id FROM inbox_ai_runs WHERE id=$1
	`, runID).Scan(&outcome, &linkedMessageID); err != nil {
		t.Fatal(err)
	}
	if outcome != "sent_unchanged" || linkedMessageID.String() != sent.Message.ID {
		t.Fatalf("run outcome/message = %s/%s, want sent_unchanged/%s", outcome, linkedMessageID, sent.Message.ID)
	}

	staleSnapshot, err := repo.LoadProviderInboxAIDraftContext(ctx, clientID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	staleRunID, err := repo.StartProviderInboxAIDraftRun(
		ctx, clientID, conversationID, uuid.New(), "self_hosted", "test-gemma", strings64("b"), staleSnapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE inbox_ai_runs SET available_at=NOW()-INTERVAL '1 second' WHERE id=$1`, staleRunID); err != nil {
		t.Fatal(err)
	}
	const staleWorkerID = "inbox-ai-stale-worker"
	staleClaim, err := claimWorker.claim(ctx, staleWorkerID)
	if err != nil || staleClaim.ID != staleRunID {
		t.Fatalf("first stale draft claim = %s, error = %v; want %s", staleClaim.ID, err, staleRunID)
	}
	if _, err := pool.Exec(ctx, `UPDATE inbox_ai_runs SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, staleRunID); err != nil {
		t.Fatal(err)
	}
	const replacementWorkerID = "inbox-ai-replacement-worker"
	replacementClaim, err := claimWorker.claim(ctx, replacementWorkerID)
	if err != nil || replacementClaim.ID != staleRunID {
		t.Fatalf("replacement stale draft claim = %s, error = %v; want %s", replacementClaim.ID, err, staleRunID)
	}
	if err := repo.CompleteProviderInboxAIDraftRun(
		ctx, staleRunID, staleWorkerID, staleClaim.AttemptCount,
		aiapi.InboxReplyDraftResponse{Draft: "Expired worker reply"}, 10*time.Millisecond,
	); err == nil {
		t.Fatal("expired inbox AI draft worker completed a reclaimed run")
	}
	if err := repo.CompleteProviderInboxAIDraftRun(
		ctx, staleRunID, replacementWorkerID, replacementClaim.AttemptCount,
		aiapi.InboxReplyDraftResponse{Draft: "Old context reply"}, 10*time.Millisecond,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SendMarketplaceMessage(ctx, marketplaceCustomerID, conversationID, uuid.New(), "A newer question", uuid.NullUUID{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SendProviderMessage(
		ctx, clientID, conversationID, uuid.New(), "Old context reply", uuid.NullUUID{},
		uuid.NullUUID{UUID: staleRunID, Valid: true},
	); !errors.Is(err, ErrInboxAIDraftStale) {
		t.Fatalf("stale AI send error = %v, want ErrInboxAIDraftStale", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM inbox_messages WHERE id=$1`, uuid.MustParse(sent.Message.ID)); err != nil {
		t.Fatalf("delete linked message while retaining AI audit: %v", err)
	}
	var retainedOutcome string
	var retainedMessageID uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT provider_outcome, provider_message_id FROM inbox_ai_runs WHERE id=$1
	`, runID).Scan(&retainedOutcome, &retainedMessageID); err != nil {
		t.Fatal(err)
	}
	if retainedOutcome != "sent_unchanged" || retainedMessageID.Valid {
		t.Fatalf("retained AI audit = %s/%v, want sent_unchanged with redacted message link", retainedOutcome, retainedMessageID)
	}
}

func strings64(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
