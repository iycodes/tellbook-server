package payments

import (
	"testing"

	"github.com/google/uuid"
)

func TestAllocateBookingRefundAcrossPayments(t *testing.T) {
	payments := []refundablePayment{
		{ID: uuid.New(), CurrencyCode: "NGN", AmountMinor: 5000, AvailableMinor: 3000},
		{ID: uuid.New(), CurrencyCode: "NGN", AmountMinor: 4000, AvailableMinor: 4000},
	}
	allocations, err := allocateBookingRefund(6000, "NGN", payments)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 2 || allocations[0].Amount != 3000 || allocations[1].Amount != 3000 {
		t.Fatalf("unexpected allocations: %#v", allocations)
	}
}

func TestAllocateBookingRefundRejectsInsufficientBalance(t *testing.T) {
	_, err := allocateBookingRefund(6000, "NGN", []refundablePayment{{
		ID: uuid.New(), CurrencyCode: "NGN", AmountMinor: 5000, AvailableMinor: 5000,
	}})
	if err == nil {
		t.Fatal("expected insufficient refundable balance error")
	}
}
