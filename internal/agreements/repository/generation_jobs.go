package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	"booking/go-server/internal/agreements/render"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CreateGenerationDraftParams struct {
	ClientID           uuid.UUID
	Title              string
	Description        string
	Category           string
	Tags               []string
	ConfirmationMethod domain.ConfirmationMethod
	InputKind          domain.GenerationInputKind
	Input              any
}

type CreatedGenerationDraft struct {
	FamilyID  uuid.UUID
	VersionID uuid.UUID
	JobID     uuid.UUID
}

type CompleteGenerationJobParams struct {
	JobID                uuid.UUID
	WorkerID             string
	SuggestedTitle       string
	SuggestedDescription string
	Document             aiapi.DocumentSchema
	Warnings             []aiapi.Warning
}

func (r *Repository) CreateGenerationDraft(ctx context.Context, params CreateGenerationDraftParams) (CreatedGenerationDraft, error) {
	if r == nil || r.db == nil || params.ClientID == uuid.Nil {
		return CreatedGenerationDraft{}, errors.New("invalid agreement generation repository")
	}
	params.Title = strings.TrimSpace(params.Title)
	params.Description = strings.TrimSpace(params.Description)
	params.Category = strings.TrimSpace(params.Category)
	if params.Title == "" || params.Category == "" {
		return CreatedGenerationDraft{}, errors.New("agreement title and category are required")
	}
	if _, err := domain.ParseConfirmationMethod(string(params.ConfirmationMethod)); err != nil {
		return CreatedGenerationDraft{}, err
	}
	inputJSON, err := domain.EncodeGenerationInput(params.InputKind, params.Input)
	if err != nil {
		return CreatedGenerationDraft{}, err
	}
	sourceKind := domain.TemplateSourceAI
	sourcePDFKey := ""
	sourcePDFFileName := ""
	if params.InputKind == domain.GenerationInputUpload {
		sourceKind = domain.TemplateSourceUpload
		upload := params.Input.(domain.UploadGenerationInput)
		sourcePDFKey = strings.TrimSpace(upload.SourcePDFR2Key)
		sourcePDFFileName = strings.TrimSpace(upload.SourcePDFFileName)
	}

	created := CreatedGenerationDraft{FamilyID: uuid.New(), VersionID: uuid.New(), JobID: uuid.New()}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreatedGenerationDraft{}, fmt.Errorf("begin agreement generation draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_families (
			id, client_id, owner_type, title, description, category, tags,
			confirmation_method, status, created_by_client_id, created_at, updated_at
		)
		SELECT $1, c.id, 'client', $3, $4, $5, $6, $7, 'draft', c.id, NOW(), NOW()
		FROM clients c
		WHERE c.id = $2
	`, created.FamilyID, params.ClientID, params.Title, params.Description, params.Category, normalizeTags(params.Tags), params.ConfirmationMethod); err != nil {
		return CreatedGenerationDraft{}, fmt.Errorf("insert agreement template family: %w", err)
	}

	command, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_versions (
			id, family_id, version_number, state, document_schema, used_variable_keys,
			schema_version, renderer_version, source_kind, source_pdf_r2_key,
			source_pdf_file_name, template_schema_hash, review_warnings, revision,
			created_by_client_id, created_at, updated_at
		)
		SELECT $1, f.id, 1, 'draft', NULL, '{}', $3, $4, $5, $6, $7, '', '[]', 1,
		       $2, NOW(), NOW()
		FROM agreement_template_families f
		WHERE f.id = $8 AND f.client_id = $2 AND f.owner_type = 'client'
	`, created.VersionID, params.ClientID, aiapi.AgreementDocumentSchemaVersion, render.RendererVersion, sourceKind, sourcePDFKey, sourcePDFFileName, created.FamilyID)
	if err != nil {
		return CreatedGenerationDraft{}, fmt.Errorf("insert agreement template version: %w", err)
	}
	if command.RowsAffected() != 1 {
		return CreatedGenerationDraft{}, ErrNotFound
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_template_generation_jobs (
			id, client_id, family_id, version_id, input_kind, input_json, status,
			attempt_count, max_attempts, run_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'queued', 0, 3, NOW(), NOW(), NOW())
	`, created.JobID, params.ClientID, created.FamilyID, created.VersionID, params.InputKind, inputJSON); err != nil {
		return CreatedGenerationDraft{}, fmt.Errorf("insert agreement template generation job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedGenerationDraft{}, fmt.Errorf("commit agreement generation draft: %w", err)
	}
	return created, nil
}

func (r *Repository) ClaimGenerationJobs(
	ctx context.Context,
	workerID string,
	limit int,
	leaseDuration time.Duration,
) ([]domain.TemplateGenerationJob, error) {
	workerID = strings.TrimSpace(workerID)
	if r == nil || r.db == nil || workerID == "" || limit <= 0 || leaseDuration <= 0 {
		return nil, errors.New("invalid agreement generation job claim")
	}
	if limit > 25 {
		limit = 25
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin generation job claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE agreement_template_generation_jobs
		SET status = 'failed', lease_owner = '', lease_expires_at = NULL,
		    error_code = 'attempts_exhausted',
		    error_message = 'Automatic generation attempts were exhausted.',
		    completed_at = NOW(), updated_at = NOW()
		WHERE status = 'processing'
		  AND lease_expires_at <= NOW()
		  AND attempt_count >= max_attempts
	`); err != nil {
		return nil, fmt.Errorf("expire exhausted generation jobs: %w", err)
	}

	rows, err := tx.Query(ctx, `
		WITH candidates AS (
			SELECT id
			FROM agreement_template_generation_jobs
			WHERE attempt_count < max_attempts
			  AND run_at <= NOW()
			  AND (
				status = 'queued'
				OR (status = 'processing' AND lease_expires_at <= NOW())
			  )
			ORDER BY run_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE agreement_template_generation_jobs j
		SET status = 'processing', attempt_count = j.attempt_count + 1,
		    lease_owner = $2, lease_expires_at = NOW() + $3::interval,
		    started_at = COALESCE(j.started_at, NOW()),
		    error_code = '', error_message = '', updated_at = NOW()
		FROM candidates c
		WHERE j.id = c.id
		RETURNING j.id, j.client_id, j.family_id, j.version_id, j.input_kind,
		          (SELECT f.confirmation_method FROM agreement_template_families f WHERE f.id = j.family_id),
		          j.input_json, j.status, j.attempt_count, j.max_attempts, j.run_at,
		          j.lease_owner, j.lease_expires_at, j.error_code, j.error_message,
		          j.created_at, j.started_at, j.completed_at, j.updated_at
	`, limit, workerID, intervalLiteral(leaseDuration))
	if err != nil {
		return nil, fmt.Errorf("claim agreement generation jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]domain.TemplateGenerationJob, 0, limit)
	for rows.Next() {
		job, err := scanGenerationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agreement generation jobs: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit generation job claim: %w", err)
	}
	return jobs, nil
}

func (r *Repository) CompleteGenerationJob(ctx context.Context, params CompleteGenerationJobParams) error {
	if r == nil || r.db == nil || params.JobID == uuid.Nil || strings.TrimSpace(params.WorkerID) == "" {
		return errors.New("invalid generation job completion")
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin generation job completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var versionID, familyID uuid.UUID
	var methodText, statusText, leaseOwner string
	var leaseExpiresAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT j.version_id, j.family_id, f.confirmation_method, j.status, j.lease_owner, j.lease_expires_at
		FROM agreement_template_generation_jobs j
		JOIN agreement_template_families f ON f.id = j.family_id AND f.client_id = j.client_id
		JOIN agreement_template_versions v ON v.id = j.version_id AND v.family_id = f.id
		WHERE j.id = $1
		FOR UPDATE OF j, f, v
	`, params.JobID).Scan(&versionID, &familyID, &methodText, &statusText, &leaseOwner, &leaseExpiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock agreement generation job: %w", err)
	}
	if statusText != string(domain.JobStatusProcessing) || leaseOwner != strings.TrimSpace(params.WorkerID) ||
		leaseExpiresAt == nil || !leaseExpiresAt.After(time.Now().UTC()) {
		return ErrLeaseLost
	}
	method, err := domain.ParseConfirmationMethod(methodText)
	if err != nil {
		return err
	}
	if err := domain.ValidateDocument(params.Document, method, domain.AgreementVariableKeySet()); err != nil {
		return fmt.Errorf("validate generated agreement before storage: %w", err)
	}
	hash, err := domain.TemplateSchemaHash(params.Document, method)
	if err != nil {
		return err
	}
	documentJSON, err := json.Marshal(params.Document)
	if err != nil {
		return fmt.Errorf("encode generated agreement document: %w", err)
	}
	warnings := params.Warnings
	if warnings == nil {
		warnings = []aiapi.Warning{}
	}
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return fmt.Errorf("encode agreement review warnings: %w", err)
	}

	command, err := tx.Exec(ctx, `
		UPDATE agreement_template_versions
		SET document_schema = $2, used_variable_keys = $3, template_schema_hash = $4,
		    review_warnings = $5, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND family_id = $6 AND state = 'draft'
	`, versionID, documentJSON, params.Document.VariableKeys(), hash, warningsJSON, familyID)
	if err != nil {
		return fmt.Errorf("store generated agreement document: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	if title := strings.TrimSpace(params.SuggestedTitle); title != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE agreement_template_families
			SET title = $2, description = CASE WHEN $3 = '' THEN description ELSE $3 END, updated_at = NOW()
			WHERE id = $1 AND status = 'draft'
		`, familyID, title, strings.TrimSpace(params.SuggestedDescription)); err != nil {
			return fmt.Errorf("update generated agreement metadata: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_template_generation_jobs
		SET status = 'completed', lease_owner = '', lease_expires_at = NULL,
		    error_code = '', error_message = '', completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, params.JobID); err != nil {
		return fmt.Errorf("complete agreement generation job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit agreement generation job: %w", err)
	}
	return nil
}

func (r *Repository) FailGenerationJob(
	ctx context.Context,
	jobID uuid.UUID,
	workerID,
	errorCode,
	errorMessage string,
	retryAt time.Time,
	permanent bool,
) error {
	if r == nil || r.db == nil || jobID == uuid.Nil || strings.TrimSpace(workerID) == "" {
		return errors.New("invalid generation job failure")
	}
	command, err := r.db.Exec(ctx, `
		UPDATE agreement_template_generation_jobs
		SET status = CASE WHEN $6 OR attempt_count >= max_attempts THEN 'failed' ELSE 'queued' END,
		    run_at = CASE WHEN $6 OR attempt_count >= max_attempts THEN run_at ELSE $5 END,
		    lease_owner = '', lease_expires_at = NULL,
		    error_code = $3, error_message = $4,
		    completed_at = CASE WHEN $6 OR attempt_count >= max_attempts THEN NOW() ELSE NULL END,
		    updated_at = NOW()
		WHERE id = $1 AND status = 'processing' AND lease_owner = $2
	`, jobID, strings.TrimSpace(workerID), boundedError(errorCode, 80), boundedError(errorMessage, 500), retryAt.UTC(), permanent)
	if err != nil {
		return fmt.Errorf("fail agreement generation job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (r *Repository) RetryGenerationJob(ctx context.Context, clientID, jobID uuid.UUID) error {
	if r == nil || r.db == nil || clientID == uuid.Nil || jobID == uuid.Nil {
		return errors.New("invalid generation job retry")
	}
	command, err := r.db.Exec(ctx, `
		UPDATE agreement_template_generation_jobs
		SET status = 'queued', attempt_count = 0, run_at = NOW(), lease_owner = '',
		    lease_expires_at = NULL, error_code = '', error_message = '',
		    started_at = NULL, completed_at = NULL, updated_at = NOW()
		WHERE id = $1 AND client_id = $2 AND status = 'failed'
	`, jobID, clientID)
	if err != nil {
		return fmt.Errorf("retry agreement generation job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func (r *Repository) GetGenerationJob(ctx context.Context, clientID, jobID uuid.UUID) (domain.TemplateGenerationJob, error) {
	if r == nil || r.db == nil || clientID == uuid.Nil || jobID == uuid.Nil {
		return domain.TemplateGenerationJob{}, errors.New("invalid generation job lookup")
	}
	job, err := scanGenerationJob(r.db.QueryRow(ctx, `
		SELECT j.id, j.client_id, j.family_id, j.version_id, j.input_kind,
		       f.confirmation_method, j.input_json, j.status, j.attempt_count,
		       j.max_attempts, j.run_at, j.lease_owner, j.lease_expires_at,
		       j.error_code, j.error_message, j.created_at, j.started_at,
		       j.completed_at, j.updated_at
		FROM agreement_template_generation_jobs j
		JOIN agreement_template_families f
		  ON f.id = j.family_id AND f.client_id = j.client_id AND f.owner_type = 'client'
		WHERE j.id = $1 AND j.client_id = $2
	`, jobID, clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TemplateGenerationJob{}, ErrNotFound
	}
	return job, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanGenerationJob(row rowScanner) (domain.TemplateGenerationJob, error) {
	var job domain.TemplateGenerationJob
	var inputKind, confirmationMethod, status string
	if err := row.Scan(
		&job.ID, &job.ClientID, &job.FamilyID, &job.VersionID, &inputKind, &confirmationMethod,
		&job.InputJSON, &status, &job.AttemptCount, &job.MaxAttempts, &job.RunAt,
		&job.LeaseOwner, &job.LeaseExpiresAt, &job.ErrorCode, &job.ErrorMessage,
		&job.CreatedAt, &job.StartedAt, &job.CompletedAt, &job.UpdatedAt,
	); err != nil {
		return domain.TemplateGenerationJob{}, fmt.Errorf("scan agreement generation job: %w", err)
	}
	parsedKind, err := domain.ParseGenerationInputKind(inputKind)
	if err != nil {
		return domain.TemplateGenerationJob{}, err
	}
	parsedStatus, err := domain.ParseJobStatus(status)
	if err != nil {
		return domain.TemplateGenerationJob{}, err
	}
	job.InputKind = parsedKind
	parsedMethod, err := domain.ParseConfirmationMethod(confirmationMethod)
	if err != nil {
		return domain.TemplateGenerationJob{}, err
	}
	job.ConfirmationMethod = parsedMethod
	job.Status = parsedStatus
	return job, nil
}

func normalizeTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, tag)
	}
	return result
}

func intervalLiteral(duration time.Duration) string {
	return fmt.Sprintf("%f seconds", duration.Seconds())
}

func boundedError(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
