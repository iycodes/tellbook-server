package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ineligibleTessaEmailService struct{}

func (ineligibleTessaEmailService) IssueTessaEmailLinkTx(context.Context, pgx.Tx, TessaEmailLinkRequest) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (ineligibleTessaEmailService) VerifyTessaEmailLinkTx(context.Context, pgx.Tx, uuid.UUID, string, string, string, string) (TessaEmailLinkResult, error) {
	return TessaEmailLinkResult{}, nil
}

func TestTessaOnboardingControlWorkerIntegration(t *testing.T) {
	for _, scenario := range []string{"send", "revision", "expired", "window", "stop", "disabled", "email_disabled", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, r, _, store := tessaLinkFixture(t)
			r.WithEmailLinking(ineligibleTessaEmailService{}, "https://provider.example.invalid")
			receipt := tessaInbound("tessa_onboarding", "hello", "2348142751683")
			t.Cleanup(func() {
				r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
				r.db.Exec(ctx, `DELETE FROM tessa_whatsapp_onboarding WHERE phone_number_id=$1 AND destination='+2348142751683'`, r.phoneID)
			})
			if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
				t.Fatal(err)
			}
			var anonymous bool
			var jobID uuid.UUID
			if err := r.db.QueryRow(ctx, `SELECT d.id,d.client_id IS NULL AND d.onboarding_id IS NOT NULL FROM tessa_whatsapp_outbox d JOIN meta_whatsapp_webhook_receipts q ON q.id=d.source_receipt_id WHERE q.dedupe_key=$1`, receipt.DedupeKey).Scan(&jobID, &anonymous); err != nil || !anonymous {
				t.Fatal("generic reply incorrectly bound to an account", err)
			}
			sender := &fakeTessaTextSender{}
			var err error
			switch scenario {
			case "revision":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_onboarding SET revision=revision+1 WHERE phone_number_id=$1 AND destination='+2348142751683'`, r.phoneID)
			case "expired":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_onboarding SET expires_at=NOW()-INTERVAL '1 second' WHERE phone_number_id=$1 AND destination='+2348142751683'`, r.phoneID)
			case "window":
				_, err = r.db.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET window_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, jobID)
			case "stop":
				stop := tessaInbound("stop", "", "2348142751683")
				err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{stop})
				t.Cleanup(func() {
					r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, stop.DedupeKey)
				})
			case "disabled":
				r.enabled = false
			case "email_disabled":
				r.emailLinks = nil
			case "unknown":
				sender.err = errors.New("ambiguous WhatsApp result")
			}
			if err != nil {
				t.Fatal(err)
			}
			w := NewTessaControlWorker(r, sender, nil)
			if _, err = w.processOne(ctx); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "send" || scenario == "unknown" {
				want = 1
			}
			if sender.calls != want {
				t.Fatal("unsafe anonymous reply dispatch")
			}
			if _, err = w.processOne(ctx); err != nil {
				t.Fatal(err)
			}
			if sender.calls != want {
				t.Fatal("duplicate or ambiguous reply redispatched")
			}
		})
	}
}

func TestTessaOnboardingBudgetsStaleIngressAndParsingIntegration(t *testing.T) {
	ctx, r, _, store := tessaLinkFixture(t)
	r.WithEmailLinking(ineligibleTessaEmailService{}, "https://provider.example.invalid")
	t.Cleanup(func() {
		r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE id IN (SELECT source_receipt_id FROM tessa_whatsapp_outbox WHERE onboarding_id IN (SELECT id FROM tessa_whatsapp_onboarding WHERE phone_number_id=$1 AND destination='+2348142751683'))`, r.phoneID)
		r.db.Exec(ctx, `DELETE FROM tessa_whatsapp_onboarding WHERE phone_number_id=$1 AND destination='+2348142751683'`, r.phoneID)
	})
	first := tessaInbound("tessa_onboarding", "hello", "2348142751683")
	if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{first, first}); err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"old", "future", "budget"} {
		receipt := tessaInbound("tessa_onboarding", "CONNECT", "2348142751683")
		at := time.Now()
		switch which {
		case "old":
			at = at.Add(-11 * time.Minute)
		case "future":
			at = at.Add(2 * time.Minute)
		case "budget":
			if _, err := r.db.Exec(ctx, `UPDATE tessa_whatsapp_onboarding SET reply_count=30 WHERE phone_number_id=$1 AND destination='+2348142751683'`, r.phoneID); err != nil {
				t.Fatal(err)
			}
		}
		receipt.ProviderTimestamp = &at
		if err := store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
		})
	}
	var count int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE onboarding_id IN (SELECT id FROM tessa_whatsapp_onboarding WHERE phone_number_id=$1 AND destination='+2348142751683')`, r.phoneID).Scan(&count); err != nil || count != 1 {
		t.Fatal("reply budget or stale receipt check bypassed", err)
	}
	if got := classifyInboundControl("2348142751683", "text", strings.Repeat("x", 321)); got.token != "" || got.kind != "tessa_onboarding" {
		t.Fatal("unbounded transient onboarding text")
	}
	if got := classifyInboundControl("2348142751683", "image", "private caption"); got.kind != "" {
		t.Fatal("unsupported content entered onboarding")
	}
}
