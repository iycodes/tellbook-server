package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	paymentReconciliationJobKind = "reconcile_payment"
	paymentReconciliationMaxAge  = 48 * time.Hour
	paymentReconciliationMaxRuns = 1000
	paymentProviderLeaseDuration = 30 * time.Second
)

var ErrPaymentProviderBudgetBusy = errors.New("payment provider request budget is busy")

type PaymentProviderBudgetBusyError struct {
	RetryAt time.Time
}

func (err *PaymentProviderBudgetBusyError) Error() string {
	return ErrPaymentProviderBudgetBusy.Error()
}

func (err *PaymentProviderBudgetBusyError) Is(target error) bool {
	return target == ErrPaymentProviderBudgetBusy
}

type PaymentReconciliationScheduler struct {
	repository *LedgerRepository
}

func NewPaymentReconciliationScheduler(repository *LedgerRepository) *PaymentReconciliationScheduler {
	return &PaymentReconciliationScheduler{repository: repository}
}

func (scheduler *PaymentReconciliationScheduler) Schedule(ctx context.Context, payment FinancialPayment) error {
	if scheduler == nil || scheduler.repository == nil || payment.ID == uuid.Nil || strings.TrimSpace(payment.PublicToken) == "" {
		return errors.New("payment reconciliation scheduler is unavailable")
	}
	return scheduler.repository.SchedulePaymentReconciliation(ctx, payment.ID, payment.PublicToken, false)
}

func (scheduler *PaymentReconciliationScheduler) Nudge(ctx context.Context, payment FinancialPayment) error {
	if scheduler == nil || scheduler.repository == nil || payment.ID == uuid.Nil || strings.TrimSpace(payment.PublicToken) == "" {
		return errors.New("payment reconciliation scheduler is unavailable")
	}
	return scheduler.repository.SchedulePaymentReconciliation(ctx, payment.ID, payment.PublicToken, true)
}

type PaymentReconciliationWorker struct {
	repository *LedgerRepository
	checkout   *CheckoutService
	logger     *slog.Logger
	workerID   string
	wake       <-chan struct{}
}

func NewPaymentReconciliationWorker(
	repository *LedgerRepository,
	checkout *CheckoutService,
	logger *slog.Logger,
	wake <-chan struct{},
) *PaymentReconciliationWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &PaymentReconciliationWorker{
		repository: repository, checkout: checkout, logger: logger,
		workerID: "payment-reconciliation-" + uuid.NewString(), wake: wake,
	}
}

func (worker *PaymentReconciliationWorker) Start(ctx context.Context) {
	if worker == nil || worker.repository == nil || worker.checkout == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-worker.wake:
		case <-timer.C:
		}
		worker.drain(ctx)
		timer.Reset(worker.repository.NextPaymentReconciliationDelay(ctx, jitterDuration(25*time.Second, 0.2)))
	}
}

func (worker *PaymentReconciliationWorker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		jobs, err := worker.repository.ClaimPaymentReconciliationJobs(
			ctx, worker.workerID, 10, 2*time.Minute,
		)
		if err != nil {
			worker.logger.Error("claim payment reconciliation jobs failed", "error", err)
			return
		}
		if len(jobs) == 0 {
			return
		}
		var batch sync.WaitGroup
		batch.Add(len(jobs))
		for _, job := range jobs {
			go func() {
				defer batch.Done()
				worker.process(ctx, job)
			}()
		}
		batch.Wait()
		if len(jobs) < 10 {
			return
		}
	}
}

func (worker *PaymentReconciliationWorker) process(ctx context.Context, job FinancialJob) {
	now := time.Now().UTC()
	if job.Attempts > paymentReconciliationMaxRuns || now.Sub(job.CreatedAt) > paymentReconciliationMaxAge {
		if err := worker.repository.DeadLetterFinancialJob(
			ctx, job.ID, worker.workerID, "payment reconciliation exceeded its maximum attempts or age",
		); err != nil {
			worker.logger.Error("dead-letter payment reconciliation job failed", "job_id", job.ID, "error", err)
		}
		return
	}
	payment, err := worker.repository.GetPaymentByID(ctx, job.AggregateID)
	if err == nil && isTerminalPaymentStatus(payment.Status) {
		worker.complete(ctx, job)
		return
	}
	if err == nil {
		payment, err = worker.repository.WithPaymentProviderBudget(
			ctx, payment.Provider, 500*time.Millisecond,
			func() (FinancialPayment, error) {
				return worker.checkout.ReconcileByPublicToken(ctx, payment.PublicToken)
			},
		)
	}
	var budgetBusy *PaymentProviderBudgetBusyError
	if errors.As(err, &budgetBusy) {
		retryAt := budgetBusy.RetryAt
		if !retryAt.After(now) {
			retryAt = now.Add(500 * time.Millisecond)
		}
		retryAt = retryAt.Add(jitterDuration(250*time.Millisecond, 0.4))
		if deferErr := worker.repository.DeferPaymentReconciliationJob(
			ctx, job.ID, worker.workerID, retryAt,
		); deferErr != nil {
			worker.logger.Error("defer payment reconciliation for provider capacity failed", "job_id", job.ID, "error", deferErr)
		}
		return
	}
	if err == nil && isTerminalPaymentStatus(payment.Status) {
		worker.complete(ctx, job)
		return
	}
	delay := paymentReconciliationDelay(now.Sub(job.CreatedAt), job.Attempts, err != nil)
	reason := "payment remains non-terminal"
	if err != nil {
		reason = err.Error()
	}
	if failErr := worker.repository.FailFinancialJob(ctx, job.ID, worker.workerID, now.Add(delay), reason); failErr != nil {
		worker.logger.Error("reschedule payment reconciliation job failed", "job_id", job.ID, "error", failErr)
	}
}

func (worker *PaymentReconciliationWorker) complete(ctx context.Context, job FinancialJob) {
	if err := worker.repository.CompleteFinancialJob(ctx, job.ID, worker.workerID); err != nil {
		worker.logger.Error("complete payment reconciliation job failed", "job_id", job.ID, "error", err)
	}
}

func paymentReconciliationDelay(age time.Duration, attempts int, failed bool) time.Duration {
	base := 5 * time.Second
	switch {
	case age >= 2*time.Hour:
		base = 5 * time.Minute
	case age >= 10*time.Minute:
		base = 2 * time.Minute
	case age >= 2*time.Minute:
		base = 30 * time.Second
	}
	if failed {
		shift := min(max(attempts/3, 0), 6)
		errorBackoff := 5 * time.Second * time.Duration(1<<shift)
		if errorBackoff > 5*time.Minute {
			errorBackoff = 5 * time.Minute
		}
		if errorBackoff > base {
			base = errorBackoff
		}
	}
	return jitterDuration(base, 0.2)
}

func jitterDuration(base time.Duration, ratio float64) time.Duration {
	if base <= 0 || ratio <= 0 {
		return base
	}
	delta := (rand.Float64()*2 - 1) * ratio
	return time.Duration(float64(base) * (1 + delta))
}

func paymentReconciliationDeduplicationKey(paymentID uuid.UUID) string {
	return fmt.Sprintf("%s:%s", paymentReconciliationJobKind, paymentID)
}
