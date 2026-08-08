package payments

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type paymentStatusReconciler interface {
	ReconcileByPublicToken(context.Context, string) (FinancialPayment, error)
}

type activeReconciliation struct {
	cancel     context.CancelFunc
	done       chan struct{}
	wake       chan struct{}
	references int
	lastNudge  time.Time
}

// ActivePaymentReconciler keeps provider polling server-side during checkout and
// while customers watch status. One loop is shared by every holder of a token.
type ActivePaymentReconciler struct {
	rootContext  context.Context
	reconciler   paymentStatusReconciler
	logger       *slog.Logger
	fastFor      time.Duration
	fastEvery    time.Duration
	slowEvery    time.Duration
	hintCooldown time.Duration

	mu     sync.Mutex
	active map[string]*activeReconciliation
}

func NewActivePaymentReconciler(
	ctx context.Context,
	checkout *CheckoutService,
	logger *slog.Logger,
) *ActivePaymentReconciler {
	var reconciler paymentStatusReconciler
	if checkout != nil {
		reconciler = checkout
	}
	return newActivePaymentReconciler(ctx, reconciler, logger, 2*time.Minute, 5*time.Second, 30*time.Second)
}

func newActivePaymentReconciler(
	ctx context.Context,
	reconciler paymentStatusReconciler,
	logger *slog.Logger,
	fastFor time.Duration,
	fastEvery time.Duration,
	slowEvery time.Duration,
) *ActivePaymentReconciler {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ActivePaymentReconciler{
		rootContext:  ctx,
		reconciler:   reconciler,
		logger:       logger,
		fastFor:      fastFor,
		fastEvery:    fastEvery,
		slowEvery:    slowEvery,
		hintCooldown: 2 * time.Second,
		active:       make(map[string]*activeReconciliation),
	}
}

// Watch starts or joins active reconciliation for a payment token. The returned
// function must be called when the watcher disconnects.
func (r *ActivePaymentReconciler) Watch(paymentToken string) func() {
	release, _ := r.acquire(paymentToken)
	return release
}

// TrackCheckout keeps reconciliation active through the initial checkout window
// even when the browser cannot establish an SSE connection.
func (r *ActivePaymentReconciler) TrackCheckout(paymentToken string) {
	if r == nil {
		return
	}
	release, done := r.acquire(paymentToken)
	timer := time.NewTimer(r.fastFor)
	go func() {
		defer timer.Stop()
		select {
		case <-r.rootContext.Done():
		case <-done:
		case <-timer.C:
		}
		release()
	}()
}

// Nudge requests an immediate provider verification without creating a second
// reconciliation loop. Repeated browser hints are coalesced per payment.
func (r *ActivePaymentReconciler) Nudge(paymentToken string) bool {
	paymentToken = strings.TrimSpace(paymentToken)
	if r == nil || r.reconciler == nil || paymentToken == "" {
		return false
	}

	r.mu.Lock()
	entry := r.active[paymentToken]
	r.mu.Unlock()
	if entry == nil {
		r.TrackCheckout(paymentToken)
		r.mu.Lock()
		entry = r.active[paymentToken]
		if entry != nil {
			entry.lastNudge = time.Now()
		}
		r.mu.Unlock()
		return entry != nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	entry = r.active[paymentToken]
	if entry == nil {
		return false
	}
	now := time.Now()
	if !entry.lastNudge.IsZero() && now.Sub(entry.lastNudge) < r.hintCooldown {
		return false
	}
	entry.lastNudge = now
	select {
	case entry.wake <- struct{}{}:
	default:
	}
	return true
}

func (r *ActivePaymentReconciler) acquire(paymentToken string) (func(), <-chan struct{}) {
	paymentToken = strings.TrimSpace(paymentToken)
	if r == nil || r.reconciler == nil || paymentToken == "" {
		done := make(chan struct{})
		close(done)
		return func() {}, done
	}

	r.mu.Lock()
	entry := r.active[paymentToken]
	if entry == nil {
		pollContext, cancel := context.WithCancel(r.rootContext)
		entry = &activeReconciliation{
			cancel: cancel,
			done:   make(chan struct{}),
			wake:   make(chan struct{}, 1),
		}
		r.active[paymentToken] = entry
		go r.poll(pollContext, paymentToken, entry)
	}
	entry.references++
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			current := r.active[paymentToken]
			if current != entry {
				return
			}
			current.references--
			if current.references > 0 {
				return
			}
			r.stopLocked(paymentToken, current)
		})
	}, entry.done
}

func (r *ActivePaymentReconciler) poll(ctx context.Context, paymentToken string, entry *activeReconciliation) {
	startedAt := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	consecutiveErrors := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-entry.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}

		payment, err := r.reconciler.ReconcileByPublicToken(ctx, paymentToken)
		if err == nil {
			consecutiveErrors = 0
			if isTerminalPaymentStatus(payment.Status) {
				r.stop(paymentToken, entry)
				return
			}
		} else if !errors.Is(err, context.Canceled) {
			consecutiveErrors++
			if consecutiveErrors == 1 || consecutiveErrors%6 == 0 {
				r.logger.Warn(
					"active payment reconciliation failed",
					"consecutive_errors", consecutiveErrors,
					"error", err,
				)
			}
		}

		// A hint received during the provider call is already covered by that call.
		select {
		case <-entry.wake:
		default:
		}
		next := r.slowEvery
		if time.Since(startedAt) < r.fastFor {
			next = r.fastEvery
		}
		timer.Reset(next)
	}
}

func (r *ActivePaymentReconciler) stop(paymentToken string, entry *activeReconciliation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[paymentToken] == entry {
		r.stopLocked(paymentToken, entry)
	}
}

func (r *ActivePaymentReconciler) stopLocked(paymentToken string, entry *activeReconciliation) {
	delete(r.active, paymentToken)
	entry.cancel()
	close(entry.done)
}
