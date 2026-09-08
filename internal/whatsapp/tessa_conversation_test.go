package whatsapp

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
	"time"
)

type captureTessaIngress struct{ messages []TessaInboundMessage }

func (s *captureTessaIngress) StoreTessaWhatsAppMessageTx(_ context.Context, _ pgx.Tx, in TessaInboundMessage) error {
	s.messages = append(s.messages, in)
	return nil
}

func TestTessaConversationRoutingExcludesLinkingControlsIntegration(t *testing.T) {
	ctx, r, clientID, store := tessaLinkFixture(t)
	challenge, err := r.Start(ctx, clientID, "+2348142751683", TessaWhatsAppNotice)
	if err != nil {
		t.Fatal(err)
	}
	receipt := tessaInbound("tessa_link", tessaChallengeToken(t, challenge), "2348142751683")
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	capture := &captureTessaIngress{}
	r.WithConversationIngress(capture)
	// A linked conversation does not depend on the email onboarding service.
	for _, text := range []string{"CONNECT", "AGREE", "RESEND", "123456", "person@example.invalid", "TESSA LINK malformed", "VERIFY malformed"} {
		receipt := tessaInbound("", "", "2348142751683")
		receipt.control = classifyInboundControl("2348142751683", "text", text)
		if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
		})
	}
	if len(capture.messages) != 0 {
		t.Fatal("linking data escaped into conversation ingress")
	}
	question := strings.Repeat("Booking question. ", 30)
	receipt = tessaInbound("", "", "2348142751683")
	// Conversation ingress must not inherit the anonymous onboarding TTL.
	delayed := time.Now().Add(-15 * time.Minute)
	receipt.ProviderTimestamp = &delayed
	receipt.control = classifyInboundControl("2348142751683", "text", question)
	if err = store.StoreWebhookReceipts(ctx, []WebhookReceipt{receipt, receipt}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.db.Exec(ctx, `DELETE FROM meta_whatsapp_webhook_receipts WHERE dedupe_key=$1`, receipt.DedupeKey)
	})
	if len(capture.messages) != 1 {
		t.Fatal("ordinary message lost or replayed")
	}
	in := capture.messages[0]
	if in.ClientID != clientID || in.ReceiptID == uuid.Nil || in.PhoneNumberID != r.phoneID || in.Sender != "+2348142751683" || in.Content != strings.TrimSpace(question) {
		t.Fatal("verified namespace or bounded content lost")
	}
}
