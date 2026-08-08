package appdata

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

type Reconciler struct {
	repo     *payments.LedgerRepository
	checkout *payments.CheckoutService
	logger   *slog.Logger
	interval time.Duration
	staleFor time.Duration
	limit    int
	workerID string
}

func NewReconciler(repo *payments.LedgerRepository, checkout *payments.CheckoutService, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		repo:     repo,
		checkout: checkout,
		logger:   logger,
		interval: 2 * time.Minute,
		staleFor: 90 * time.Second,
		limit:    100,
		workerID: "payment-reconciliation-" + uuid.NewString(),
	}
}

func (r *Reconciler) Start(ctx context.Context) {
	if r == nil || r.repo == nil || r.checkout == nil {
		return
	}

	r.runOnce(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *Reconciler) runOnce(ctx context.Context) {
	stalePayments, err := r.repo.ClaimStalePayments(
		ctx,
		r.workerID,
		time.Now().UTC().Add(-r.staleFor),
		r.limit,
		2*time.Minute,
	)
	if err != nil {
		r.logger.Error("claim stale pending payments failed", "error", err)
		return
	}

	for _, payment := range stalePayments {
		_, err := r.checkout.ReconcileByPublicToken(ctx, payment.PublicToken)
		switch {
		case err == nil:
		case errors.Is(err, payments.ErrLedgerRecordNotFound), errors.Is(err, payments.ErrConcurrentUpdate):
		default:
			if delayErr := r.repo.DelayPaymentReconciliation(ctx, payment.ID, r.workerID, time.Now().UTC().Add(r.interval)); delayErr != nil {
				r.logger.Error("delay payment reconciliation retry", "payment_id", payment.ID.String(), "error", delayErr)
			}
			r.logger.Warn("reconcile pending payment failed", "payment_id", payment.ID.String(), "provider", payment.Provider, "error", err)
		}
	}
}
