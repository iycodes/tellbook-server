package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type BookingRefundWorker struct {
	repository *LedgerRepository
	providers  map[string]RefundProvider
	logger     *slog.Logger
}

type bookingRefundRequest struct {
	ID           uuid.UUID
	BookingID    uuid.UUID
	AmountMinor  int64
	CurrencyCode string
	Reason       string
}

type refundablePayment struct {
	ID                   uuid.UUID
	Provider             string
	TransactionReference string
	AmountMinor          int64
	AvailableMinor       int64
	CurrencyCode         string
	CurrencyExponent     uint8
}

type bookingRefundAttempt struct {
	ID                   uuid.UUID
	RequestID            uuid.UUID
	PaymentID            uuid.UUID
	Provider             string
	TransactionReference string
	AmountMinor          int64
	PaymentAmountMinor   int64
	CurrencyCode         string
	CurrencyExponent     uint8
	Reason               string
}

type refundAllocation struct {
	Payment refundablePayment
	Amount  int64
}

func NewBookingRefundWorker(repository *LedgerRepository, providers map[string]RefundProvider, logger *slog.Logger) *BookingRefundWorker {
	if logger == nil {
		logger = slog.Default()
	}
	configured := make(map[string]RefundProvider, len(providers))
	for name, provider := range providers {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && provider != nil {
			configured[name] = provider
		}
	}
	return &BookingRefundWorker{repository: repository, providers: configured, logger: logger}
}

func (worker *BookingRefundWorker) Start(ctx context.Context, wakes ...<-chan struct{}) {
	if worker == nil || worker.repository == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	wake := firstWorkerWake(wakes)
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
		worker.runOnce(ctx)
		timer.Reset(jitterDuration(25*time.Second, 0.2))
	}
}

func (worker *BookingRefundWorker) runOnce(ctx context.Context) {
	if err := worker.reconcile(ctx); err != nil {
		worker.logger.Error("reconcile booking refunds", "error", err)
	}
	for processed := 0; processed < 10; processed++ {
		request, found, err := worker.prepareNext(ctx)
		if err != nil {
			worker.logger.Error("prepare booking refund", "error", err)
			return
		}
		if !found {
			break
		}
		worker.logger.Info("prepared booking refund", "request_id", request.ID.String())
	}
	for processed := 0; processed < 25; processed++ {
		attempt, found, err := worker.claimPreparedAttempt(ctx)
		if err != nil {
			worker.logger.Error("claim booking refund attempt", "error", err)
			return
		}
		if !found {
			break
		}
		worker.dispatch(ctx, attempt)
	}
	if err := worker.reconcile(ctx); err != nil {
		worker.logger.Error("finalize booking refunds", "error", err)
	}
}

func (worker *BookingRefundWorker) prepareNext(ctx context.Context) (bookingRefundRequest, bool, error) {
	tx, err := worker.repository.db.Begin(ctx)
	if err != nil {
		return bookingRefundRequest{}, false, err
	}
	defer tx.Rollback(ctx)
	var request bookingRefundRequest
	err = tx.QueryRow(ctx, `
		SELECT id, booking_id, amount_minor, currency_code, reason
		FROM booking_refund_requests
		WHERE status='queued'
		ORDER BY created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1
	`).Scan(&request.ID, &request.BookingID, &request.AmountMinor, &request.CurrencyCode, &request.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return bookingRefundRequest{}, false, nil
	}
	if err != nil {
		return bookingRefundRequest{}, false, fmt.Errorf("lock booking refund request: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT payment.id, payment.provider,
			COALESCE(NULLIF(payment.provider_reference,''), payment.reference),
			payment.amount_minor,
			GREATEST(payment.amount_minor-COALESCE(SUM(adjustment.allocation_impact_minor)
				FILTER (WHERE adjustment.status IN ('pending','successful')),0),0),
			payment.currency_code, payment.currency_exponent
		FROM payments payment
		LEFT JOIN payment_adjustments adjustment ON adjustment.payment_id=payment.id
		WHERE payment.booking_id=$1
		  AND payment.status IN ('paid','partially_refunded','refunded')
		GROUP BY payment.id
		ORDER BY payment.paid_at DESC NULLS LAST, payment.created_at DESC, payment.id
	`, request.BookingID)
	if err != nil {
		return bookingRefundRequest{}, false, fmt.Errorf("list refundable payments: %w", err)
	}
	payments := make([]refundablePayment, 0)
	for rows.Next() {
		var payment refundablePayment
		if err := rows.Scan(
			&payment.ID, &payment.Provider, &payment.TransactionReference, &payment.AmountMinor,
			&payment.AvailableMinor, &payment.CurrencyCode, &payment.CurrencyExponent,
		); err != nil {
			rows.Close()
			return bookingRefundRequest{}, false, fmt.Errorf("scan refundable payment: %w", err)
		}
		payments = append(payments, payment)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return bookingRefundRequest{}, false, fmt.Errorf("iterate refundable payments: %w", err)
	}
	rows.Close()
	allocations, err := allocateBookingRefund(request.AmountMinor, request.CurrencyCode, payments)
	if err != nil {
		_, updateErr := tx.Exec(ctx, `
			UPDATE booking_refund_requests SET status='failed', failure_message=$2, updated_at=NOW()
			WHERE id=$1
		`, request.ID, err.Error())
		if updateErr != nil {
			return bookingRefundRequest{}, false, updateErr
		}
		return bookingRefundRequest{}, true, tx.Commit(ctx)
	}
	for _, allocation := range allocations {
		if _, err := tx.Exec(ctx, `
			INSERT INTO booking_refund_attempts (
				id, request_id, payment_id, provider, transaction_reference,
				amount_minor, payment_amount_minor, currency_code, currency_exponent
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, uuid.New(), request.ID, allocation.Payment.ID, allocation.Payment.Provider,
			allocation.Payment.TransactionReference, allocation.Amount, allocation.Payment.AmountMinor,
			allocation.Payment.CurrencyCode, allocation.Payment.CurrencyExponent); err != nil {
			return bookingRefundRequest{}, false, fmt.Errorf("create booking refund attempt: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE booking_refund_requests SET status='processing', updated_at=NOW() WHERE id=$1`, request.ID); err != nil {
		return bookingRefundRequest{}, false, fmt.Errorf("start booking refund request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return bookingRefundRequest{}, false, err
	}
	return request, true, nil
}

func allocateBookingRefund(amount int64, currency string, payments []refundablePayment) ([]refundAllocation, error) {
	if amount <= 0 {
		return nil, errors.New("refund amount must be positive")
	}
	remaining := amount
	allocations := make([]refundAllocation, 0, len(payments))
	for _, payment := range payments {
		if payment.AvailableMinor <= 0 || payment.CurrencyCode != currency {
			continue
		}
		allocated := payment.AvailableMinor
		if allocated > remaining {
			allocated = remaining
		}
		allocations = append(allocations, refundAllocation{Payment: payment, Amount: allocated})
		remaining -= allocated
		if remaining == 0 {
			return allocations, nil
		}
	}
	return nil, errors.New("refundable payment balance is lower than the approved refund")
}

func (worker *BookingRefundWorker) claimPreparedAttempt(ctx context.Context) (bookingRefundAttempt, bool, error) {
	var attempt bookingRefundAttempt
	err := worker.repository.db.QueryRow(ctx, `
		UPDATE booking_refund_attempts attempt
		SET status='initiating', updated_at=NOW()
		FROM (
			SELECT id FROM booking_refund_attempts
			WHERE status='prepared' ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1
		) candidate, booking_refund_requests request
		WHERE attempt.id=candidate.id AND request.id=attempt.request_id
		RETURNING attempt.id, attempt.request_id, attempt.payment_id, attempt.provider,
			attempt.transaction_reference, attempt.amount_minor, attempt.payment_amount_minor,
			attempt.currency_code, attempt.currency_exponent, request.reason
	`).Scan(
		&attempt.ID, &attempt.RequestID, &attempt.PaymentID, &attempt.Provider,
		&attempt.TransactionReference, &attempt.AmountMinor, &attempt.PaymentAmountMinor,
		&attempt.CurrencyCode, &attempt.CurrencyExponent, &attempt.Reason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return bookingRefundAttempt{}, false, nil
	}
	if err != nil {
		return bookingRefundAttempt{}, false, fmt.Errorf("claim prepared refund attempt: %w", err)
	}
	return attempt, true, nil
}

func (worker *BookingRefundWorker) dispatch(ctx context.Context, attempt bookingRefundAttempt) {
	provider := worker.providers[attempt.Provider]
	if provider == nil {
		worker.failAttempt(ctx, attempt, "refund provider is not configured", false)
		return
	}
	result, err := provider.InitiateRefund(ctx, RefundRequest{
		RequestID: attempt.RequestID, PaymentID: attempt.PaymentID,
		TransactionReference: attempt.TransactionReference, Provider: attempt.Provider,
		AmountMinor: money.Minor(attempt.AmountMinor), CurrencyCode: attempt.CurrencyCode,
		CurrencyExponent: attempt.CurrencyExponent, Reason: attempt.Reason,
	})
	if err != nil {
		// The provider may have accepted the request before the connection failed.
		// Never retry an ambiguous financial side effect automatically.
		worker.failAttempt(ctx, attempt, err.Error(), true)
		return
	}
	adjustmentStatus := string(result.Status)
	if _, err := worker.repository.db.Exec(ctx, `
		UPDATE booking_refund_attempts
		SET provider_reference=$2, provider_status=$3, status=$4, updated_at=NOW()
		WHERE id=$1 AND status='initiating'
	`, attempt.ID, result.ProviderReference, result.ProviderStatus, adjustmentStatus); err != nil {
		worker.logger.Error("store booking refund result", "attempt_id", attempt.ID.String(), "error", err)
		return
	}
	kind := "partial_refund"
	if attempt.AmountMinor == attempt.PaymentAmountMinor {
		kind = "refund"
	}
	if _, _, err := worker.repository.RecordPaymentAdjustment(ctx, RecordPaymentAdjustmentInput{
		PaymentID: attempt.PaymentID, Provider: attempt.Provider,
		ProviderReference: result.ProviderReference, Kind: kind, Status: adjustmentStatus,
		CurrencyCode: attempt.CurrencyCode, AmountMinor: attempt.AmountMinor,
		AllocationImpact: attempt.AmountMinor, Reason: attempt.Reason, OccurredAt: time.Now().UTC(),
	}); err != nil {
		worker.failAttempt(ctx, attempt, "record provider refund: "+err.Error(), true)
		return
	}
}

// ResolveBookingRefundProviderReference maps Paystack's refund lifecycle webhooks back to
// the numeric refund id returned by Create Refund. Paystack may omit refund_reference
// from those webhooks, so the attempt's payment, provider, and exact amount are the
// strongest identity available for a Tellbook-initiated refund.
func (r *LedgerRepository) ResolveBookingRefundProviderReference(
	ctx context.Context,
	paymentID uuid.UUID,
	provider string,
	amountMinor int64,
	fallbackReference string,
) (string, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("begin booking refund webhook correlation: %w", err)
	}
	defer tx.Rollback(ctx)
	var providerReference string
	err = tx.QueryRow(ctx, `
		SELECT attempt.provider_reference
		FROM booking_refund_attempts attempt
		INNER JOIN booking_refund_requests request ON request.id=attempt.request_id
		WHERE attempt.payment_id=$1 AND attempt.provider=$2 AND attempt.amount_minor=$3
		  AND attempt.provider_reference IS NOT NULL
		  AND attempt.status IN ('pending','successful','failed','unknown')
		  AND request.status IN ('processing','successful','failed','manual_review')
		ORDER BY
			CASE attempt.status WHEN 'pending' THEN 0 WHEN 'unknown' THEN 1 ELSE 2 END,
			attempt.created_at DESC
		LIMIT 1
	`, paymentID, strings.ToLower(strings.TrimSpace(provider)), amountMinor).Scan(&providerReference)
	if err == nil {
		return providerReference, true, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("correlate booking refund webhook: %w", err)
	}

	var attemptID uuid.UUID
	var attemptStatus string
	err = tx.QueryRow(ctx, `
		SELECT attempt.id, attempt.status
		FROM booking_refund_attempts attempt
		INNER JOIN booking_refund_requests request ON request.id=attempt.request_id
		WHERE attempt.payment_id=$1 AND attempt.provider=$2 AND attempt.amount_minor=$3
		  AND attempt.provider_reference IS NULL
		  AND attempt.status IN ('initiating','unknown')
		  AND request.status IN ('processing','manual_review')
		ORDER BY CASE attempt.status WHEN 'initiating' THEN 0 ELSE 1 END, attempt.created_at DESC
		LIMIT 1 FOR UPDATE OF attempt
	`, paymentID, strings.ToLower(strings.TrimSpace(provider)), amountMinor).Scan(&attemptID, &attemptStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, tx.Commit(ctx)
	}
	if err != nil {
		return "", false, fmt.Errorf("load unreferenced booking refund attempt: %w", err)
	}
	if attemptStatus == "initiating" {
		return "", false, errors.New("booking refund provider identity is not available yet")
	}
	if strings.TrimSpace(fallbackReference) == "" {
		return "", false, errors.New("booking refund fallback identity is empty")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE booking_refund_attempts SET provider_reference=$2, updated_at=NOW()
		WHERE id=$1 AND provider_reference IS NULL AND status='unknown'
	`, attemptID, fallbackReference); err != nil {
		return "", false, fmt.Errorf("adopt booking refund webhook identity: %w", err)
	}
	return fallbackReference, true, tx.Commit(ctx)
}

func (worker *BookingRefundWorker) failAttempt(ctx context.Context, attempt bookingRefundAttempt, message string, ambiguous bool) {
	attemptStatus, requestStatus := "failed", "failed"
	if ambiguous {
		attemptStatus, requestStatus = "unknown", "manual_review"
	}
	_, err := worker.repository.db.Exec(ctx, `
		WITH changed AS (
			UPDATE booking_refund_attempts SET status=$2, failure_message=$3, updated_at=NOW()
			WHERE id=$1 RETURNING request_id
		)
		UPDATE booking_refund_requests request
		SET status=$4, failure_message=$3, updated_at=NOW()
		FROM changed WHERE request.id=changed.request_id
	`, attempt.ID, attemptStatus, message, requestStatus)
	if err != nil {
		worker.logger.Error("fail booking refund attempt", "attempt_id", attempt.ID.String(), "error", err)
	}
}

func (worker *BookingRefundWorker) reconcile(ctx context.Context) error {
	_, err := worker.repository.db.Exec(ctx, `
		UPDATE booking_refund_attempts attempt
		SET status=adjustment.status, updated_at=NOW()
		FROM payment_adjustments adjustment
		WHERE attempt.provider=adjustment.provider
		  AND attempt.provider_reference=adjustment.provider_reference
		  AND attempt.status IN ('pending','successful','failed','unknown')
		  AND attempt.status IS DISTINCT FROM adjustment.status;

		UPDATE booking_refund_attempts
		SET status='unknown', failure_message='Refund initiation was interrupted; review provider state before retrying', updated_at=NOW()
		WHERE status='initiating' AND updated_at < NOW()-INTERVAL '2 minutes';

		UPDATE booking_refund_requests request
		SET status=CASE
			WHEN EXISTS (SELECT 1 FROM booking_refund_attempts a WHERE a.request_id=request.id AND a.status='unknown') THEN 'manual_review'
			WHEN EXISTS (SELECT 1 FROM booking_refund_attempts a WHERE a.request_id=request.id AND a.status='failed') THEN 'failed'
			WHEN EXISTS (SELECT 1 FROM booking_refund_attempts a WHERE a.request_id=request.id)
			 AND NOT EXISTS (SELECT 1 FROM booking_refund_attempts a WHERE a.request_id=request.id AND a.status<>'successful') THEN 'successful'
			ELSE 'processing'
		END,
		completed_at=CASE
			WHEN EXISTS (SELECT 1 FROM booking_refund_attempts a WHERE a.request_id=request.id)
			 AND NOT EXISTS (SELECT 1 FROM booking_refund_attempts a WHERE a.request_id=request.id AND a.status<>'successful') THEN NOW()
			ELSE completed_at
		END,
		updated_at=NOW()
		WHERE request.status IN ('processing','manual_review')
		  AND EXISTS (SELECT 1 FROM booking_refund_attempts a WHERE a.request_id=request.id);
	`)
	return err
}
