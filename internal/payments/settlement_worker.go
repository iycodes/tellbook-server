package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const settlementSyncOverlap = 48 * time.Hour

type SettlementWorker struct {
	repository *LedgerRepository
	providers  map[string]SettlementProvider
	logger     *slog.Logger
	workerID   string
}

func NewSettlementWorker(repository *LedgerRepository, providers map[string]SettlementProvider, logger *slog.Logger) *SettlementWorker {
	if logger == nil {
		logger = slog.Default()
	}
	cleaned := make(map[string]SettlementProvider, len(providers))
	for name, provider := range providers {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && provider != nil {
			cleaned[name] = provider
		}
	}
	return &SettlementWorker{
		repository: repository, providers: cleaned, logger: logger,
		workerID: "settlement-sync-" + uuid.NewString(),
	}
}

func (w *SettlementWorker) Start(ctx context.Context) {
	if w == nil || w.repository == nil || len(w.providers) == 0 {
		return
	}
	w.runOnce(ctx)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *SettlementWorker) runOnce(ctx context.Context) {
	providerNames := make([]string, 0, len(w.providers))
	for name := range w.providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, providerName := range providerNames {
		w.syncProvider(ctx, providerName, w.providers[providerName])
	}
}

func (w *SettlementWorker) syncProvider(ctx context.Context, providerName string, provider SettlementProvider) {
	cursor, claimed, err := w.repository.ClaimSettlementSync(ctx, providerName, w.workerID, 4*time.Minute)
	if err != nil {
		w.logger.Error("claim settlement sync failed", "provider", providerName, "error", err)
		return
	}
	if !claimed {
		return
	}

	until := time.Now().UTC()
	from := cursor.Add(-settlementSyncOverlap)
	evidence, err := provider.ListSettlementEvidence(ctx, SettlementQuery{From: from, To: until})
	if err == nil {
		for _, item := range evidence {
			if _, recordErr := w.repository.RecordSettlementEvidence(ctx, item); recordErr != nil {
				err = recordErr
				break
			}
		}
	}
	if err == nil {
		err = w.repository.ReapplyUnmatchedSettlementEvidence(ctx, providerName, 200)
	}
	if err != nil {
		if failErr := w.repository.FailSettlementSync(ctx, providerName, w.workerID, time.Now().UTC().Add(time.Minute), err); failErr != nil {
			w.logger.Error("delay settlement sync failed", "provider", providerName, "error", failErr)
		}
		w.logger.Warn("settlement sync failed", "provider", providerName, "error", err)
		return
	}
	if err := w.repository.CompleteSettlementSync(ctx, providerName, w.workerID, until); err != nil {
		w.logger.Error("complete settlement sync failed", "provider", providerName, "error", err)
	}
}

func (r *LedgerRepository) ClaimSettlementSync(
	ctx context.Context,
	provider string,
	workerID string,
	leaseDuration time.Duration,
) (time.Time, bool, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	workerID = strings.TrimSpace(workerID)
	if r == nil || r.db == nil || (provider != "payaza" && provider != "paystack") || workerID == "" || leaseDuration <= 0 {
		return time.Time{}, false, errors.New("invalid settlement sync claim")
	}
	leaseSeconds := max(int64(leaseDuration/time.Second), 1)
	if _, err := r.db.Exec(ctx, `
		INSERT INTO provider_settlement_sync_states (provider, cursor_at)
		SELECT $1, COALESCE(
			MIN(payment.paid_at),
			NOW() - INTERVAL '30 days'
		)
		FROM payments payment
		LEFT JOIN payment_allocations allocation ON allocation.payment_id = payment.id
		WHERE payment.provider = $1
		  AND payment.status IN ('paid', 'partially_refunded')
		  AND (allocation.id IS NULL OR allocation.settlement_status = 'pending')
		ON CONFLICT (provider) DO NOTHING
	`, provider); err != nil {
		return time.Time{}, false, fmt.Errorf("initialize settlement sync: %w", err)
	}
	if _, err := r.db.Exec(ctx, `
		UPDATE provider_settlement_sync_states state
		SET cursor_at = LEAST(state.cursor_at, pending.earliest_paid_at), updated_at = NOW()
		FROM (
			SELECT MIN(payment.paid_at) AS earliest_paid_at
			FROM payments payment
			LEFT JOIN payment_allocations allocation ON allocation.payment_id = payment.id
			WHERE payment.provider = $1
			  AND payment.status IN ('paid', 'partially_refunded')
			  AND (allocation.id IS NULL OR allocation.settlement_status = 'pending')
		) pending
		WHERE state.provider = $1 AND pending.earliest_paid_at IS NOT NULL
	`, provider); err != nil {
		return time.Time{}, false, fmt.Errorf("rewind settlement sync cursor: %w", err)
	}
	var cursor time.Time
	err := r.db.QueryRow(ctx, `
		UPDATE provider_settlement_sync_states
		SET lease_owner = $2,
			lease_expires_at = NOW() + ($3 * INTERVAL '1 second'),
			last_error = '',
			updated_at = NOW()
		WHERE provider = $1
		  AND (lease_expires_at IS NULL OR lease_expires_at <= NOW())
		RETURNING cursor_at
	`, provider, workerID, leaseSeconds).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("claim settlement sync: %w", err)
	}
	return cursor.UTC(), true, nil
}

func (r *LedgerRepository) CompleteSettlementSync(ctx context.Context, provider, workerID string, cursor time.Time) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE provider_settlement_sync_states
		SET cursor_at = GREATEST(cursor_at, $3), last_success_at = NOW(),
			lease_owner = '', lease_expires_at = NULL, last_error = '', updated_at = NOW()
		WHERE provider = $1 AND lease_owner = $2
	`, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(workerID), cursor.UTC())
	if err != nil {
		return fmt.Errorf("complete settlement sync: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (r *LedgerRepository) FailSettlementSync(ctx context.Context, provider, workerID string, retryAt time.Time, cause error) error {
	message := "settlement provider request failed"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 500 {
		message = message[:500]
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE provider_settlement_sync_states
		SET lease_expires_at = $3, last_error = $4, updated_at = NOW()
		WHERE provider = $1 AND lease_owner = $2
	`, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(workerID), retryAt.UTC(), message)
	if err != nil {
		return fmt.Errorf("fail settlement sync: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (r *LedgerRepository) RecordSettlementEvidence(ctx context.Context, evidence SettlementEvidence) (bool, error) {
	evidence.Provider = strings.ToLower(strings.TrimSpace(evidence.Provider))
	evidence.SettlementReference = strings.TrimSpace(evidence.SettlementReference)
	evidence.PaymentReference = strings.TrimSpace(evidence.PaymentReference)
	evidence.ProviderStatus = strings.TrimSpace(evidence.ProviderStatus)
	evidence.SettlementStatus = strings.ToLower(strings.TrimSpace(evidence.SettlementStatus))
	evidence.CurrencyCode = strings.ToUpper(strings.TrimSpace(evidence.CurrencyCode))
	if (evidence.Provider != "payaza" && evidence.Provider != "paystack") ||
		evidence.SettlementReference == "" || evidence.PaymentReference == "" ||
		evidence.SettlementStatus != "available" || evidence.AmountMinor <= 0 ||
		!isUpperASCII(evidence.CurrencyCode, 3) || evidence.AvailableAt.IsZero() {
		return false, errors.New("invalid settlement evidence")
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin settlement evidence: %w", err)
	}
	defer tx.Rollback(ctx)

	status := "unmatched"
	var paymentID *uuid.UUID
	var payment FinancialPayment
	payment, err = scanFinancialPayment(tx.QueryRow(ctx, `
		SELECT id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
			provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments WHERE provider = $1 AND reference = $2 FOR UPDATE
	`, evidence.Provider, evidence.PaymentReference))
	if err == nil {
		paymentID = &payment.ID
		if moneyMatchesSettlement(payment, evidence) {
			allocation, allocationErr := getPaymentAllocationByPaymentTx(ctx, tx, payment.ID)
			switch {
			case allocationErr == nil && allocation.CurrencyCode == evidence.CurrencyCode && allocation.Amounts.GrossMinor == int64(evidence.AmountMinor) &&
				(allocation.SettlementReference == "" || allocation.SettlementReference == evidence.SettlementReference):
				status = "available"
				targetStatus := allocation.Status
				if allocation.Status == "pending" && allocation.Amounts.BusinessNetAmountMinor > 0 &&
					(payment.Status == PaymentStatusPaid || payment.Status == PaymentStatusPartiallyRefunded) {
					targetStatus = "eligible"
				}
				if _, err := tx.Exec(ctx, `
					UPDATE payment_allocations
					SET settlement_status = 'available', settlement_reference = $2,
						available_for_payout_at = $3, status = $4,
						calculation_snapshot = calculation_snapshot || jsonb_build_object(
							'settlement_evidence', 'provider_settlement',
							'settlement_provider', $5::text,
							'settlement_reference', $2::text
						),
						updated_at = NOW()
					WHERE id = $1
				`, allocation.ID, evidence.SettlementReference, evidence.AvailableAt.UTC(), targetStatus, evidence.Provider); err != nil {
					return false, fmt.Errorf("apply settlement evidence: %w", err)
				}
			case allocationErr == nil:
				status = "mismatched"
			case errors.Is(allocationErr, pgx.ErrNoRows):
				status = "unmatched"
			default:
				return false, allocationErr
			}
		} else {
			status = "mismatched"
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("match settlement payment: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_settlement_evidence (
			id, provider, settlement_reference, payment_reference, payment_id,
			provider_status, amount_minor, currency_code, status, available_at,
			observed_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW(),NOW())
		ON CONFLICT (provider, settlement_reference, payment_reference) DO UPDATE
		SET payment_id = EXCLUDED.payment_id,
			provider_status = EXCLUDED.provider_status,
			amount_minor = EXCLUDED.amount_minor,
			currency_code = EXCLUDED.currency_code,
			status = EXCLUDED.status,
			available_at = EXCLUDED.available_at,
			updated_at = NOW()
	`, uuid.New(), evidence.Provider, evidence.SettlementReference, evidence.PaymentReference,
		paymentID, evidence.ProviderStatus, int64(evidence.AmountMinor), evidence.CurrencyCode,
		status, evidence.AvailableAt.UTC()); err != nil {
		return false, fmt.Errorf("store settlement evidence: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit settlement evidence: %w", err)
	}
	return status == "available", nil
}

func moneyMatchesSettlement(payment FinancialPayment, evidence SettlementEvidence) bool {
	return payment.AmountMinor == evidence.AmountMinor &&
		payment.CurrencyCode == evidence.CurrencyCode && payment.Reference == evidence.PaymentReference
}

func (r *LedgerRepository) ReapplyUnmatchedSettlementEvidence(ctx context.Context, provider string, limit int) error {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.Query(ctx, `
		SELECT provider, settlement_reference, payment_reference, provider_status,
			amount_minor, currency_code, available_at
		FROM provider_settlement_evidence
		WHERE provider = $1 AND status = 'unmatched'
		ORDER BY observed_at
		LIMIT $2
	`, strings.ToLower(strings.TrimSpace(provider)), limit)
	if err != nil {
		return fmt.Errorf("list unmatched settlement evidence: %w", err)
	}
	items := make([]SettlementEvidence, 0, limit)
	for rows.Next() {
		var item SettlementEvidence
		if err := rows.Scan(&item.Provider, &item.SettlementReference, &item.PaymentReference,
			&item.ProviderStatus, &item.AmountMinor, &item.CurrencyCode, &item.AvailableAt); err != nil {
			rows.Close()
			return err
		}
		item.SettlementStatus = "available"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if _, err := r.RecordSettlementEvidence(ctx, item); err != nil {
			return err
		}
	}
	return nil
}
