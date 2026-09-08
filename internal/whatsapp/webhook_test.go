package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type webhookStoreStub struct {
	receipts []WebhookReceipt
	err      error
}

func (store *webhookStoreStub) StoreWebhookReceipts(_ context.Context, receipts []WebhookReceipt) error {
	store.receipts = append(store.receipts, receipts...)
	return store.err
}

func TestWebhookVerificationHandshake(t *testing.T) {
	handler, _ := newTestWebhookHandler(t, &webhookStoreStub{})
	request := httptest.NewRequest(http.MethodGet, "/?hub.mode=subscribe&hub.verify_token=verify-token-123456&hub.challenge=challenge-1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "challenge-1" {
		t.Fatalf("valid handshake = %d %q", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=challenge-1", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("invalid handshake status = %d", response.Code)
	}
}

func TestWebhookPersistsConfiguredPhoneStatusWithoutRawContent(t *testing.T) {
	store := &webhookStoreStub{}
	handler, secret := newTestWebhookHandler(t, store)
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"333"},"statuses":[{"id":"wamid.status-1","status":"delivered","timestamp":"1788523200","biz_opaque_callback_data":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}],"messages":[{"id":"wamid.inbound-1","from":"2348012345678","timestamp":"1788523201","type":"text","text":{"body":"private body"}}]}}]}]}`
	response := sendSignedWebhook(handler, secret, body)
	if response.Code != http.StatusOK || response.Body.String() != `{"accepted":true}` {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if len(store.receipts) != 2 {
		t.Fatalf("stored receipts = %#v", store.receipts)
	}
	if store.receipts[0].MessageStatus != "delivered" || store.receipts[0].ProcessingStatus != "pending" {
		t.Fatalf("status receipt = %#v", store.receipts[0])
	}
	if store.receipts[0].CorrelationID != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("status correlation = %q", store.receipts[0].CorrelationID)
	}
	if store.receipts[1].EventKind != "inbound_message" || store.receipts[1].ProcessingStatus != "completed" {
		t.Fatalf("inbound receipt = %#v", store.receipts[1])
	}
	if store.receipts[1].control.kind != "tessa_onboarding" {
		t.Fatal("text was not routed to transient, gated onboarding")
	}
	for _, receipt := range store.receipts {
		encoded := receipt.MessageID + receipt.MessageStatus + receipt.ProviderErrorCode
		if strings.Contains(encoded, "private body") || strings.Contains(encoded, "2348012345678") {
			t.Fatalf("receipt retained inbound content or sender: %#v", receipt)
		}
	}
}

func TestWebhookDropsMalformedOpaqueCorrelation(t *testing.T) {
	store := &webhookStoreStub{}
	handler, secret := newTestWebhookHandler(t, store)
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"333"},"statuses":[{"id":"wamid.status-invalid-correlation","status":"sent","timestamp":"1788523200","biz_opaque_callback_data":"not-a-delivery-id"}]}}]}]}`
	response := sendSignedWebhook(handler, secret, body)
	if response.Code != http.StatusOK || len(store.receipts) != 1 || store.receipts[0].CorrelationID != "" {
		t.Fatalf("malformed correlation response=%d receipts=%#v", response.Code, store.receipts)
	}
}

func TestWebhookRejectsBadSignatureBeforeStorage(t *testing.T) {
	store := &webhookStoreStub{}
	handler, _ := newTestWebhookHandler(t, store)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"object":"whatsapp_business_account"}`))
	request.Header.Set("X-Hub-Signature-256", "sha256=00")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || len(store.receipts) != 0 {
		t.Fatalf("bad signature response=%d receipts=%d", response.Code, len(store.receipts))
	}
}

func TestWebhookAcknowledgesOtherPhoneWithoutStorage(t *testing.T) {
	store := &webhookStoreStub{}
	handler, secret := newTestWebhookHandler(t, store)
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"999"},"statuses":[{"id":"wamid.status-1","status":"sent","timestamp":"1788523200"}]}}]}]}`
	response := sendSignedWebhook(handler, secret, body)
	if response.Code != http.StatusOK || len(store.receipts) != 0 {
		t.Fatalf("other phone response=%d receipts=%d", response.Code, len(store.receipts))
	}
}

func TestWebhookAcknowledgesOtherBusinessWithoutStorage(t *testing.T) {
	store := &webhookStoreStub{}
	handler, secret := newTestWebhookHandler(t, store)
	body := `{"object":"whatsapp_business_account","entry":[{"id":"999","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"333"},"statuses":[{"id":"wamid.status-1","status":"sent","timestamp":"1788523200"}]}}]}]}`
	response := sendSignedWebhook(handler, secret, body)
	if response.Code != http.StatusOK || len(store.receipts) != 0 {
		t.Fatalf("other business response=%d receipts=%d", response.Code, len(store.receipts))
	}
}

func TestWebhookRejectsOversizedBody(t *testing.T) {
	store := &webhookStoreStub{}
	handler, err := NewWebhookHandler(WebhookConfig{
		AppSecret: "meta-app-secret-at-least-sixteen", VerifyToken: "verify-token-123456",
		BusinessID: "222", PhoneNumberID: "333", MaxBodyBytes: 1024,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	response := sendSignedWebhook(handler, "meta-app-secret-at-least-sixteen", strings.Repeat("x", 1025))
	if response.Code != http.StatusRequestEntityTooLarge || len(store.receipts) != 0 {
		t.Fatalf("oversized response=%d receipts=%d", response.Code, len(store.receipts))
	}
}

func TestWebhookLogsDoNotContainInboundContentOrSender(t *testing.T) {
	store := &webhookStoreStub{}
	var logs bytes.Buffer
	secret := "meta-app-secret-at-least-sixteen"
	handler, err := NewWebhookHandler(WebhookConfig{
		AppSecret: secret, VerifyToken: "verify-token-123456", BusinessID: "222", PhoneNumberID: "333",
	}, store, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"999"},"messages":[{"id":"wamid.private","from":"2348012345678","timestamp":"1788523201","type":"text","text":{"body":"private body marker"}}]}}]}]}`
	response := sendSignedWebhook(handler, secret, body)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d", response.Code)
	}
	for _, forbidden := range []string{"private body marker", "2348012345678", "wamid.private"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("webhook logs contained %q: %s", forbidden, logs.String())
		}
	}
}

func TestWebhookReturnsRetryableFailureWhenStorageFails(t *testing.T) {
	store := &webhookStoreStub{err: errors.New("database unavailable")}
	handler, secret := newTestWebhookHandler(t, store)
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"333"},"statuses":[{"id":"wamid.status-1","status":"sent","timestamp":"1788523200"}]}}]}]}`
	response := sendSignedWebhook(handler, secret, body)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage failure status = %d", response.Code)
	}
}

func TestWebhookDedupeKeyIsStableAndStatusSpecific(t *testing.T) {
	first := receiptDedupeKey("333", "wamid.1", "sent", "100", "")
	if repeated := receiptDedupeKey("333", "wamid.1", "sent", "100", ""); repeated != first {
		t.Fatalf("dedupe key changed: %q != %q", repeated, first)
	}
	if delivered := receiptDedupeKey("333", "wamid.1", "delivered", "100", ""); delivered == first {
		t.Fatal("different status shared a dedupe key")
	}
}

func TestWebhookStoresUnknownStatusAsCompleted(t *testing.T) {
	store := &webhookStoreStub{}
	handler, secret := newTestWebhookHandler(t, store)
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"333"},"statuses":[{"id":"wamid.status-unknown","status":"future_status","timestamp":"1788523200"}]}}]}]}`
	response := sendSignedWebhook(handler, secret, body)
	if response.Code != http.StatusOK || len(store.receipts) != 1 || store.receipts[0].ProcessingStatus != "completed" {
		t.Fatalf("unknown status response=%d receipts=%#v", response.Code, store.receipts)
	}
}

func TestWebhookEnforcesStructuralEventLimit(t *testing.T) {
	store := &webhookStoreStub{}
	handler, err := NewWebhookHandler(WebhookConfig{
		AppSecret: "meta-app-secret-at-least-sixteen", VerifyToken: "verify-token-123456",
		BusinessID: "222", PhoneNumberID: "333", MaxEvents: 2,
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"333"},"statuses":[{"id":"wamid.1","status":"sent","timestamp":"1788523200"}]}}]}]}`
	response := sendSignedWebhook(handler, "meta-app-secret-at-least-sixteen", body)
	if response.Code != http.StatusBadRequest || len(store.receipts) != 0 {
		t.Fatalf("limit response=%d receipts=%d", response.Code, len(store.receipts))
	}
}

func newTestWebhookHandler(t *testing.T, store WebhookReceiptStore) (*WebhookHandler, string) {
	t.Helper()
	secret := "meta-app-secret-at-least-sixteen"
	handler, err := NewWebhookHandler(WebhookConfig{
		AppSecret: secret, VerifyToken: "verify-token-123456", BusinessID: "222", PhoneNumberID: "333",
	}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler, secret
}

func sendSignedWebhook(handler http.Handler, secret, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
