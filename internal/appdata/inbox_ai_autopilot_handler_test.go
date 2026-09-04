package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMarketplaceInboxAIBookingActorRequiresAuthenticatedCustomer(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/marketplace/conversations/invalid/ai-booking", nil)
	if _, _, ok := marketplaceInboxAIBookingActor(recorder, request); ok {
		t.Fatal("unauthenticated marketplace request was authorized")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestInboxAIProposalIDParamRejectsMalformedRouteAuthority(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("proposalID", "not-a-uuid")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	if _, ok := inboxAIProposalIDParam(recorder, request); ok {
		t.Fatal("malformed proposal authority was accepted")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestWriteInboxAIBookingErrorMapsPublicContract(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "stale", err: ErrInboxAIProposalStale, wantStatus: http.StatusConflict, wantCode: "stale_proposal"},
		{name: "payment window", err: ErrAutopilotPaymentWindowUnavailable, wantStatus: http.StatusConflict, wantCode: "stale_proposal"},
		{name: "idempotency", err: ErrInboxIdempotencyConflict, wantStatus: http.StatusConflict, wantCode: "idempotency_conflict"},
		{name: "agreement", err: ErrInboxAIAgreementRequired, wantStatus: http.StatusUnprocessableEntity, wantCode: "agreement_required"},
		{name: "wrapped service", err: errors.Join(ErrInboxAIServiceUnavailable, errors.New("disabled")), wantStatus: http.StatusUnprocessableEntity, wantCode: "service_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			if writeInboxAIBookingError(recorder, test.err) {
				t.Fatal("error mapper reported success")
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d", recorder.Code, test.wantStatus)
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error.Code != test.wantCode {
				t.Fatalf("code=%q, want %q", payload.Error.Code, test.wantCode)
			}
		})
	}
}
