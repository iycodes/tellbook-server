package payments

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

const maxProviderWebhookBody = 1 << 20

type ProviderWebhookHandler struct {
	ledger    webhookEventStore
	verifiers map[string]WebhookVerifier
}

type webhookEventStore interface {
	StoreVerifiedWebhook(context.Context, StoreVerifiedWebhookInput) (StoredWebhookEvent, bool, error)
}

func NewProviderWebhookHandler(ledger *LedgerService, verifiers map[string]WebhookVerifier) *ProviderWebhookHandler {
	return &ProviderWebhookHandler{ledger: ledger, verifiers: verifiers}
}

func (h *ProviderWebhookHandler) Routes(r chi.Router) {
	for _, provider := range []string{"payaza", "paystack"} {
		if h.verifiers[provider] != nil {
			provider := provider
			r.Post("/"+provider, func(w http.ResponseWriter, r *http.Request) {
				h.handle(w, r, provider)
			})
		}
	}
}

func (h *ProviderWebhookHandler) handle(w http.ResponseWriter, r *http.Request, provider string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxProviderWebhookBody)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeWebhookError(w, http.StatusBadRequest, "invalid_webhook", "Webhook body is invalid.")
		return
	}
	verified, err := h.verifiers[provider].VerifyAndDecodeWebhook(rawBody, r.Header)
	if err != nil {
		writeWebhookError(w, http.StatusUnauthorized, "webhook_verification_failed", "Webhook verification failed.")
		return
	}
	event, _, err := h.ledger.StoreVerifiedWebhook(r.Context(), StoreVerifiedWebhookInput{
		Provider: provider, ProviderEventID: verified.ProviderEventID, EventType: verified.EventType,
		RawBody: rawBody, NormalizedEvent: verified.Normalized, VerifiedAt: time.Now().UTC(),
	})
	if errors.Is(err, ErrIdempotencyConflict) {
		writeWebhookError(w, http.StatusConflict, "webhook_conflict", "Webhook event conflicts with an existing event.")
		return
	}
	if err != nil {
		writeWebhookError(w, http.StatusServiceUnavailable, "webhook_storage_failed", "Webhook could not be stored.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"accepted":true,"event_id":"` + event.ID.String() + `"}`))
}

func writeWebhookError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `"}}`))
}
