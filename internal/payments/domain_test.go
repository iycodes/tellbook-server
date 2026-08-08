package payments

import (
	"errors"
	"math"
	"testing"
)

func TestValidatePaymentTransition(t *testing.T) {
	t.Parallel()

	if err := ValidatePaymentTransition(PaymentStatusPending, PaymentStatusPaid); err != nil {
		t.Fatalf("valid transition error = %v", err)
	}
	if err := ValidatePaymentTransition(PaymentStatusPaid, PaymentStatusPending); !errors.Is(err, ErrInvalidFinancialTransition) {
		t.Fatalf("invalid transition error = %v", err)
	}
	if err := ValidatePaymentTransition(PaymentStatusPaid, PaymentStatusPaid); err != nil {
		t.Fatalf("idempotent transition error = %v", err)
	}
}

func TestValidatePayoutTransitionPreservesUnknownOutcome(t *testing.T) {
	t.Parallel()

	if err := ValidatePayoutTransition(PayoutStatusUnknown, PayoutStatusSuccessful); err != nil {
		t.Fatalf("reconciled transition error = %v", err)
	}
	if err := ValidatePayoutTransition(PayoutStatusSuccessful, PayoutStatusPending); !errors.Is(err, ErrInvalidFinancialTransition) {
		t.Fatalf("successful payout regression error = %v", err)
	}
}

func TestDeriveBookingPaymentState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary BookingObligationSummary
		want    BookingPaymentState
	}{
		{name: "deposit pending", summary: BookingObligationSummary{TotalMinor: 10000, DepositMinor: 3000}, want: BookingPaymentDepositPending},
		{name: "deposit paid", summary: BookingObligationSummary{TotalMinor: 10000, DepositMinor: 3000, NetPaidMinor: 3000}, want: BookingPaymentDepositPaidBalance},
		{name: "full payment", summary: BookingObligationSummary{TotalMinor: 10000, DepositMinor: 10000, NetPaidMinor: 10000}, want: BookingPaymentPaidInFull},
		{name: "failed attempt", summary: BookingObligationSummary{TotalMinor: 10000, DepositMinor: 10000, HasFailedAttempt: true}, want: BookingPaymentFailed},
		{name: "dispute wins precedence", summary: BookingObligationSummary{TotalMinor: 10000, NetPaidMinor: 10000, Disputed: true}, want: BookingPaymentDisputed},
	}
	for _, tt := range tests {
		testCase := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := DeriveBookingPaymentState(testCase.summary)
			if err != nil {
				t.Fatalf("DeriveBookingPaymentState() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("state = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestAllocationAmountsValidate(t *testing.T) {
	t.Parallel()

	valid := AllocationAmounts{GrossMinor: 10000, ProviderFeeMinor: 150, PlatformFeeMinor: 500, TaxMinor: 50, BusinessNetAmountMinor: 9300}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	invalid := valid
	invalid.BusinessNetAmountMinor++
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() accepted an unbalanced allocation")
	}
	overflow := AllocationAmounts{GrossMinor: math.MaxInt64, ProviderFeeMinor: math.MaxInt64, PlatformFeeMinor: 1}
	if err := overflow.Validate(); err == nil {
		t.Fatal("Validate() accepted an overflowing allocation")
	}
}
