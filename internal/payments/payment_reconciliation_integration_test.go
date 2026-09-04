package payments

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPaymentProviderBudgetDoesNotHoldDatabaseConnectionDuringProviderCall(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	provider := "integration-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM payment_provider_request_budgets WHERE provider=$1
		`, provider)
	})
	repository := NewLedgerRepository(pool)
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProvider) }) }
	t.Cleanup(release)
	firstResult := make(chan error, 1)
	go func() {
		_, callErr := repository.WithPaymentProviderBudget(
			ctx,
			provider,
			200*time.Millisecond,
			func() (FinancialPayment, error) {
				close(providerStarted)
				<-releaseProvider
				return FinancialPayment{}, nil
			},
		)
		firstResult <- callErr
	}()

	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("first provider call did not start")
	}
	secondContext, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	_, err = repository.WithPaymentProviderBudget(
		secondContext,
		provider,
		200*time.Millisecond,
		func() (FinancialPayment, error) {
			t.Fatal("second provider call bypassed the persisted request budget")
			return FinancialPayment{}, nil
		},
	)
	if !errors.Is(err, ErrPaymentProviderBudgetBusy) {
		t.Fatalf("second provider budget error=%v, want ErrPaymentProviderBudgetBusy", err)
	}
	var busy *PaymentProviderBudgetBusyError
	if !errors.As(err, &busy) || !busy.RetryAt.After(time.Now().UTC()) {
		t.Fatalf("provider budget did not return a future capacity retry: %#v", err)
	}
	release()
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	time.Sleep(220 * time.Millisecond)
	called := false
	_, err = repository.WithPaymentProviderBudget(
		ctx,
		provider,
		200*time.Millisecond,
		func() (FinancialPayment, error) {
			called = true
			return FinancialPayment{}, nil
		},
	)
	if err != nil || !called {
		t.Fatalf("provider budget was not reusable: called=%v error=%v", called, err)
	}
}

func TestPaymentProviderCapacityDeferralDoesNotConsumeAnAttempt(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	jobID := uuid.New()
	workerID := "capacity-test-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO financial_jobs (
			id,kind,aggregate_type,aggregate_id,deduplication_key,payload,status,
			attempts,available_at,lease_owner,lease_expires_at
		) VALUES ($1,$2,'payment',$3,$4,'{}'::jsonb,'processing',3,NOW(),$5,NOW()+INTERVAL '1 minute')
	`, jobID, paymentReconciliationJobKind, uuid.New(), "capacity-test:"+jobID.String(), workerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM financial_jobs WHERE id=$1`, jobID) })

	retryAt := time.Now().UTC().Add(2 * time.Second)
	repository := NewLedgerRepository(pool)
	if err := repository.DeferPaymentReconciliationJob(ctx, jobID, workerID, retryAt); err != nil {
		t.Fatal(err)
	}
	var status, leaseOwner string
	var attempts int
	var availableAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status,attempts,available_at,lease_owner FROM financial_jobs WHERE id=$1
	`, jobID).Scan(&status, &attempts, &availableAt, &leaseOwner); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 2 || leaseOwner != "" || availableAt.Before(retryAt.Add(-50*time.Millisecond)) {
		t.Fatalf(
			"capacity deferral = status %s attempts %d available %s owner %q",
			status, attempts, availableAt, leaseOwner,
		)
	}
}
