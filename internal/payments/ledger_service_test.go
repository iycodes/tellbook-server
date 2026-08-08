package payments

import (
	"errors"
	"testing"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
)

func validCreatePaymentAttemptInput() CreatePaymentAttemptInput {
	return CreatePaymentAttemptInput{
		BookingID: uuid.New(), ClientID: uuid.New(), CustomerID: uuid.New(),
		Purpose: PaymentPurposeDeposit, Provider: "payaza", Method: "card",
		CountryCode: "NG", CurrencyCode: "NGN", AmountMinor: money.Minor(3000),
		PriceSnapshot:  map[string]string{"total_amount_minor": "10000", "deposit_amount_minor": "3000"},
		IdempotencyKey: "payment_intent_1234567890",
	}
}

func TestPaymentRequestFingerprintIsDeterministicAndInputBound(t *testing.T) {
	t.Parallel()

	input := validCreatePaymentAttemptInput()
	snapshot := []byte(`{"deposit_amount_minor":"3000","total_amount_minor":"10000"}`)
	first, err := paymentRequestFingerprint(input, snapshot)
	if err != nil {
		t.Fatalf("paymentRequestFingerprint() error = %v", err)
	}
	second, err := paymentRequestFingerprint(input, snapshot)
	if err != nil {
		t.Fatalf("paymentRequestFingerprint() second error = %v", err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("fingerprints = %q and %q", first, second)
	}
	input.Method = "bank_transfer"
	changed, err := paymentRequestFingerprint(input, snapshot)
	if err != nil {
		t.Fatalf("paymentRequestFingerprint() changed error = %v", err)
	}
	if changed == first {
		t.Fatal("fingerprint did not change with the payment method")
	}
}

func TestValidateCreatePaymentAttemptRejectsUnsafeInput(t *testing.T) {
	t.Parallel()

	input := validCreatePaymentAttemptInput()
	input.AmountMinor = 0
	if err := validateCreatePaymentAttempt(input); err == nil {
		t.Fatal("validateCreatePaymentAttempt() accepted zero amount")
	}
	input = validCreatePaymentAttemptInput()
	input.IdempotencyKey = "short"
	if err := validateCreatePaymentAttempt(input); err == nil {
		t.Fatal("validateCreatePaymentAttempt() accepted short idempotency key")
	}
	input = validCreatePaymentAttemptInput()
	input.Purpose = "renewal"
	if err := validateCreatePaymentAttempt(input); err == nil {
		t.Fatal("validateCreatePaymentAttempt() accepted unknown purpose")
	}
}

func TestNewLedgerServiceRequiresSecurityDependenciesTogether(t *testing.T) {
	t.Parallel()

	_, err := NewLedgerService(&LedgerRepository{}, nil, nil)
	if err != nil {
		t.Fatalf("NewLedgerService() without optional security error = %v", err)
	}
	_, err = NewLedgerService(nil, nil, nil)
	if err == nil {
		t.Fatal("NewLedgerService() accepted nil repository")
	}
	if !errors.Is(ErrIdempotencyConflict, ErrIdempotencyConflict) {
		t.Fatal("idempotency sentinel is not stable")
	}
}

func TestMaskFinancialIdentifier(t *testing.T) {
	t.Parallel()

	if got := maskFinancialIdentifier("0123456789"); got != "******6789" {
		t.Fatalf("maskFinancialIdentifier() = %q", got)
	}
	if got := maskFinancialIdentifier("123"); got != "***" {
		t.Fatalf("short mask = %q", got)
	}
}
