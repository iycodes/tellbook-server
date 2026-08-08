package payments

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type webhookEventStoreStub struct {
	created bool
	err     error
}

func (s webhookEventStoreStub) StoreVerifiedWebhook(
	context.Context,
	StoreVerifiedWebhookInput,
) (StoredWebhookEvent, bool, error) {
	return StoredWebhookEvent{ID: uuid.MustParse("2ddf7316-02cf-4694-bca3-c9abf26feb67")}, s.created, s.err
}

type webhookVerifierStub struct {
	err error
}

func (s webhookVerifierStub) VerifyAndDecodeWebhook([]byte, http.Header) (VerifiedEvent, error) {
	if s.err != nil {
		return VerifiedEvent{}, s.err
	}
	return VerifiedEvent{
		ProviderEventID: "provider-event-1",
		EventType:       "charge.success",
		Normalized:      map[string]any{"reference": "payment-1"},
	}, nil
}

func TestProviderWebhookHandlerAcknowledgesStoredEventsWithOK(t *testing.T) {
	for _, created := range []bool{true, false} {
		t.Run(map[bool]string{true: "first delivery", false: "duplicate delivery"}[created], func(t *testing.T) {
			handler := &ProviderWebhookHandler{
				ledger: webhookEventStoreStub{created: created},
				verifiers: map[string]WebhookVerifier{
					"paystack": webhookVerifierStub{},
				},
			}
			router := chi.NewRouter()
			router.Route("/webhooks", handler.Routes)

			request := httptest.NewRequest(http.MethodPost, "/webhooks/paystack", strings.NewReader(`{"event":"charge.success"}`))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if !strings.Contains(response.Body.String(), `"accepted":true`) {
				t.Fatalf("body = %q, want accepted response", response.Body.String())
			}
		})
	}
}

func TestProviderWebhookHandlerRejectsInvalidSignature(t *testing.T) {
	handler := &ProviderWebhookHandler{
		ledger: webhookEventStoreStub{},
		verifiers: map[string]WebhookVerifier{
			"payaza": webhookVerifierStub{err: errors.New("invalid signature")},
		},
	}
	router := chi.NewRouter()
	router.Route("/webhooks", handler.Routes)

	request := httptest.NewRequest(http.MethodPost, "/webhooks/payaza", strings.NewReader(`{"event":"payment"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
