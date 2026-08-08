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

	"github.com/google/uuid"
)

type CollectionWebhookWorker struct {
	repository *LedgerRepository
	ledger     *LedgerService
	checkout   *CheckoutService
	logger     *slog.Logger
	workerID   string
}

func NewCollectionWebhookWorker(repository *LedgerRepository, ledger *LedgerService, checkout *CheckoutService, logger *slog.Logger) *CollectionWebhookWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &CollectionWebhookWorker{
		repository: repository, ledger: ledger, checkout: checkout, logger: logger,
		workerID: "collection-webhook-" + uuid.NewString(),
	}
}

func (w *CollectionWebhookWorker) Start(ctx context.Context) {
	if w == nil || w.repository == nil || w.ledger == nil || w.checkout == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
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

func (w *CollectionWebhookWorker) runOnce(ctx context.Context) {
	jobs, err := w.repository.ClaimCollectionWebhookJobs(ctx, w.workerID, 20, 30*time.Second)
	if err != nil {
		w.logger.Error("claim collection webhook jobs failed", "error", err)
		return
	}
	for _, job := range jobs {
		if err := w.process(ctx, job); err != nil {
			retryAt := time.Now().UTC().Add(webhookRetryDelay(job.Attempts))
			_ = w.repository.FailVerifiedWebhookProcessing(ctx, job.AggregateID, retryAt, err.Error())
			if failErr := w.repository.FailFinancialJob(ctx, job.ID, w.workerID, retryAt, err.Error()); failErr != nil {
				w.logger.Error("fail collection webhook job", "job_id", job.ID.String(), "error", failErr)
			}
			continue
		}
		if err := w.repository.CompleteFinancialJob(ctx, job.ID, w.workerID); err != nil {
			w.logger.Error("complete collection webhook job", "job_id", job.ID.String(), "error", err)
		}
	}
}

func (w *CollectionWebhookWorker) process(ctx context.Context, job FinancialJob) error {
	payload, err := w.ledger.LoadVerifiedWebhook(ctx, job.AggregateID)
	if err != nil {
		return err
	}
	if payload.Event.ProcessingStatus == "completed" {
		return nil
	}
	if _, err := w.repository.BeginVerifiedWebhookProcessing(ctx, job.AggregateID); err != nil {
		return err
	}
	reference, err := collectionReference(payload.NormalizedEvent)
	if err != nil {
		return err
	}
	payment, err := w.repository.GetPaymentByReference(ctx, payload.Event.Provider, reference)
	if err != nil {
		return err
	}
	if input, handled, err := adjustmentFromWebhook(payload.Event, payload.NormalizedEvent, payment); err != nil {
		return err
	} else if handled {
		if _, _, err := w.repository.RecordPaymentAdjustment(ctx, input); err != nil {
			return err
		}
		return w.repository.CompleteVerifiedWebhookProcessing(ctx, job.AggregateID, "payment adjustment recorded")
	}
	reconciled, err := w.checkout.Reconcile(ctx, payment)
	if err != nil {
		return err
	}
	if !webhookReconciliationComplete(reconciled.Status) {
		return fmt.Errorf("provider reconciliation remains %s", reconciled.Status)
	}
	return w.repository.CompleteVerifiedWebhookProcessing(
		ctx,
		job.AggregateID,
		"payment reconciliation reached "+string(reconciled.Status),
	)
}

func webhookReconciliationComplete(status PaymentStatus) bool {
	switch status {
	case PaymentStatusPaid, PaymentStatusPartiallyRefunded, PaymentStatusRefunded,
		PaymentStatusDisputed, PaymentStatusReversed, PaymentStatusFailed,
		PaymentStatusExpired, PaymentStatusCancelled:
		return true
	default:
		return false
	}
}

func adjustmentFromWebhook(
	event StoredWebhookEvent,
	normalized json.RawMessage,
	payment FinancialPayment,
) (RecordPaymentAdjustmentInput, bool, error) {
	var values map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(normalized)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return RecordPaymentAdjustmentInput{}, false, fmt.Errorf("decode payment adjustment webhook: %w", err)
	}
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	if event.Provider == "payaza" {
		reversed, _ := values["is_reversed"].(bool)
		if !reversed {
			return RecordPaymentAdjustmentInput{}, false, nil
		}
		return RecordPaymentAdjustmentInput{
			PaymentID: payment.ID, Provider: payment.Provider,
			ProviderReference: "webhook:" + event.ID.String(), Kind: "reversal", Status: "successful",
			CurrencyCode: payment.CurrencyCode, AmountMinor: int64(payment.AmountMinor),
			AllocationImpact: int64(payment.AmountMinor), Reason: "Provider reported collection reversal",
		}, true, nil
	}
	if event.Provider != "paystack" {
		return RecordPaymentAdjustmentInput{}, false, nil
	}

	input := RecordPaymentAdjustmentInput{
		PaymentID: payment.ID, Provider: payment.Provider, CurrencyCode: payment.CurrencyCode,
		Reason: eventType,
	}
	switch {
	case strings.HasPrefix(eventType, "refund."):
		amount, err := paystackMinorAmount(values["amount"])
		if err != nil || amount <= 0 {
			return RecordPaymentAdjustmentInput{}, true, errors.New("Paystack refund webhook has an invalid amount")
		}
		if amount > int64(payment.AmountMinor) {
			return RecordPaymentAdjustmentInput{}, true, errors.New("Paystack refund exceeds the payment amount")
		}
		if currency := strings.ToUpper(strings.TrimSpace(stringValue(values["currency"]))); currency != "" && currency != payment.CurrencyCode {
			return RecordPaymentAdjustmentInput{}, true, errors.New("Paystack refund currency does not match the payment")
		}
		input.Kind = "partial_refund"
		if amount == int64(payment.AmountMinor) {
			input.Kind = "refund"
		}
		input.Status = paystackRefundAdjustmentStatus(eventType)
		input.AmountMinor = amount
		input.AllocationImpact = amount
		input.ProviderReference = adjustmentReference(event, values, "refund_reference")
		return input, true, nil
	case strings.HasPrefix(eventType, "charge.dispute."):
		amount := int64(payment.AmountMinor)
		if value, exists := values["refund_amount"]; exists && value != nil {
			parsed, err := paystackMinorAmount(value)
			if err != nil || parsed <= 0 || parsed > int64(payment.AmountMinor) {
				return RecordPaymentAdjustmentInput{}, true, errors.New("Paystack dispute has an invalid amount")
			}
			amount = parsed
		}
		input.Kind = "dispute"
		input.Status = "pending"
		if eventType == "charge.dispute.resolve" {
			switch strings.ToLower(strings.TrimSpace(stringValue(values["resolution"]))) {
			case "merchant-accepted":
				input.Status = "successful"
			case "declined":
				input.Status = "failed"
			default:
				return RecordPaymentAdjustmentInput{}, true, errors.New("Paystack resolved dispute has no supported resolution")
			}
		}
		input.AmountMinor = amount
		input.AllocationImpact = amount
		input.ProviderReference = adjustmentReference(event, values, "id")
		return input, true, nil
	default:
		return RecordPaymentAdjustmentInput{}, false, nil
	}
}

func paystackRefundAdjustmentStatus(eventType string) string {
	switch eventType {
	case "refund.processed":
		return "successful"
	case "refund.failed":
		return "failed"
	default:
		return "pending"
	}
}

func adjustmentReference(event StoredWebhookEvent, values map[string]any, key string) string {
	if value := strings.TrimSpace(stringValue(values[key])); value != "" && value != "<nil>" {
		return value
	}
	return "webhook:" + event.ID.String()
}

func paystackMinorAmount(value any) (int64, error) {
	text := strings.TrimSpace(stringValue(value))
	if !isCanonicalNonNegativeInteger(text) {
		return 0, errors.New("amount is not canonical minor units")
	}
	return strconv.ParseInt(text, 10, 64)
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

func collectionReference(normalized json.RawMessage) (string, error) {
	var values map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(normalized)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return "", fmt.Errorf("decode normalized collection webhook: %w", err)
	}
	for _, key := range []string{"merchant_reference", "merchant_transaction_reference", "reference", "transaction_reference"} {
		if value, ok := values[key].(string); ok && isPaymentReference(value) {
			return strings.TrimSpace(value), nil
		}
	}
	return "", errors.New("verified collection webhook has no TellBook payment reference")
}

func webhookRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * 15 * time.Second
}
