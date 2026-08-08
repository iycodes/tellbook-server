package payments

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/secure"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type StoreVerifiedWebhookInput struct {
	Provider        string
	ProviderEventID string
	EventType       string
	RawBody         []byte
	NormalizedEvent map[string]any
	VerifiedAt      time.Time
}

type StoredWebhookEvent struct {
	ID               uuid.UUID
	Provider         string
	ProviderEventID  string
	BodySHA256       []byte
	EventType        string
	ProcessingStatus string
	ReceivedAt       time.Time
	VerifiedAt       time.Time
}

func (s *LedgerService) StoreVerifiedWebhook(
	ctx context.Context,
	input StoreVerifiedWebhookInput,
) (StoredWebhookEvent, bool, error) {
	if s == nil || s.repository == nil || s.keyring == nil {
		return StoredWebhookEvent{}, false, errors.New("financial webhook storage is not configured")
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ProviderEventID = strings.TrimSpace(input.ProviderEventID)
	input.EventType = strings.TrimSpace(input.EventType)
	if (input.Provider != "payaza" && input.Provider != "paystack") || input.EventType == "" || len(input.RawBody) == 0 {
		return StoredWebhookEvent{}, false, errors.New("invalid verified webhook event")
	}
	if input.VerifiedAt.IsZero() {
		input.VerifiedAt = time.Now().UTC()
	}
	normalized, err := json.Marshal(input.NormalizedEvent)
	if err != nil {
		return StoredWebhookEvent{}, false, fmt.Errorf("encode normalized webhook event: %w", err)
	}

	eventID := uuid.New()
	encrypted, err := s.keyring.Encrypt(input.RawBody, providerWebhookEventAAD(eventID, input.Provider))
	if err != nil {
		return StoredWebhookEvent{}, false, err
	}
	bodyHash := sha256.Sum256(input.RawBody)
	return s.repository.insertVerifiedWebhook(ctx, insertVerifiedWebhookParams{
		ID: eventID, Provider: input.Provider, ProviderEventID: input.ProviderEventID,
		BodySHA256: bodyHash[:], EventType: input.EventType, Ciphertext: encrypted,
		NormalizedEvent: normalized, VerifiedAt: input.VerifiedAt,
		Process: hasTellBookFinancialReference(input.NormalizedEvent),
	})
}

func hasTellBookFinancialReference(normalized map[string]any) bool {
	for _, key := range []string{"merchant_reference", "reference", "transaction_reference"} {
		value, ok := normalized[key].(string)
		if !ok {
			continue
		}
		reference := strings.TrimSpace(value)
		if isPaymentReference(reference) || isPayoutReference(reference) {
			return true
		}
	}
	return false
}

type VerifiedWebhookPayload struct {
	Event           StoredWebhookEvent
	RawBody         []byte
	NormalizedEvent json.RawMessage
}

func (s *LedgerService) LoadVerifiedWebhook(
	ctx context.Context,
	eventID uuid.UUID,
) (VerifiedWebhookPayload, error) {
	if s == nil || s.repository == nil || s.keyring == nil || eventID == uuid.Nil {
		return VerifiedWebhookPayload{}, errors.New("financial webhook storage is not configured")
	}
	const query = `
		SELECT
			id, provider, provider_event_id, body_sha256, event_type, processing_status,
			received_at, verified_at, raw_body_ciphertext, raw_body_nonce,
			encryption_key_version, normalized_event
		FROM provider_webhook_events
		WHERE id = $1
	`
	var payload VerifiedWebhookPayload
	var ciphertext secure.Ciphertext
	if err := s.repository.db.QueryRow(ctx, query, eventID).Scan(
		&payload.Event.ID, &payload.Event.Provider, &payload.Event.ProviderEventID,
		&payload.Event.BodySHA256, &payload.Event.EventType, &payload.Event.ProcessingStatus,
		&payload.Event.ReceivedAt, &payload.Event.VerifiedAt, &ciphertext.Data,
		&ciphertext.Nonce, &ciphertext.KeyVersion, &payload.NormalizedEvent,
	); errors.Is(err, pgx.ErrNoRows) {
		return VerifiedWebhookPayload{}, ErrLedgerRecordNotFound
	} else if err != nil {
		return VerifiedWebhookPayload{}, fmt.Errorf("load verified webhook event: %w", err)
	}
	rawBody, err := s.keyring.Decrypt(ciphertext, providerWebhookEventAAD(payload.Event.ID, payload.Event.Provider))
	if err != nil {
		return VerifiedWebhookPayload{}, fmt.Errorf("decrypt verified webhook event: %w", err)
	}
	bodyHash := sha256.Sum256(rawBody)
	if !equalBytes(payload.Event.BodySHA256, bodyHash[:]) {
		return VerifiedWebhookPayload{}, errors.New("verified webhook body hash mismatch")
	}
	payload.RawBody = rawBody
	return payload, nil
}

func providerWebhookEventAAD(eventID uuid.UUID, provider string) []byte {
	return []byte("provider-webhook-event:" + eventID.String() + ":" + provider)
}

type insertVerifiedWebhookParams struct {
	ID              uuid.UUID
	Provider        string
	ProviderEventID string
	BodySHA256      []byte
	EventType       string
	Ciphertext      secure.Ciphertext
	NormalizedEvent json.RawMessage
	VerifiedAt      time.Time
	Process         bool
}

func (r *LedgerRepository) insertVerifiedWebhook(
	ctx context.Context,
	params insertVerifiedWebhookParams,
) (StoredWebhookEvent, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return StoredWebhookEvent{}, false, fmt.Errorf("begin webhook insert: %w", err)
	}
	defer tx.Rollback(ctx)

	const query = `
		INSERT INTO provider_webhook_events (
			id, provider, provider_event_id, body_sha256, event_type,
			raw_body_ciphertext, raw_body_nonce, encryption_key_version,
			normalized_event, processing_status, received_at, verified_at, next_attempt_at,
			processed_at, processing_result
		)
		VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,
			CASE WHEN $11 THEN 'pending' ELSE 'completed' END,
			NOW(),$10,NOW(),
			CASE WHEN $11 THEN NULL ELSE NOW() END,
			CASE WHEN $11 THEN '' ELSE 'ignored: no TellBook financial reference' END
		)
		ON CONFLICT DO NOTHING
		RETURNING id, provider, provider_event_id, body_sha256, event_type, processing_status, received_at, verified_at
	`
	var event StoredWebhookEvent
	err = tx.QueryRow(
		ctx, query, params.ID, params.Provider, params.ProviderEventID, params.BodySHA256,
		params.EventType, params.Ciphertext.Data, params.Ciphertext.Nonce,
		params.Ciphertext.KeyVersion, params.NormalizedEvent, params.VerifiedAt, params.Process,
	).Scan(
		&event.ID, &event.Provider, &event.ProviderEventID, &event.BodySHA256, &event.EventType,
		&event.ProcessingStatus, &event.ReceivedAt, &event.VerifiedAt,
	)
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		event, err = getStoredWebhookEventTx(ctx, tx, params.Provider, params.ProviderEventID, params.BodySHA256)
	}
	if err != nil {
		return StoredWebhookEvent{}, false, fmt.Errorf("insert verified webhook event: %w", err)
	}
	if created && params.Process {
		if err := enqueueFinancialJobTx(ctx, tx, FinancialJobParams{
			ID: uuid.New(), Kind: "process_provider_webhook", AggregateType: "provider_webhook_event",
			AggregateID: event.ID, DeduplicationKey: "process_provider_webhook:" + event.ID.String(),
			Payload: json.RawMessage(`{}`),
		}); err != nil {
			return StoredWebhookEvent{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return StoredWebhookEvent{}, false, fmt.Errorf("commit webhook insert: %w", err)
	}
	return event, created, nil
}

func getStoredWebhookEventTx(
	ctx context.Context,
	tx pgx.Tx,
	provider string,
	providerEventID string,
	bodyHash []byte,
) (StoredWebhookEvent, error) {
	query := `
		SELECT id, provider, provider_event_id, body_sha256, event_type, processing_status, received_at, verified_at
		FROM provider_webhook_events
		WHERE provider = $1 AND body_sha256 = $2
		LIMIT 1
	`
	args := []any{provider, bodyHash}
	if providerEventID != "" {
		query = `
			SELECT id, provider, provider_event_id, body_sha256, event_type, processing_status, received_at, verified_at
			FROM provider_webhook_events
			WHERE provider = $1 AND (provider_event_id = $2 OR body_sha256 = $3)
			ORDER BY (provider_event_id = $2) DESC
			LIMIT 1
		`
		args = []any{provider, providerEventID, bodyHash}
	}
	var event StoredWebhookEvent
	if err := tx.QueryRow(ctx, query, args...).Scan(
		&event.ID, &event.Provider, &event.ProviderEventID, &event.BodySHA256, &event.EventType,
		&event.ProcessingStatus, &event.ReceivedAt, &event.VerifiedAt,
	); err != nil {
		return StoredWebhookEvent{}, err
	}
	if !equalBytes(event.BodySHA256, bodyHash) {
		return StoredWebhookEvent{}, ErrIdempotencyConflict
	}
	return event, nil
}

func (r *LedgerRepository) BeginVerifiedWebhookProcessing(
	ctx context.Context,
	eventID uuid.UUID,
) (StoredWebhookEvent, error) {
	const query = `
		UPDATE provider_webhook_events
		SET
			processing_status = 'processing', processing_attempts = processing_attempts + 1,
			processing_error = '', processing_lease_expires_at = NOW() + INTERVAL '60 seconds'
		WHERE id = $1
		  AND (
			(processing_status IN ('pending', 'failed') AND next_attempt_at <= NOW())
			OR (processing_status = 'processing' AND processing_lease_expires_at <= NOW())
		  )
		RETURNING
			id, provider, provider_event_id, body_sha256, event_type,
			processing_status, received_at, verified_at
	`
	var event StoredWebhookEvent
	if err := r.db.QueryRow(ctx, query, eventID).Scan(
		&event.ID, &event.Provider, &event.ProviderEventID, &event.BodySHA256,
		&event.EventType, &event.ProcessingStatus, &event.ReceivedAt, &event.VerifiedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return StoredWebhookEvent{}, ErrConcurrentUpdate
	} else if err != nil {
		return StoredWebhookEvent{}, fmt.Errorf("begin verified webhook processing: %w", err)
	}
	return event, nil
}

func (r *LedgerRepository) CompleteVerifiedWebhookProcessing(
	ctx context.Context,
	eventID uuid.UUID,
	result string,
) error {
	const query = `
		UPDATE provider_webhook_events
		SET
			processing_status = 'completed', processed_at = NOW(),
			processing_result = $2, processing_error = '', processing_lease_expires_at = NULL
		WHERE id = $1 AND processing_status = 'processing'
	`
	tag, err := r.db.Exec(ctx, query, eventID, strings.TrimSpace(result))
	if err != nil {
		return fmt.Errorf("complete verified webhook processing: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (r *LedgerRepository) FailVerifiedWebhookProcessing(
	ctx context.Context,
	eventID uuid.UUID,
	retryAt time.Time,
	reason string,
) error {
	if retryAt.IsZero() {
		return errors.New("verified webhook retry time is required")
	}
	const query = `
		UPDATE provider_webhook_events
		SET
			processing_status = 'failed', next_attempt_at = $2,
			processing_error = $3, processing_lease_expires_at = NULL
		WHERE id = $1 AND processing_status = 'processing'
	`
	tag, err := r.db.Exec(ctx, query, eventID, retryAt, strings.TrimSpace(reason))
	if err != nil {
		return fmt.Errorf("fail verified webhook processing: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentUpdate
	}
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

type FinancialJob struct {
	ID               uuid.UUID
	Kind             string
	AggregateType    string
	AggregateID      uuid.UUID
	DeduplicationKey string
	Payload          json.RawMessage
	Attempts         int
	LeaseOwner       string
	LeaseExpiresAt   time.Time
}

func (r *LedgerRepository) ClaimFinancialJobs(
	ctx context.Context,
	workerID string,
	limit int,
	leaseDuration time.Duration,
) ([]FinancialJob, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || limit <= 0 || leaseDuration <= 0 {
		return nil, errors.New("worker ID, positive limit, and lease duration are required")
	}
	leaseSeconds := int64(leaseDuration / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	const query = `
		WITH candidates AS (
			SELECT id
			FROM financial_jobs
			WHERE
				(status IN ('pending', 'failed') AND available_at <= NOW())
				OR (status = 'processing' AND lease_expires_at <= NOW())
			ORDER BY available_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE financial_jobs AS job
		SET
			status = 'processing', attempts = attempts + 1,
			lease_owner = $2, lease_expires_at = NOW() + ($3 * INTERVAL '1 second'),
			updated_at = NOW()
		FROM candidates
		WHERE job.id = candidates.id
		RETURNING
			job.id, job.kind, job.aggregate_type, job.aggregate_id,
			job.deduplication_key, job.payload, job.attempts,
			job.lease_owner, job.lease_expires_at
	`
	rows, err := r.db.Query(ctx, query, limit, workerID, leaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("claim financial jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]FinancialJob, 0, limit)
	for rows.Next() {
		var job FinancialJob
		if err := rows.Scan(
			&job.ID, &job.Kind, &job.AggregateType, &job.AggregateID,
			&job.DeduplicationKey, &job.Payload, &job.Attempts,
			&job.LeaseOwner, &job.LeaseExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan financial job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate financial jobs: %w", err)
	}
	return jobs, nil
}

func (r *LedgerRepository) ClaimFinancialJobsByKind(
	ctx context.Context,
	workerID string,
	kind string,
	limit int,
	leaseDuration time.Duration,
) ([]FinancialJob, error) {
	workerID = strings.TrimSpace(workerID)
	kind = strings.TrimSpace(kind)
	if workerID == "" || kind == "" || limit <= 0 || leaseDuration <= 0 {
		return nil, errors.New("worker ID, job kind, positive limit, and lease duration are required")
	}
	leaseSeconds := int64(leaseDuration / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	const query = `
		WITH candidates AS (
			SELECT id FROM financial_jobs
			WHERE kind = $1 AND (
				(status IN ('pending', 'failed') AND available_at <= NOW())
				OR (status = 'processing' AND lease_expires_at <= NOW())
			)
			ORDER BY available_at, created_at
			FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE financial_jobs AS job
		SET status = 'processing', attempts = attempts + 1,
			lease_owner = $3, lease_expires_at = NOW() + ($4 * INTERVAL '1 second'), updated_at = NOW()
		FROM candidates WHERE job.id = candidates.id
		RETURNING job.id, job.kind, job.aggregate_type, job.aggregate_id,
			job.deduplication_key, job.payload, job.attempts, job.lease_owner, job.lease_expires_at
	`
	rows, err := r.db.Query(ctx, query, kind, limit, workerID, leaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("claim %s financial jobs: %w", kind, err)
	}
	defer rows.Close()
	jobs := make([]FinancialJob, 0, limit)
	for rows.Next() {
		var job FinancialJob
		if err := rows.Scan(
			&job.ID, &job.Kind, &job.AggregateType, &job.AggregateID,
			&job.DeduplicationKey, &job.Payload, &job.Attempts,
			&job.LeaseOwner, &job.LeaseExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan %s financial job: %w", kind, err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *LedgerRepository) ClaimCollectionWebhookJobs(
	ctx context.Context,
	workerID string,
	limit int,
	leaseDuration time.Duration,
) ([]FinancialJob, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || limit <= 0 || leaseDuration <= 0 {
		return nil, errors.New("worker ID, positive limit, and lease duration are required")
	}
	leaseSeconds := int64(leaseDuration / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	const query = `
		WITH candidates AS (
			SELECT job.id
			FROM financial_jobs job
			INNER JOIN provider_webhook_events event ON event.id = job.aggregate_id
			WHERE job.kind = 'process_provider_webhook'
			  AND job.aggregate_type = 'provider_webhook_event'
			  AND (
				LEFT(BTRIM(COALESCE(event.normalized_event->>'merchant_reference', '')), 4) IN ('pay-', 'pay_')
				OR LEFT(BTRIM(COALESCE(event.normalized_event->>'reference', '')), 4) IN ('pay-', 'pay_')
				OR LEFT(BTRIM(COALESCE(event.normalized_event->>'transaction_reference', '')), 4) IN ('pay-', 'pay_')
			  )
			  AND (
				(job.status IN ('pending', 'failed') AND job.available_at <= NOW())
				OR (job.status = 'processing' AND job.lease_expires_at <= NOW())
			  )
			ORDER BY job.available_at, job.created_at
			FOR UPDATE OF job SKIP LOCKED
			LIMIT $1
		)
		UPDATE financial_jobs AS job
		SET
			status = 'processing', attempts = attempts + 1,
			lease_owner = $2, lease_expires_at = NOW() + ($3 * INTERVAL '1 second'),
			updated_at = NOW()
		FROM candidates
		WHERE job.id = candidates.id
		RETURNING
			job.id, job.kind, job.aggregate_type, job.aggregate_id,
			job.deduplication_key, job.payload, job.attempts,
			job.lease_owner, job.lease_expires_at
	`
	rows, err := r.db.Query(ctx, query, limit, workerID, leaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("claim collection webhook jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]FinancialJob, 0, limit)
	for rows.Next() {
		var job FinancialJob
		if err := rows.Scan(
			&job.ID, &job.Kind, &job.AggregateType, &job.AggregateID,
			&job.DeduplicationKey, &job.Payload, &job.Attempts,
			&job.LeaseOwner, &job.LeaseExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan collection webhook job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collection webhook jobs: %w", err)
	}
	return jobs, nil
}

func (r *LedgerRepository) ClaimPayoutWebhookJobs(
	ctx context.Context,
	workerID string,
	limit int,
	leaseDuration time.Duration,
) ([]FinancialJob, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || limit <= 0 || leaseDuration <= 0 {
		return nil, errors.New("worker ID, positive limit, and lease duration are required")
	}
	leaseSeconds := int64(leaseDuration / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	const query = `
		WITH candidates AS (
			SELECT job.id
			FROM financial_jobs job
			INNER JOIN provider_webhook_events event ON event.id = job.aggregate_id
			WHERE job.kind = 'process_provider_webhook'
			  AND job.aggregate_type = 'provider_webhook_event'
			  AND (
				LEFT(BTRIM(COALESCE(event.normalized_event->>'merchant_reference', '')), 5) IN ('pout-', 'pout_')
				OR LEFT(BTRIM(COALESCE(event.normalized_event->>'reference', '')), 5) IN ('pout-', 'pout_')
				OR LEFT(BTRIM(COALESCE(event.normalized_event->>'transaction_reference', '')), 5) IN ('pout-', 'pout_')
			  )
			  AND (
				(job.status IN ('pending', 'failed') AND job.available_at <= NOW())
				OR (job.status = 'processing' AND job.lease_expires_at <= NOW())
			  )
			ORDER BY job.available_at, job.created_at
			FOR UPDATE OF job SKIP LOCKED
			LIMIT $1
		)
		UPDATE financial_jobs AS job
		SET
			status = 'processing', attempts = attempts + 1,
			lease_owner = $2, lease_expires_at = NOW() + ($3 * INTERVAL '1 second'),
			updated_at = NOW()
		FROM candidates
		WHERE job.id = candidates.id
		RETURNING
			job.id, job.kind, job.aggregate_type, job.aggregate_id,
			job.deduplication_key, job.payload, job.attempts,
			job.lease_owner, job.lease_expires_at
	`
	rows, err := r.db.Query(ctx, query, limit, workerID, leaseSeconds)
	if err != nil {
		return nil, fmt.Errorf("claim payout webhook jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]FinancialJob, 0, limit)
	for rows.Next() {
		var job FinancialJob
		if err := rows.Scan(
			&job.ID, &job.Kind, &job.AggregateType, &job.AggregateID,
			&job.DeduplicationKey, &job.Payload, &job.Attempts,
			&job.LeaseOwner, &job.LeaseExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan payout webhook job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate payout webhook jobs: %w", err)
	}
	return jobs, nil
}

func (r *LedgerRepository) CompleteFinancialJob(ctx context.Context, jobID uuid.UUID, workerID string) error {
	const query = `
		UPDATE financial_jobs
		SET
			status = 'completed', completed_at = NOW(), lease_owner = '',
			lease_expires_at = NULL, last_error = '', updated_at = NOW()
		WHERE id = $1 AND status = 'processing' AND lease_owner = $2
	`
	tag, err := r.db.Exec(ctx, query, jobID, strings.TrimSpace(workerID))
	if err != nil {
		return fmt.Errorf("complete financial job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

func (r *LedgerRepository) FailFinancialJob(
	ctx context.Context,
	jobID uuid.UUID,
	workerID string,
	retryAt time.Time,
	reason string,
) error {
	if retryAt.IsZero() {
		return errors.New("financial job retry time is required")
	}
	const query = `
		UPDATE financial_jobs
		SET
			status = 'failed', available_at = $3, lease_owner = '',
			lease_expires_at = NULL, last_error = $4, updated_at = NOW()
		WHERE id = $1 AND status = 'processing' AND lease_owner = $2
	`
	tag, err := r.db.Exec(ctx, query, jobID, strings.TrimSpace(workerID), retryAt, strings.TrimSpace(reason))
	if err != nil {
		return fmt.Errorf("fail financial job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}
