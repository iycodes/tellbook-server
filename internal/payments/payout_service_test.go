package payments

import (
	"errors"
	"net/http"
	"testing"
)

type payoutHTTPError struct{ status int }

func (e payoutHTTPError) Error() string       { return "sensitive provider response" }
func (e payoutHTTPError) HTTPStatusCode() int { return e.status }

func TestNormalizePayoutInitiationStatus(t *testing.T) {
	if got := normalizePayoutInitiationStatus(PayoutStatusSuccessful); got != PayoutStatusPending {
		t.Fatalf("successful initiation normalized to %q", got)
	}
	if got := normalizePayoutInitiationStatus(PayoutStatus("provider_new_state")); got != PayoutStatusUnknown {
		t.Fatalf("unknown initiation normalized to %q", got)
	}
}

func TestNormalizePayoutReconciliationStatus(t *testing.T) {
	if got := normalizePayoutReconciliationStatus(PayoutStatusSuccessful); got != PayoutStatusSuccessful {
		t.Fatalf("successful reconciliation normalized to %q", got)
	}
	if got := normalizePayoutReconciliationStatus(PayoutStatusCreated); got != PayoutStatusUnknown {
		t.Fatalf("invalid reconciliation normalized to %q", got)
	}
}

func TestProviderFailureMessageDoesNotPersistProviderBody(t *testing.T) {
	message := providerFailureMessage(payoutHTTPError{status: http.StatusUnprocessableEntity})
	if message != "provider request failed with HTTP 422" {
		t.Fatalf("failure message = %q", message)
	}
	if message := providerFailureMessage(errors.New("secret transport details")); message != "provider request outcome is unknown" {
		t.Fatalf("transport failure message = %q", message)
	}
}
