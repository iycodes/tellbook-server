package payments

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type activeReconcilerBackend struct {
	mu       sync.Mutex
	statuses []PaymentStatus
	calls    int
	called   chan int
}

func (f *activeReconcilerBackend) ReconcileByPublicToken(context.Context, string) (FinancialPayment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := min(f.calls, len(f.statuses)-1)
	f.calls++
	select {
	case f.called <- f.calls:
	default:
	}
	return FinancialPayment{Status: f.statuses[index]}, nil
}

func (f *activeReconcilerBackend) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestActivePaymentReconcilerSharesOneLoopAndStopsAtTerminalStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &activeReconcilerBackend{
		statuses: []PaymentStatus{PaymentStatusPending, PaymentStatusPaid},
		called:   make(chan int, 4),
	}
	reconciler := newActivePaymentReconciler(
		ctx,
		backend,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Minute,
		10*time.Millisecond,
		time.Minute,
	)

	stopFirst := reconciler.Watch("payment-token")
	stopSecond := reconciler.Watch("payment-token")
	defer stopFirst()
	defer stopSecond()

	waitForActiveReconcilerCall(t, backend.called, 1)
	waitForActiveReconcilerCall(t, backend.called, 2)
	time.Sleep(30 * time.Millisecond)
	if calls := backend.callCount(); calls != 2 {
		t.Fatalf("refresh calls = %d, want 2", calls)
	}
	if active := activeReconcilerCount(reconciler); active != 0 {
		t.Fatalf("active reconciliations = %d, want 0 after terminal status", active)
	}
}

func TestActivePaymentReconcilerStopsWhenLastWatcherLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &activeReconcilerBackend{
		statuses: []PaymentStatus{PaymentStatusPending},
		called:   make(chan int, 4),
	}
	reconciler := newActivePaymentReconciler(
		ctx,
		backend,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Minute,
		20*time.Millisecond,
		time.Minute,
	)

	stop := reconciler.Watch("payment-token")
	waitForActiveReconcilerCall(t, backend.called, 1)
	stop()
	time.Sleep(50 * time.Millisecond)
	if calls := backend.callCount(); calls != 1 {
		t.Fatalf("refresh calls after watcher left = %d, want 1", calls)
	}
}

func TestActivePaymentReconcilerTracksNewCheckoutWithoutAWatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &activeReconcilerBackend{
		statuses: []PaymentStatus{PaymentStatusPending},
		called:   make(chan int, 8),
	}
	reconciler := newActivePaymentReconciler(
		ctx,
		backend,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		35*time.Millisecond,
		10*time.Millisecond,
		time.Minute,
	)

	reconciler.TrackCheckout("payment-token")
	waitForActiveReconcilerCall(t, backend.called, 1)
	waitForActiveReconcilerCall(t, backend.called, 2)
	time.Sleep(70 * time.Millisecond)
	if calls := backend.callCount(); calls < 2 || calls > 5 {
		t.Fatalf("refresh calls after checkout tracking window = %d, want 2..5", calls)
	}
	if active := activeReconcilerCount(reconciler); active != 0 {
		t.Fatalf("active reconciliations = %d, want 0 after tracking window", active)
	}
}

func TestActivePaymentReconcilerNudgeRunsImmediatelyAndCoalescesHints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &activeReconcilerBackend{
		statuses: []PaymentStatus{PaymentStatusPending},
		called:   make(chan int, 8),
	}
	reconciler := newActivePaymentReconciler(
		ctx,
		backend,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Minute,
		time.Minute,
		time.Minute,
	)
	reconciler.hintCooldown = 20 * time.Millisecond

	stop := reconciler.Watch("payment-token")
	defer stop()
	waitForActiveReconcilerCall(t, backend.called, 1)
	if !reconciler.Nudge("payment-token") {
		t.Fatal("first verification hint was not accepted")
	}
	waitForActiveReconcilerCall(t, backend.called, 2)
	if reconciler.Nudge("payment-token") {
		t.Fatal("verification hint inside cooldown was not coalesced")
	}
	time.Sleep(25 * time.Millisecond)
	if !reconciler.Nudge("payment-token") {
		t.Fatal("verification hint after cooldown was not accepted")
	}
	waitForActiveReconcilerCall(t, backend.called, 3)
}

func TestActivePaymentReconcilerWithNoCheckoutIsInert(t *testing.T) {
	reconciler := NewActivePaymentReconciler(context.Background(), nil, nil)
	stop := reconciler.Watch("payment-token")
	stop()
	reconciler.TrackCheckout("payment-token")
	if active := activeReconcilerCount(reconciler); active != 0 {
		t.Fatalf("active reconciliations = %d, want 0", active)
	}
}

func activeReconcilerCount(reconciler *ActivePaymentReconciler) int {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return len(reconciler.active)
}

func waitForActiveReconcilerCall(t *testing.T, calls <-chan int, want int) {
	t.Helper()
	select {
	case got := <-calls:
		if got != want {
			t.Fatalf("refresh call = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for refresh call %d", want)
	}
}
