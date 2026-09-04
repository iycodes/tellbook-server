package appdata

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBookingChangeFingerprintIncludesRequestShape(t *testing.T) {
	bookingID := uuid.New()
	first := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	if bookingChangeFingerprint(bookingID, "reschedule", &first) == bookingChangeFingerprint(bookingID, "reschedule", &second) {
		t.Fatal("different reschedule slots produced the same fingerprint")
	}
	if bookingChangeFingerprint(bookingID, "cancellation", nil) != bookingChangeFingerprint(bookingID, "cancellation", nil) {
		t.Fatal("identical cancellation requests produced different fingerprints")
	}
}

func TestBookingCommandFingerprintIncludesQuoteAndReason(t *testing.T) {
	bookingID := uuid.New()
	base := bookingCommandFingerprint(bookingID, "cancel", "quote-one", "customer request")
	if base == bookingCommandFingerprint(bookingID, "cancel", "quote-two", "customer request") {
		t.Fatal("different quote tokens produced the same command fingerprint")
	}
	if base == bookingCommandFingerprint(bookingID, "cancel", "quote-one", "different reason") {
		t.Fatal("different reasons produced the same command fingerprint")
	}
	if base != bookingCommandFingerprint(bookingID, " cancel ", " quote-one ", " customer request ") {
		t.Fatal("equivalent normalized commands produced different fingerprints")
	}
}

func TestProviderConfirmationRequiresPaymentAndAgreement(t *testing.T) {
	booking := bookingChangeRecord{PaymentStatus: "paid_in_full"}
	if !providerConfirmationObligationsSatisfied(booking) {
		t.Fatal("a paid booking without an agreement requirement should be confirmable")
	}
	booking.AgreementRequired = true
	booking.AgreementStatus = "pending"
	if providerConfirmationObligationsSatisfied(booking) {
		t.Fatal("a required pending agreement should block confirmation")
	}
	booking.AgreementStatus = "signed"
	if !providerConfirmationObligationsSatisfied(booking) {
		t.Fatal("a signed agreement should satisfy confirmation requirements")
	}
	booking.PaymentStatus = "full_payment_pending"
	if providerConfirmationObligationsSatisfied(booking) {
		t.Fatal("an unpaid booking should block confirmation")
	}
}

func TestBookingChangeQuoteResponsePreservesCapabilityToken(t *testing.T) {
	quote := bookingChangeQuoteRecord{
		PublicToken: "quote-token", BookingID: uuid.New(), Kind: "cancellation",
		CurrentStartsAt: time.Now(), CurrentEndsAt: time.Now().Add(time.Hour),
		CurrencyCode: "NGN", RefundAmountMinor: 5000,
	}
	response := bookingChangeQuoteResponse(quote)
	if response.QuoteToken != quote.PublicToken || response.RefundAmountMinor != 5000 {
		t.Fatalf("unexpected quote response: %#v", response)
	}
}

func TestWriteBookingChangeErrorUsesConflictContracts(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{ErrBookingActionNotAllowed, "booking_action_not_allowed"},
		{ErrBookingChangeExpired, "booking_change_quote_expired"},
		{ErrBookingChangeStale, "booking_change_quote_stale"},
		{ErrAutomatedReschedule, "manual_reschedule_policy"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		writeBookingChangeError(recorder, test.err)
		if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("error %v returned status=%d body=%s", test.err, recorder.Code, recorder.Body.String())
		}
	}
}
