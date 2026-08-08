package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments/capabilities"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const allocationPolicyNoPlatformFeeV1 = "provider_fee_only_v1"

type AllocationWorker struct {
	repository   *LedgerRepository
	capabilities *capabilities.Registry
	environment  capabilities.Environment
	logger       *slog.Logger
	workerID     string
}

func NewAllocationWorker(repository *LedgerRepository, registry *capabilities.Registry, environment capabilities.Environment, logger *slog.Logger) *AllocationWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &AllocationWorker{
		repository: repository, capabilities: registry, environment: environment,
		logger: logger, workerID: "payment-allocation-" + uuid.NewString(),
	}
}

func (w *AllocationWorker) Start(ctx context.Context) {
	if w == nil || w.repository == nil || w.capabilities == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
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

func (w *AllocationWorker) runOnce(ctx context.Context) {
	w.processJobs(ctx, "create_payment_allocation", w.process)
	w.processJobs(ctx, "reevaluate_payment_allocation", w.processReevaluation)
}

func (w *AllocationWorker) processJobs(ctx context.Context, kind string, process func(context.Context, FinancialJob) error) {
	jobs, err := w.repository.ClaimFinancialJobsByKind(ctx, w.workerID, kind, 20, 30*time.Second)
	if err != nil {
		w.logger.Error("claim payment allocation jobs failed", "kind", kind, "error", err)
		return
	}
	for _, job := range jobs {
		if err := process(ctx, job); err != nil {
			retryAt := time.Now().UTC().Add(webhookRetryDelay(job.Attempts))
			if failErr := w.repository.FailFinancialJob(ctx, job.ID, w.workerID, retryAt, err.Error()); failErr != nil {
				w.logger.Error("fail payment allocation job", "job_id", job.ID.String(), "error", failErr)
			}
			continue
		}
		if err := w.repository.CompleteFinancialJob(ctx, job.ID, w.workerID); err != nil {
			w.logger.Error("complete payment allocation job", "job_id", job.ID.String(), "error", err)
		}
	}
}

func (w *AllocationWorker) processReevaluation(ctx context.Context, job FinancialJob) error {
	return w.repository.ReevaluatePaymentAllocation(ctx, job.AggregateID)
}

func (w *AllocationWorker) process(ctx context.Context, job FinancialJob) error {
	payment, err := w.repository.GetPaymentByID(ctx, job.AggregateID)
	if err != nil {
		return err
	}
	if payment.Status != PaymentStatusPaid {
		return errors.New("payment is not paid")
	}
	capability, err := w.capabilities.LookupProviderSupport(capabilities.Query{
		Operation: capabilities.OperationCollection, CountryCode: payment.CountryCode,
		CurrencyCode: payment.CurrencyCode, Rail: payment.Method, Environment: w.environment,
	}, capabilities.Provider(payment.Provider))
	if err != nil {
		return err
	}
	evidence, eventID, err := w.repository.GetCollectionFeeEvidence(ctx, payment)
	if err != nil {
		return err
	}
	feeMinor, err := verifiedCollectionFee(payment.Provider, evidence, capability.CurrencyExponent)
	if err != nil {
		return err
	}
	if feeMinor < 0 || feeMinor > int64(payment.AmountMinor) {
		return errors.New("provider collection fee is outside the payment amount")
	}
	_, _, err = w.repository.CreatePaymentAllocation(ctx, CreatePaymentAllocationInput{
		PaymentID: payment.ID, ClientID: payment.ClientID, CurrencyCode: payment.CurrencyCode,
		Amounts: AllocationAmounts{
			GrossMinor: int64(payment.AmountMinor), ProviderFeeMinor: feeMinor,
			BusinessNetAmountMinor: int64(payment.AmountMinor) - feeMinor,
		},
		PolicyVersion: allocationPolicyNoPlatformFeeV1,
		CalculationSnapshot: map[string]string{
			"provider_fee_source": "verified_webhook", "provider_webhook_event_id": eventID.String(),
			"platform_fee_policy": "zero", "settlement_evidence": "pending",
		},
		SettlementStatus: "pending",
	})
	return err
}

func verifiedCollectionFee(provider string, evidence json.RawMessage, exponent uint8) (int64, error) {
	var values map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(evidence)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return 0, err
	}
	key := "fees"
	if provider == "payaza" {
		key = "transaction_fee"
	}
	value, ok := values[key]
	if !ok {
		return 0, fmt.Errorf("verified %s webhook does not contain provider fee", provider)
	}
	number := ""
	switch typed := value.(type) {
	case json.Number:
		number = typed.String()
	case string:
		number = strings.TrimSpace(typed)
	default:
		return 0, errors.New("provider fee has an invalid type")
	}
	if provider == "paystack" {
		if !isCanonicalNonNegativeInteger(number) {
			return 0, errors.New("Paystack fee is not canonical minor units")
		}
		fee, err := strconv.ParseInt(number, 10, 64)
		if err != nil {
			return 0, errors.New("Paystack fee is not canonical minor units")
		}
		return fee, nil
	}
	if provider == "payaza" {
		return money.ParseDecimal(number, exponent)
	}
	return 0, errors.New("unsupported collection fee provider")
}

func isCanonicalNonNegativeInteger(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func (r *LedgerRepository) ReevaluatePaymentAllocation(ctx context.Context, allocationID uuid.UUID) error {
	if r == nil || r.db == nil || allocationID == uuid.Nil {
		return errors.New("invalid payment allocation reevaluation")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin payment allocation reevaluation: %w", err)
	}
	defer tx.Rollback(ctx)

	allocation, err := scanPaymentAllocation(tx.QueryRow(ctx, `
		SELECT id, payment_id, client_id, currency_code, gross_amount_minor,
			provider_collection_fee_minor, platform_fee_minor, tax_amount_minor,
			adjustment_amount_minor, business_net_amount_minor, policy_version,
			calculation_snapshot, settlement_status, settlement_reference,
			available_for_payout_at, status, created_at, updated_at
		FROM payment_allocations WHERE id = $1 FOR UPDATE
	`, allocationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLedgerRecordNotFound
	}
	if err != nil {
		return fmt.Errorf("lock payment allocation for reevaluation: %w", err)
	}
	payment, err := getPaymentForUpdate(ctx, tx, allocation.PaymentID)
	if err != nil {
		return err
	}

	var adjustmentImpact int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(allocation_impact_minor), 0)
		FROM payment_adjustments
		WHERE payment_id = $1 AND status = 'successful'
	`, payment.ID).Scan(&adjustmentImpact); err != nil {
		return fmt.Errorf("sum allocation adjustment impact: %w", err)
	}
	baseNet := allocation.Amounts.GrossMinor - allocation.Amounts.ProviderFeeMinor -
		allocation.Amounts.PlatformFeeMinor - allocation.Amounts.TaxMinor
	if baseNet < 0 {
		return errors.New("payment allocation fees exceed gross amount")
	}
	if adjustmentImpact > baseNet {
		adjustmentImpact = baseNet
	}
	businessNet := baseNet - adjustmentImpact

	var hasSuccessfulPayout, hasActivePayout bool
	if err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(BOOL_OR(status = 'successful'), FALSE),
			COALESCE(BOOL_OR(status IN ('created','pending','requires_action','unknown')), FALSE)
		FROM payouts WHERE payment_allocation_id = $1
	`, allocation.ID).Scan(&hasSuccessfulPayout, &hasActivePayout); err != nil {
		return fmt.Errorf("derive payment allocation payout state: %w", err)
	}

	targetStatus := "blocked"
	switch {
	case hasSuccessfulPayout:
		targetStatus = "paid"
	case hasActivePayout:
		targetStatus = "reserved"
	case businessNet <= 0:
		targetStatus = "blocked"
	case payment.Status != PaymentStatusPaid && payment.Status != PaymentStatusPartiallyRefunded:
		targetStatus = "blocked"
	case allocation.SettlementStatus == "available" && allocation.AvailableForPayoutAt != nil && !allocation.AvailableForPayoutAt.After(time.Now().UTC()):
		targetStatus = "eligible"
	case allocation.SettlementStatus == "pending":
		targetStatus = "pending"
	}

	if _, err := tx.Exec(ctx, `
		UPDATE payment_allocations
		SET adjustment_amount_minor = $2,
			business_net_amount_minor = $3,
			status = $4,
			updated_at = NOW()
		WHERE id = $1
	`, allocation.ID, adjustmentImpact, businessNet, targetStatus); err != nil {
		return fmt.Errorf("reevaluate payment allocation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit payment allocation reevaluation: %w", err)
	}
	return nil
}

func (r *LedgerRepository) GetPaymentByID(ctx context.Context, paymentID uuid.UUID) (FinancialPayment, error) {
	const query = `
		SELECT id, public_token, booking_id, client_id, customer_id, purpose, provider, method,
			country_code, currency_code, amount_minor, price_snapshot, reference,
			provider_reference, provider_channel, idempotency_key, request_fingerprint, status, provider_status,
			reconciliation_reason, failure_code, failure_message, checkout_url, expires_at,
			paid_at, last_reconciled_at, version, created_at, updated_at
		FROM payments WHERE id = $1
	`
	payment, err := scanFinancialPayment(r.db.QueryRow(ctx, query, paymentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FinancialPayment{}, ErrLedgerRecordNotFound
	}
	return payment, err
}

func (r *LedgerRepository) GetCollectionFeeEvidence(ctx context.Context, payment FinancialPayment) (json.RawMessage, uuid.UUID, error) {
	const query = `
		SELECT id, normalized_event
		FROM provider_webhook_events
		WHERE provider = $1
		  AND processing_status = 'completed'
		  AND (
			BTRIM(COALESCE(normalized_event->>'merchant_reference', '')) = $2
			OR BTRIM(COALESCE(normalized_event->>'reference', '')) = $2
			OR BTRIM(COALESCE(normalized_event->>'transaction_reference', '')) = $2
		  )
		ORDER BY received_at DESC LIMIT 1
	`
	var eventID uuid.UUID
	var evidence json.RawMessage
	if err := r.db.QueryRow(ctx, query, payment.Provider, payment.Reference).Scan(&eventID, &evidence); errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, ErrLedgerRecordNotFound
	} else if err != nil {
		return nil, uuid.Nil, err
	}
	return evidence, eventID, nil
}
