package authchallenge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type authWhatsAppCaptureSender struct {
	messages []whatsapp.TemplateMessage
	result   whatsapp.SendResult
	err      error
}

func (sender *authWhatsAppCaptureSender) SendTemplate(_ context.Context, message whatsapp.TemplateMessage) (whatsapp.SendResult, error) {
	sender.messages = append(sender.messages, message)
	return sender.result, sender.err
}

func TestClassifyWhatsAppSendErrorDoesNotRetryAmbiguousOutcomes(t *testing.T) {
	disposition, code, _ := classifyWhatsAppSendError(&whatsapp.TransportError{
		Cause: errors.New("connection reset"), Ambiguous: true,
	})
	if disposition != mailer.DispositionAmbiguous || code != "meta_outcome_unknown" {
		t.Fatalf("ambiguous classification = %q %q", disposition, code)
	}
	disposition, code, retryAfter := classifyWhatsAppSendError(&whatsapp.GraphError{
		HTTPStatus: 429, Code: 4, Class: whatsapp.ErrorClassRateLimit, RetryAfter: 12 * time.Second,
	})
	if disposition != mailer.DispositionRetryable || code != "meta_rate_limit_4" || retryAfter != 12*time.Second {
		t.Fatalf("rate-limit classification = %q %q %s", disposition, code, retryAfter)
	}
}

func TestWhatsAppAuthDeliveryAndCallbackLifecycle(t *testing.T) {
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
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	service, err := NewService(pool, Config{
		WhatsAppEnabled: true, EncryptionKeys: `{"v1":"` + key + `"}`,
		ActiveKey: "v1", DestinationKey: "whatsapp-auth-test-hmac-key-32-bytes",
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, realm, phone string
	}{
		{name: "provider", realm: RealmProvider, phone: "+2348012345601"},
		{name: "marketplace", realm: RealmMarketplaceCustomer, phone: "+2348012345602"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _ = pool.Exec(ctx, `DELETE FROM provider_auth_challenges WHERE identifier=$1`, test.phone)
			_, _ = pool.Exec(ctx, `DELETE FROM marketplace_auth_challenges WHERE identifier=$1`, test.phone)
			started, err := service.Start(ctx, StartRequest{
				Realm: test.realm, RawIdentifier: test.phone, Channel: ChannelWhatsApp, Purpose: PurposeSignIn,
			})
			if err != nil {
				t.Fatal(err)
			}
			var jobID uuid.UUID
			var templateKey string
			if err := pool.QueryRow(ctx, `
				SELECT id,template_key FROM auth_code_delivery_jobs
				WHERE COALESCE(provider_challenge_id,marketplace_challenge_id)=$1
			`, started.ChallengeID).Scan(&jobID, &templateKey); err != nil {
				t.Fatal(err)
			}
			if templateKey != string(whatsapp.TemplateAuthCode) {
				t.Fatalf("template key = %q", templateKey)
			}
			sender := &authWhatsAppCaptureSender{result: whatsapp.SendResult{MessageID: "wamid.auth." + jobID.String()}}
			worker := NewWhatsAppWorker(service, sender, nil, nil, nil, 1, 2*time.Second)
			if !worker.processDeliveryCycle(ctx) || len(sender.messages) != 1 {
				t.Fatalf("delivery cycle messages = %#v", sender.messages)
			}
			message := sender.messages[0]
			instruction := message.Values.Body["code_instruction"]
			code := strings.TrimPrefix(instruction, "Use the code ")
			if message.Key != whatsapp.TemplateAuthCode || message.Values.OpaqueCallbackData != jobID.String() ||
				!validCode(code) || instruction != "Use the code "+code || len(message.Values.Body) != 1 {
				t.Fatalf("auth WhatsApp message = %#v", message)
			}
			var state, providerMessageID string
			var ciphertext []byte
			var verifyExpiresAt *time.Time
			challengeTableName, _, _ := challengeTable(test.realm)
			query := `SELECT job.status,job.provider_message_id,job.payload_ciphertext,challenge.verify_expires_at
				FROM auth_code_delivery_jobs job JOIN ` + challengeTableName + ` challenge
				ON challenge.id=COALESCE(job.provider_challenge_id,job.marketplace_challenge_id)
				WHERE job.id=$1`
			if err := pool.QueryRow(ctx, query, jobID).Scan(&state, &providerMessageID, &ciphertext, &verifyExpiresAt); err != nil {
				t.Fatal(err)
			}
			if state != "accepted" || providerMessageID != sender.result.MessageID || ciphertext != nil || verifyExpiresAt == nil {
				t.Fatalf("accepted state=%q wamid=%q ciphertext=%d expires=%v", state, providerMessageID, len(ciphertext), verifyExpiresAt)
			}

			providerAt := time.Now().UTC().Truncate(time.Second)
			receipt := whatsapp.WebhookReceipt{
				DedupeKey:  fmt.Sprintf("%x", sha256.Sum256([]byte(jobID.String()))),
				BusinessID: "222", PhoneNumberID: "333", EventKind: "status",
				MessageID: sender.result.MessageID, MessageStatus: "delivered",
				ProviderTimestamp: &providerAt, CorrelationID: jobID.String(), ProcessingStatus: "pending",
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
			})
			if err := whatsapp.NewWebhookRepository(pool, nil).StoreWebhookReceipts(ctx, []whatsapp.WebhookReceipt{receipt}); err != nil {
				t.Fatal(err)
			}
			worker.processStatusCycle(ctx)
			if err := pool.QueryRow(ctx, `SELECT status FROM auth_code_delivery_jobs WHERE id=$1`, jobID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "delivered" {
				t.Fatalf("callback state = %q", state)
			}
			if _, err := service.Verify(ctx, test.realm, started.ChallengeID, code, PurposeSignIn, nil); err != nil {
				t.Fatalf("verify delivered code: %v", err)
			}
			_, _ = pool.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
			_, _ = pool.Exec(ctx, `DELETE FROM provider_auth_challenges WHERE identifier=$1`, test.phone)
			_, _ = pool.Exec(ctx, `DELETE FROM marketplace_auth_challenges WHERE identifier=$1`, test.phone)
		})
	}
}

func TestWhatsAppAuthAmbiguousOutcomeIsNotReplayedAndCallbackCanResolve(t *testing.T) {
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
	phone := "+2348012345603"
	_, _ = pool.Exec(ctx, `DELETE FROM provider_auth_challenges WHERE identifier=$1`, phone)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	service, err := NewService(pool, Config{
		WhatsAppEnabled: true, EncryptionKeys: `{"v1":"` + key + `"}`,
		ActiveKey: "v1", DestinationKey: "ambiguous-auth-test-hmac-key-32-bytes",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, StartRequest{
		Realm: RealmProvider, RawIdentifier: phone, Channel: ChannelWhatsApp, Purpose: PurposeSignIn,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_auth_challenges WHERE identifier=$1`, phone)
	})
	var jobID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM auth_code_delivery_jobs WHERE provider_challenge_id=$1`, started.ChallengeID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	sender := &authWhatsAppCaptureSender{err: &whatsapp.TransportError{
		Cause: errors.New("response lost after write"), Ambiguous: true,
	}}
	worker := NewWhatsAppWorker(service, sender, nil, nil, nil, 1, 2*time.Second)
	worker.processDeliveryCycle(ctx)
	var state string
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT status,payload_ciphertext FROM auth_code_delivery_jobs WHERE id=$1`, jobID).Scan(&state, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" || ciphertext != nil || len(sender.messages) != 1 {
		t.Fatalf("ambiguous state=%q ciphertext=%d sends=%d", state, len(ciphertext), len(sender.messages))
	}
	worker.processDeliveryCycle(ctx)
	if len(sender.messages) != 1 {
		t.Fatalf("ambiguous auth delivery was replayed %d times", len(sender.messages))
	}

	providerAt := time.Now().UTC().Truncate(time.Second)
	receipt := whatsapp.WebhookReceipt{
		DedupeKey:  fmt.Sprintf("%x", sha256.Sum256([]byte("ambiguous-"+jobID.String()))),
		BusinessID: "222", PhoneNumberID: "333", EventKind: "status",
		MessageID: "wamid.auth.ambiguous." + jobID.String(), MessageStatus: "delivered",
		ProviderTimestamp: &providerAt, CorrelationID: jobID.String(), ProcessingStatus: "pending",
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
	})
	if err := whatsapp.NewWebhookRepository(pool, nil).StoreWebhookReceipts(ctx, []whatsapp.WebhookReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	worker.processStatusCycle(ctx)
	if err := pool.QueryRow(ctx, `SELECT status FROM auth_code_delivery_jobs WHERE id=$1`, jobID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		t.Fatalf("resolved ambiguous state = %q", state)
	}
	instruction := sender.messages[0].Values.Body["code_instruction"]
	code := strings.TrimPrefix(instruction, "Use the code ")
	if _, err := service.Verify(ctx, RealmProvider, started.ChallengeID, code, PurposeSignIn, nil); err != nil {
		t.Fatalf("verify callback-resolved code: %v", err)
	}
}
