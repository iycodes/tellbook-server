package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PayoutWebhookWorker struct {
	repository *LedgerRepository
	ledger     *LedgerService
	payouts    *PayoutService
	logger     *slog.Logger
	workerID   string
}

func NewPayoutWebhookWorker(repository *LedgerRepository, ledger *LedgerService, payouts *PayoutService, logger *slog.Logger) *PayoutWebhookWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &PayoutWebhookWorker{
		repository: repository, ledger: ledger, payouts: payouts, logger: logger,
		workerID: "payout-webhook-" + uuid.NewString(),
	}
}

func (w *PayoutWebhookWorker) Start(ctx context.Context) {
	if w == nil || w.repository == nil || w.ledger == nil || w.payouts == nil {
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

func (w *PayoutWebhookWorker) runOnce(ctx context.Context) {
	jobs, err := w.repository.ClaimPayoutWebhookJobs(ctx, w.workerID, 20, 30*time.Second)
	if err != nil {
		w.logger.Error("claim payout webhook jobs failed", "error", err)
		return
	}
	for _, job := range jobs {
		if err := w.process(ctx, job); err != nil {
			retryAt := time.Now().UTC().Add(webhookRetryDelay(job.Attempts))
			_ = w.repository.FailVerifiedWebhookProcessing(ctx, job.AggregateID, retryAt, err.Error())
			_ = w.repository.FailFinancialJob(ctx, job.ID, w.workerID, retryAt, err.Error())
			continue
		}
		if err := w.repository.CompleteFinancialJob(ctx, job.ID, w.workerID); err != nil {
			w.logger.Error("complete payout webhook job", "job_id", job.ID.String(), "error", err)
		}
	}
}

func (w *PayoutWebhookWorker) process(ctx context.Context, job FinancialJob) error {
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
	reference, err := payoutReference(payload.NormalizedEvent)
	if err != nil {
		return err
	}
	payout, err := w.repository.GetFinancialPayoutByReference(ctx, payload.Event.Provider, reference)
	if err != nil {
		return err
	}
	if _, err := w.payouts.Reconcile(ctx, payout); err != nil {
		return err
	}
	return w.repository.CompleteVerifiedWebhookProcessing(ctx, job.AggregateID, "payout reconciled")
}

func payoutReference(normalized json.RawMessage) (string, error) {
	var values map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(normalized)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return "", fmt.Errorf("decode normalized payout webhook: %w", err)
	}
	for _, key := range []string{"merchant_reference", "reference", "transaction_reference"} {
		if value, ok := values[key].(string); ok && isPayoutReference(value) {
			return strings.TrimSpace(value), nil
		}
	}
	return "", errors.New("verified payout webhook has no TellBook payout reference")
}

type PayoutReconciler struct {
	repository *LedgerRepository
	payouts    *PayoutService
	logger     *slog.Logger
	workerID   string
}

func NewPayoutReconciler(repository *LedgerRepository, payouts *PayoutService, logger *slog.Logger) *PayoutReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PayoutReconciler{
		repository: repository,
		payouts:    payouts,
		logger:     logger,
		workerID:   "payout-reconciliation-" + uuid.NewString(),
	}
}

func (w *PayoutReconciler) Start(ctx context.Context) {
	if w == nil || w.repository == nil || w.payouts == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			items, err := w.repository.ClaimStalePayouts(
				ctx,
				w.workerID,
				time.Now().UTC().Add(-30*time.Second),
				50,
				2*time.Minute,
			)
			if err != nil {
				w.logger.Error("claim stale payouts failed", "error", err)
				continue
			}
			for _, payout := range items {
				var err error
				if payout.Status == PayoutStatusCreated {
					_, err = w.payouts.RetryCreated(ctx, payout)
				} else {
					_, err = w.payouts.Reconcile(ctx, payout)
				}
				if err != nil {
					if delayErr := w.repository.DelayPayoutReconciliation(ctx, payout.ID, w.workerID, time.Now().UTC().Add(time.Minute)); delayErr != nil {
						w.logger.Error("delay payout reconciliation retry", "payout_id", payout.ID.String(), "error", delayErr)
					}
					w.logger.Warn("reconcile payout failed", "payout_id", payout.ID.String(), "error", err)
				}
			}
		}
	}
}
