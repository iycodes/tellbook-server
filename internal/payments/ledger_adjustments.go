package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PaymentAdjustment struct {
	ID                  uuid.UUID
	PaymentID           uuid.UUID
	Provider            string
	ProviderReference   string
	Kind                string
	Status              string
	CurrencyCode        string
	AmountMinor         int64
	Reason              string
	OccurredAt          time.Time
	AllocationImpact    int64
	FundsAlreadyPaidOut bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type RecordPaymentAdjustmentInput struct {
	PaymentID         uuid.UUID
	Provider          string
	ProviderReference string
	Kind              string
	Status            string
	CurrencyCode      string
	AmountMinor       int64
	Reason            string
	OccurredAt        time.Time
	AllocationImpact  int64
}

func (r *LedgerRepository) RecordPaymentAdjustment(
	ctx context.Context,
	input RecordPaymentAdjustmentInput,
) (PaymentAdjustment, bool, error) {
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ProviderReference = strings.TrimSpace(input.ProviderReference)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Status = strings.TrimSpace(input.Status)
	input.CurrencyCode = strings.ToUpper(strings.TrimSpace(input.CurrencyCode))
	if input.PaymentID == uuid.Nil || (input.Provider != "payaza" && input.Provider != "paystack") ||
		input.ProviderReference == "" || !validAdjustmentKind(input.Kind) || !validAdjustmentStatus(input.Status) ||
		!isUpperASCII(input.CurrencyCode, 3) || input.AmountMinor <= 0 || input.AllocationImpact < 0 ||
		input.AllocationImpact > input.AmountMinor {
		return PaymentAdjustment{}, false, errors.New("invalid payment adjustment")
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now().UTC()
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PaymentAdjustment{}, false, fmt.Errorf("begin payment adjustment: %w", err)
	}
	defer tx.Rollback(ctx)
	var bookingID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT booking_id FROM payments WHERE id = $1`, input.PaymentID).Scan(&bookingID); errors.Is(err, pgx.ErrNoRows) {
		return PaymentAdjustment{}, false, ErrLedgerRecordNotFound
	} else if err != nil {
		return PaymentAdjustment{}, false, fmt.Errorf("load adjustment booking: %w", err)
	}
	if err := lockBookingFinancialStateTx(ctx, tx, bookingID); err != nil {
		return PaymentAdjustment{}, false, err
	}
	payment, err := getPaymentForUpdate(ctx, tx, input.PaymentID)
	if err != nil {
		return PaymentAdjustment{}, false, err
	}
	if payment.Provider != input.Provider || payment.CurrencyCode != input.CurrencyCode || input.AmountMinor > int64(payment.AmountMinor) {
		return PaymentAdjustment{}, false, errors.New("payment adjustment does not match payment")
	}

	fundsAlreadyPaidOut, err := paymentFundsAlreadyPaidOutTx(ctx, tx, payment.ID)
	if err != nil {
		return PaymentAdjustment{}, false, err
	}
	const insertQuery = `
		INSERT INTO payment_adjustments (
			id, payment_id, provider, provider_reference, kind, status, currency_code,
			amount_minor, reason, occurred_at, allocation_impact_minor,
			funds_already_paid_out, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW(),NOW())
		ON CONFLICT (provider, provider_reference) DO NOTHING
		RETURNING
			id, payment_id, provider, provider_reference, kind, status, currency_code,
			amount_minor, reason, occurred_at, allocation_impact_minor,
			funds_already_paid_out, created_at, updated_at
	`
	adjustment, err := scanPaymentAdjustment(tx.QueryRow(
		ctx, insertQuery, uuid.New(), input.PaymentID, input.Provider, input.ProviderReference,
		input.Kind, input.Status, input.CurrencyCode, input.AmountMinor, strings.TrimSpace(input.Reason),
		input.OccurredAt, input.AllocationImpact, fundsAlreadyPaidOut,
	))
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		adjustment, err = getPaymentAdjustmentByProviderReferenceTx(ctx, tx, input.Provider, input.ProviderReference)
	}
	if err != nil {
		return PaymentAdjustment{}, false, fmt.Errorf("record payment adjustment: %w", err)
	}
	if !created && (adjustment.PaymentID != input.PaymentID || adjustment.Kind != input.Kind ||
		adjustment.AmountMinor != input.AmountMinor ||
		adjustment.CurrencyCode != input.CurrencyCode || adjustment.AllocationImpact != input.AllocationImpact) {
		return PaymentAdjustment{}, false, ErrIdempotencyConflict
	}

	previousStatus := adjustment.Status
	if !created && adjustment.Status != input.Status {
		if err := validateAdjustmentTransition(adjustment.Status, input.Status); err != nil {
			return PaymentAdjustment{}, false, err
		}
		const updateQuery = `
			UPDATE payment_adjustments
			SET
				status = $2,
				reason = CASE WHEN $3 <> '' THEN $3 ELSE reason END,
				occurred_at = $4,
				updated_at = NOW()
			WHERE id = $1
			RETURNING
				id, payment_id, provider, provider_reference, kind, status, currency_code,
				amount_minor, reason, occurred_at, allocation_impact_minor,
				funds_already_paid_out, created_at, updated_at
		`
		adjustment, err = scanPaymentAdjustment(tx.QueryRow(
			ctx, updateQuery, adjustment.ID, input.Status, strings.TrimSpace(input.Reason), input.OccurredAt,
		))
		if err != nil {
			return PaymentAdjustment{}, false, fmt.Errorf("transition payment adjustment: %w", err)
		}
	}

	becameSuccessful := input.Status == "successful" && (created || previousStatus != "successful")
	if becameSuccessful {
		if err := applySuccessfulAdjustmentTx(ctx, tx, payment, adjustment); err != nil {
			return PaymentAdjustment{}, false, err
		}
	}
	if !created && previousStatus == "successful" && input.Status == "reversed" {
		if err := reverseSuccessfulAdjustmentTx(ctx, tx, payment, adjustment); err != nil {
			return PaymentAdjustment{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PaymentAdjustment{}, false, fmt.Errorf("commit payment adjustment: %w", err)
	}
	return adjustment, created, nil
}

func validAdjustmentKind(value string) bool {
	switch value {
	case "partial_refund", "refund", "reversal", "dispute", "chargeback":
		return true
	default:
		return false
	}
}

func validAdjustmentStatus(value string) bool {
	switch value {
	case "pending", "successful", "failed", "reversed":
		return true
	default:
		return false
	}
}

func validateAdjustmentTransition(from, to string) error {
	if from == to {
		return nil
	}
	allowed := map[string]map[string]struct{}{
		"pending": {
			"successful": {},
			"failed":     {},
			"reversed":   {},
		},
		"successful": {
			"reversed": {},
		},
	}
	if _, ok := allowed[from][to]; !ok {
		return fmt.Errorf("%w: adjustment %q -> %q", ErrInvalidFinancialTransition, from, to)
	}
	return nil
}

func scanPaymentAdjustment(row rowScanner) (PaymentAdjustment, error) {
	var adjustment PaymentAdjustment
	if err := row.Scan(
		&adjustment.ID, &adjustment.PaymentID, &adjustment.Provider,
		&adjustment.ProviderReference, &adjustment.Kind, &adjustment.Status,
		&adjustment.CurrencyCode, &adjustment.AmountMinor, &adjustment.Reason,
		&adjustment.OccurredAt, &adjustment.AllocationImpact,
		&adjustment.FundsAlreadyPaidOut, &adjustment.CreatedAt, &adjustment.UpdatedAt,
	); err != nil {
		return PaymentAdjustment{}, err
	}
	return adjustment, nil
}

func getPaymentAdjustmentByProviderReferenceTx(
	ctx context.Context,
	tx pgx.Tx,
	provider string,
	reference string,
) (PaymentAdjustment, error) {
	const query = `
		SELECT
			id, payment_id, provider, provider_reference, kind, status, currency_code,
			amount_minor, reason, occurred_at, allocation_impact_minor,
			funds_already_paid_out, created_at, updated_at
		FROM payment_adjustments
		WHERE provider = $1 AND provider_reference = $2
	`
	return scanPaymentAdjustment(tx.QueryRow(ctx, query, provider, reference))
}

func paymentFundsAlreadyPaidOutTx(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) (bool, error) {
	const query = `
		SELECT EXISTS (
			SELECT 1
			FROM payment_allocations allocation
			JOIN payouts payout ON payout.payment_allocation_id = allocation.id
			WHERE allocation.payment_id = $1 AND payout.status = 'successful'
		)
	`
	var paidOut bool
	if err := tx.QueryRow(ctx, query, paymentID).Scan(&paidOut); err != nil {
		return false, fmt.Errorf("check adjustment payout state: %w", err)
	}
	return paidOut, nil
}

func applySuccessfulAdjustmentTx(
	ctx context.Context,
	tx pgx.Tx,
	payment FinancialPayment,
	adjustment PaymentAdjustment,
) error {
	const totalQuery = `
		SELECT COALESCE(SUM(allocation_impact_minor), 0)
		FROM payment_adjustments
		WHERE payment_id = $1 AND status = 'successful'
	`
	var totalImpact int64
	if err := tx.QueryRow(ctx, totalQuery, payment.ID).Scan(&totalImpact); err != nil {
		return fmt.Errorf("sum payment adjustment impact: %w", err)
	}
	if totalImpact > int64(payment.AmountMinor) {
		return errors.New("payment adjustments exceed the paid amount")
	}

	targetStatus, err := deriveAdjustedPaymentStatusTx(ctx, tx, payment)
	if err != nil {
		return err
	}
	if err := ValidatePaymentTransition(payment.Status, targetStatus); err != nil {
		return err
	}
	if targetStatus != payment.Status {
		if _, err := tx.Exec(ctx, `
			UPDATE payments
			SET status = $2, version = version + 1, updated_at = NOW()
			WHERE id = $1
		`, payment.ID, string(targetStatus)); err != nil {
			return fmt.Errorf("apply payment adjustment status: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE payment_allocations
		SET status = 'blocked', updated_at = NOW()
		WHERE payment_id = $1 AND status IN ('pending', 'eligible', 'reserved')
	`, payment.ID); err != nil {
		return fmt.Errorf("block adjusted payment allocation: %w", err)
	}
	if adjustment.FundsAlreadyPaidOut && adjustment.AllocationImpact > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO business_balance_entries (
				id, client_id, payment_adjustment_id, currency_code,
				amount_minor, kind, status, description, created_at
			)
			VALUES ($1,$2,$3,$4,$5,'debt','open',$6,NOW())
			ON CONFLICT (payment_adjustment_id) WHERE payment_adjustment_id IS NOT NULL DO NOTHING
		`, uuid.New(), payment.ClientID, adjustment.ID, payment.CurrencyCode,
			-adjustment.AllocationImpact, "Post-payout payment adjustment"); err != nil {
			return fmt.Errorf("record post-payout adjustment debt: %w", err)
		}
	}
	if err := recomputeBookingPaymentStateTx(ctx, tx, payment.BookingID); err != nil {
		return err
	}
	return nil
}

func reverseSuccessfulAdjustmentTx(
	ctx context.Context,
	tx pgx.Tx,
	payment FinancialPayment,
	adjustment PaymentAdjustment,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE business_balance_entries
		SET status = 'void', resolved_at = NOW()
		WHERE payment_adjustment_id = $1 AND status IN ('open', 'partially_resolved')
	`, adjustment.ID); err != nil {
		return fmt.Errorf("void reversed adjustment debt: %w", err)
	}

	targetStatus, err := deriveAdjustedPaymentStatusTx(ctx, tx, payment)
	if err != nil {
		return err
	}
	if targetStatus != payment.Status {
		if _, err := tx.Exec(ctx, `
			UPDATE payments
			SET status = $2, version = version + 1, updated_at = NOW()
			WHERE id = $1
		`, payment.ID, string(targetStatus)); err != nil {
			return fmt.Errorf("restore payment after adjustment reversal: %w", err)
		}
	}
	var allocationID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM payment_allocations WHERE payment_id = $1`, payment.ID).Scan(&allocationID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find reversed adjustment allocation: %w", err)
	}
	if err == nil {
		if err := enqueueFinancialJobTx(ctx, tx, FinancialJobParams{
			ID:               uuid.New(),
			Kind:             "reevaluate_payment_allocation",
			AggregateType:    "payment_allocation",
			AggregateID:      allocationID,
			DeduplicationKey: "reevaluate_payment_allocation_after_adjustment:" + adjustment.ID.String(),
			Payload:          []byte(`{}`),
		}); err != nil {
			return err
		}
	}
	return recomputeBookingPaymentStateTx(ctx, tx, payment.BookingID)
}

func deriveAdjustedPaymentStatusTx(
	ctx context.Context,
	tx pgx.Tx,
	payment FinancialPayment,
) (PaymentStatus, error) {
	const query = `
		SELECT
			COALESCE(SUM(allocation_impact_minor), 0),
			COALESCE(BOOL_OR(kind IN ('dispute', 'chargeback')), FALSE),
			COALESCE(BOOL_OR(kind = 'reversal'), FALSE)
		FROM payment_adjustments
		WHERE payment_id = $1 AND status = 'successful'
	`
	var totalImpact int64
	var disputed, reversed bool
	if err := tx.QueryRow(ctx, query, payment.ID).Scan(&totalImpact, &disputed, &reversed); err != nil {
		return "", fmt.Errorf("derive adjusted payment status: %w", err)
	}
	switch {
	case disputed:
		return PaymentStatusDisputed, nil
	case reversed:
		return PaymentStatusReversed, nil
	case totalImpact >= int64(payment.AmountMinor):
		return PaymentStatusRefunded, nil
	case totalImpact > 0:
		return PaymentStatusPartiallyRefunded, nil
	default:
		return PaymentStatusPaid, nil
	}
}
