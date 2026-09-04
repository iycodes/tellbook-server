package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (repository *LedgerRepository) ClaimPaymentReconciliationJobs(
	ctx context.Context,
	workerID string,
	limit int,
	leaseDuration time.Duration,
) ([]FinancialJob, error) {
	workerID = strings.TrimSpace(workerID)
	if repository == nil || repository.db == nil || workerID == "" || limit < 1 || leaseDuration <= 0 {
		return nil, errors.New("invalid payment reconciliation claim")
	}
	leaseSeconds := max(int64(leaseDuration/time.Second), 1)
	rows, err := repository.db.Query(ctx, `
		WITH ranked AS (
			SELECT job.id, job.available_at, job.created_at,
				ROW_NUMBER() OVER (
					PARTITION BY payment.provider
					ORDER BY job.available_at, job.created_at
				) AS provider_rank
			FROM financial_jobs job
			JOIN payments payment ON payment.id=job.aggregate_id
			LEFT JOIN payment_provider_request_budgets budget ON budget.provider=payment.provider
			WHERE job.kind=$1 AND (
				(job.status IN ('pending','failed') AND job.available_at <= NOW())
				OR (job.status='processing' AND job.lease_expires_at <= NOW())
			)
			AND (budget.provider IS NULL OR (
				budget.next_allowed_at<=NOW()
				AND (budget.lease_expires_at IS NULL OR budget.lease_expires_at<=NOW())
			))
		), candidates AS (
			SELECT job.id
			FROM financial_jobs job
			JOIN ranked ON ranked.id=job.id
			WHERE ranked.provider_rank = 1
			ORDER BY ranked.provider_rank, ranked.available_at, ranked.created_at
			FOR UPDATE OF job SKIP LOCKED
			LIMIT $2
		)
		UPDATE financial_jobs job
		SET status='processing', attempts=attempts+1, lease_owner=$3,
			lease_expires_at=NOW()+($4*INTERVAL '1 second'), updated_at=NOW()
		FROM candidates
		WHERE job.id=candidates.id
		RETURNING job.id,job.kind,job.aggregate_type,job.aggregate_id,
			job.deduplication_key,job.payload,job.attempts,job.lease_owner,
			job.lease_expires_at,job.created_at
	`, paymentReconciliationJobKind, limit, workerID, leaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("claim payment reconciliation jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]FinancialJob, 0, limit)
	for rows.Next() {
		var job FinancialJob
		if err := rows.Scan(
			&job.ID, &job.Kind, &job.AggregateType, &job.AggregateID,
			&job.DeduplicationKey, &job.Payload, &job.Attempts, &job.LeaseOwner,
			&job.LeaseExpiresAt, &job.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan payment reconciliation job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate payment reconciliation jobs: %w", err)
	}
	return jobs, nil
}

func (repository *LedgerRepository) SchedulePaymentReconciliation(
	ctx context.Context,
	paymentID uuid.UUID,
	publicToken string,
	nudge bool,
) error {
	if repository == nil || repository.db == nil || paymentID == uuid.Nil || strings.TrimSpace(publicToken) == "" {
		return errors.New("invalid payment reconciliation schedule")
	}
	payload, err := json.Marshal(map[string]string{"payment_token": strings.TrimSpace(publicToken)})
	if err != nil {
		return fmt.Errorf("encode payment reconciliation job: %w", err)
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin payment reconciliation schedule: %w", err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO financial_jobs (
			id, kind, aggregate_type, aggregate_id, deduplication_key, payload,
			status, available_at, created_at, updated_at
		)
		VALUES ($1,$2,'payment',$3,$4,$5,'pending',NOW(),NOW(),NOW())
		ON CONFLICT (deduplication_key) DO UPDATE
		SET
			payload=EXCLUDED.payload,
			status=CASE
				WHEN financial_jobs.status IN ('pending','failed') THEN 'pending'
				ELSE financial_jobs.status
			END,
			available_at=CASE
				WHEN $6 AND financial_jobs.status IN ('pending','failed')
					THEN LEAST(financial_jobs.available_at,NOW())
				ELSE financial_jobs.available_at
			END,
			updated_at=CASE
				WHEN financial_jobs.status IN ('pending','failed','processing') THEN NOW()
				ELSE financial_jobs.updated_at
			END
	`, uuid.New(), paymentReconciliationJobKind, paymentID,
		paymentReconciliationDeduplicationKey(paymentID), payload, nudge)
	if err != nil {
		return fmt.Errorf("upsert payment reconciliation job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit payment reconciliation schedule: %w", err)
	}
	return nil
}

func (repository *LedgerRepository) DeferPaymentReconciliationJob(
	ctx context.Context,
	jobID uuid.UUID,
	workerID string,
	retryAt time.Time,
) error {
	workerID = strings.TrimSpace(workerID)
	if repository == nil || repository.db == nil || jobID == uuid.Nil || workerID == "" || retryAt.IsZero() {
		return errors.New("invalid payment reconciliation deferral")
	}
	tag, err := repository.db.Exec(ctx, `
		UPDATE financial_jobs
		SET status='pending',attempts=GREATEST(0,attempts-1),available_at=$3,
			lease_owner='',lease_expires_at=NULL,last_error='',updated_at=NOW()
		WHERE id=$1 AND kind=$4 AND status='processing' AND lease_owner=$2
	`, jobID, workerID, retryAt.UTC(), paymentReconciliationJobKind)
	if err != nil {
		return fmt.Errorf("defer payment reconciliation job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (repository *LedgerRepository) NextPaymentReconciliationDelay(
	ctx context.Context,
	fallback time.Duration,
) time.Duration {
	if repository == nil || repository.db == nil || fallback <= 0 {
		return fallback
	}
	var next time.Time
	err := repository.db.QueryRow(ctx, `
		SELECT COALESCE(MIN(
			GREATEST(
				CASE WHEN job.status='processing' THEN job.lease_expires_at ELSE job.available_at END,
				COALESCE(budget.next_allowed_at,'-infinity'::timestamptz),
				COALESCE(budget.lease_expires_at,'-infinity'::timestamptz)
			)
		), NOW()+($2::bigint*INTERVAL '1 millisecond'))
		FROM financial_jobs job
		JOIN payments payment ON payment.id=job.aggregate_id
		LEFT JOIN payment_provider_request_budgets budget ON budget.provider=payment.provider
		WHERE job.kind=$1 AND job.status IN ('pending','failed','processing')
	`, paymentReconciliationJobKind, fallback.Milliseconds()).Scan(&next)
	if err != nil {
		return fallback
	}
	delay := time.Until(next)
	if delay < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if delay > fallback {
		return fallback
	}
	return delay
}

func (repository *LedgerRepository) WithPaymentProviderBudget(
	ctx context.Context,
	provider string,
	minimumInterval time.Duration,
	fn func() (FinancialPayment, error),
) (FinancialPayment, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if repository == nil || repository.db == nil || provider == "" || minimumInterval <= 0 || fn == nil {
		return FinancialPayment{}, errors.New("invalid payment provider budget request")
	}
	tx, err := repository.db.Begin(ctx)
	if err != nil {
		return FinancialPayment{}, fmt.Errorf("begin payment provider budget reservation: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_provider_request_budgets (
			provider,next_allowed_at,lease_owner,lease_expires_at,updated_at
		)
		VALUES ($1,NOW(),'',NULL,NOW()) ON CONFLICT (provider) DO NOTHING
	`, provider); err != nil {
		return FinancialPayment{}, fmt.Errorf("initialize payment provider budget: %w", err)
	}
	var nextAllowed time.Time
	var leaseExpiresAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT next_allowed_at,lease_expires_at FROM payment_provider_request_budgets
		WHERE provider=$1 FOR UPDATE
	`, provider).Scan(&nextAllowed, &leaseExpiresAt); err != nil {
		return FinancialPayment{}, fmt.Errorf("load payment provider budget: %w", err)
	}
	now := time.Now().UTC()
	if nextAllowed.After(now) || (leaseExpiresAt != nil && leaseExpiresAt.After(now)) {
		retryAt := nextAllowed
		capacityProbeAt := now.Add(250 * time.Millisecond)
		if capacityProbeAt.After(retryAt) {
			retryAt = capacityProbeAt
		}
		return FinancialPayment{}, &PaymentProviderBudgetBusyError{RetryAt: retryAt}
	}
	leaseOwner := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		UPDATE payment_provider_request_budgets
		SET next_allowed_at=$2,lease_owner=$3,lease_expires_at=$4,updated_at=NOW()
		WHERE provider=$1
	`, provider, now.Add(minimumInterval), leaseOwner, now.Add(paymentProviderLeaseDuration)); err != nil {
		return FinancialPayment{}, fmt.Errorf("reserve payment provider budget: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FinancialPayment{}, fmt.Errorf("commit payment provider budget: %w", err)
	}
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = repository.db.Exec(releaseContext, `
			WITH released AS (
				UPDATE payment_provider_request_budgets
				SET lease_owner='',lease_expires_at=NULL,updated_at=NOW()
				WHERE provider=$1 AND lease_owner=$2
				RETURNING provider
			)
			SELECT pg_notify($3,'payment_provider_budget') FROM released
		`, provider, leaseOwner, coreWorkerWakeChannel)
	}()
	return fn()
}
