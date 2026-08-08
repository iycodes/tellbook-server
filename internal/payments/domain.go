package payments

import (
	"errors"
	"fmt"
	"math"
)

type PaymentPurpose string

const (
	PaymentPurposeDeposit PaymentPurpose = "deposit"
	PaymentPurposeFull    PaymentPurpose = "full"
	PaymentPurposeBalance PaymentPurpose = "balance"
)

func (p PaymentPurpose) Valid() bool {
	switch p {
	case PaymentPurposeDeposit, PaymentPurposeFull, PaymentPurposeBalance:
		return true
	default:
		return false
	}
}

type PaymentStatus string

const (
	PaymentStatusCreated           PaymentStatus = "created"
	PaymentStatusPending           PaymentStatus = "pending"
	PaymentStatusRequiresAction    PaymentStatus = "requires_action"
	PaymentStatusPaid              PaymentStatus = "paid"
	PaymentStatusPartiallyRefunded PaymentStatus = "partially_refunded"
	PaymentStatusRefunded          PaymentStatus = "refunded"
	PaymentStatusDisputed          PaymentStatus = "disputed"
	PaymentStatusReversed          PaymentStatus = "reversed"
	PaymentStatusFailed            PaymentStatus = "failed"
	PaymentStatusExpired           PaymentStatus = "expired"
	PaymentStatusCancelled         PaymentStatus = "cancelled"
)

type PayoutStatus string

const (
	PayoutStatusCreated        PayoutStatus = "created"
	PayoutStatusPending        PayoutStatus = "pending"
	PayoutStatusRequiresAction PayoutStatus = "requires_action"
	PayoutStatusSuccessful     PayoutStatus = "successful"
	PayoutStatusFailed         PayoutStatus = "failed"
	PayoutStatusReversed       PayoutStatus = "reversed"
	PayoutStatusCancelled      PayoutStatus = "cancelled"
	PayoutStatusUnknown        PayoutStatus = "unknown"
)

var ErrInvalidFinancialTransition = errors.New("invalid financial state transition")

var ErrPaymentObligationSatisfied = errors.New("booking payment obligation is already satisfied")

func ValidatePaymentTransition(from, to PaymentStatus) error {
	if from == to {
		return nil
	}
	allowed := map[PaymentStatus]map[PaymentStatus]struct{}{
		PaymentStatusCreated: {
			PaymentStatusPending: {}, PaymentStatusRequiresAction: {}, PaymentStatusFailed: {},
			PaymentStatusExpired: {}, PaymentStatusCancelled: {},
		},
		PaymentStatusPending: {
			PaymentStatusRequiresAction: {}, PaymentStatusPaid: {}, PaymentStatusFailed: {},
			PaymentStatusExpired: {}, PaymentStatusCancelled: {},
		},
		PaymentStatusRequiresAction: {
			PaymentStatusPending: {}, PaymentStatusPaid: {}, PaymentStatusFailed: {},
			PaymentStatusExpired: {}, PaymentStatusCancelled: {},
		},
		PaymentStatusPaid: {
			PaymentStatusPartiallyRefunded: {}, PaymentStatusRefunded: {},
			PaymentStatusDisputed: {}, PaymentStatusReversed: {},
		},
		PaymentStatusPartiallyRefunded: {
			PaymentStatusRefunded: {}, PaymentStatusDisputed: {}, PaymentStatusReversed: {},
		},
		PaymentStatusDisputed: {
			PaymentStatusPaid: {}, PaymentStatusPartiallyRefunded: {},
			PaymentStatusRefunded: {}, PaymentStatusReversed: {},
		},
	}
	if _, ok := allowed[from][to]; !ok {
		return fmt.Errorf("%w: payment %q -> %q", ErrInvalidFinancialTransition, from, to)
	}
	return nil
}

func ValidatePayoutTransition(from, to PayoutStatus) error {
	if from == to {
		return nil
	}
	allowed := map[PayoutStatus]map[PayoutStatus]struct{}{
		PayoutStatusCreated: {
			PayoutStatusPending: {}, PayoutStatusRequiresAction: {}, PayoutStatusFailed: {},
			PayoutStatusCancelled: {}, PayoutStatusUnknown: {}, PayoutStatusSuccessful: {},
			PayoutStatusReversed: {},
		},
		PayoutStatusPending: {
			PayoutStatusRequiresAction: {}, PayoutStatusSuccessful: {}, PayoutStatusFailed: {},
			PayoutStatusCancelled: {}, PayoutStatusUnknown: {},
		},
		PayoutStatusRequiresAction: {
			PayoutStatusPending: {}, PayoutStatusSuccessful: {}, PayoutStatusFailed: {},
			PayoutStatusCancelled: {}, PayoutStatusUnknown: {},
		},
		PayoutStatusUnknown: {
			PayoutStatusPending: {}, PayoutStatusRequiresAction: {}, PayoutStatusSuccessful: {},
			PayoutStatusFailed: {}, PayoutStatusReversed: {},
		},
		PayoutStatusSuccessful: {PayoutStatusReversed: {}},
	}
	if _, ok := allowed[from][to]; !ok {
		return fmt.Errorf("%w: payout %q -> %q", ErrInvalidFinancialTransition, from, to)
	}
	return nil
}

type BookingPaymentState string

const (
	BookingPaymentDepositPending     BookingPaymentState = "deposit_pending"
	BookingPaymentDepositPaidBalance BookingPaymentState = "deposit_paid_balance_due"
	BookingPaymentFullPending        BookingPaymentState = "full_payment_pending"
	BookingPaymentPaidInFull         BookingPaymentState = "paid_in_full"
	BookingPaymentFailed             BookingPaymentState = "payment_failed"
	BookingPaymentRefunded           BookingPaymentState = "refunded"
	BookingPaymentDisputed           BookingPaymentState = "disputed"
)

type BookingObligationSummary struct {
	TotalMinor        int64
	DepositMinor      int64
	NetPaidMinor      int64
	HasPendingAttempt bool
	HasFailedAttempt  bool
	Refunded          bool
	Disputed          bool
}

func DeriveBookingPaymentState(summary BookingObligationSummary) (BookingPaymentState, error) {
	if summary.TotalMinor <= 0 || summary.DepositMinor < 0 || summary.DepositMinor > summary.TotalMinor || summary.NetPaidMinor < 0 {
		return "", errors.New("invalid booking payment obligation amounts")
	}
	if summary.Disputed {
		return BookingPaymentDisputed, nil
	}
	if summary.Refunded && summary.NetPaidMinor == 0 {
		return BookingPaymentRefunded, nil
	}
	if summary.NetPaidMinor >= summary.TotalMinor {
		return BookingPaymentPaidInFull, nil
	}

	isDepositBooking := summary.DepositMinor > 0 && summary.DepositMinor < summary.TotalMinor
	if isDepositBooking && summary.NetPaidMinor >= summary.DepositMinor {
		return BookingPaymentDepositPaidBalance, nil
	}
	if summary.HasFailedAttempt && !summary.HasPendingAttempt {
		return BookingPaymentFailed, nil
	}
	if isDepositBooking {
		return BookingPaymentDepositPending, nil
	}
	return BookingPaymentFullPending, nil
}

type AllocationAmounts struct {
	GrossMinor             int64
	ProviderFeeMinor       int64
	PlatformFeeMinor       int64
	TaxMinor               int64
	AdjustmentMinor        int64
	BusinessNetAmountMinor int64
}

func (a AllocationAmounts) Validate() error {
	if a.GrossMinor <= 0 || a.ProviderFeeMinor < 0 || a.PlatformFeeMinor < 0 ||
		a.TaxMinor < 0 || a.AdjustmentMinor < 0 || a.BusinessNetAmountMinor < 0 {
		return errors.New("allocation amounts must be nonnegative and gross must be positive")
	}
	parts := []int64{a.ProviderFeeMinor, a.PlatformFeeMinor, a.TaxMinor, a.AdjustmentMinor, a.BusinessNetAmountMinor}
	var total int64
	for _, part := range parts {
		if part > math.MaxInt64-total {
			return errors.New("allocation amount overflow")
		}
		total += part
	}
	if total != a.GrossMinor {
		return errors.New("allocation components must equal gross amount")
	}
	return nil
}
