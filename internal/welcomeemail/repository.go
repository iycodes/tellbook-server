package welcomeemail

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxAttempts = 8

type Job struct {
	ID             uuid.UUID
	Audience       string
	RecipientEmail string
	RecipientName  string
	Subject        string
	HTMLBody       string
	TextBody       string
	AttemptCount   int
	NextAttemptAt  time.Time
	LeaseOwner     string
}

type Repository struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (r *Repository) ClaimJobs(ctx context.Context, owner string, limit int, lease time.Duration) ([]Job, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("welcome email repository is not configured")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("welcome email worker owner is required")
	}
	if limit < 1 || limit > 100 {
		limit = 32
	}
	if lease <= 0 {
		lease = 90 * time.Second
	}
	now := r.now()
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin welcome email claim: %w", err)
	}
	defer tx.Rollback(ctx)

	// A lease expiring after SMTP started is an unknown delivery outcome. Never
	// resend it automatically because that could duplicate a welcome message.
	if _, err = tx.Exec(ctx, `
		WITH expired AS (
			SELECT id FROM welcome_email_jobs
			WHERE status='processing' AND lease_expires_at<=$1
			ORDER BY lease_expires_at,id
			LIMIT 100 FOR UPDATE SKIP LOCKED
		)
		UPDATE welcome_email_jobs job
		SET status='manual_review',lease_owner='',lease_expires_at=NULL,
			last_error_code='lease_expired_outcome_unknown',updated_at=$1
		FROM expired WHERE job.id=expired.id
	`, now); err != nil {
		return nil, fmt.Errorf("fence expired welcome email claims: %w", err)
	}

	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id FROM welcome_email_jobs
			WHERE status IN ('pending','retry') AND next_attempt_at<=$1 AND attempt_count<$4
			ORDER BY next_attempt_at,created_at,id
			LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		UPDATE welcome_email_jobs job
		SET status='processing',attempt_count=job.attempt_count+1,lease_owner=$3,
			lease_expires_at=$1+($5 * INTERVAL '1 second'),updated_at=$1
		FROM due WHERE job.id=due.id
		RETURNING job.id,job.audience,job.recipient_email,job.recipient_name,
			job.subject,job.html_body,job.text_body,job.attempt_count,job.next_attempt_at,job.lease_owner
	`, now, limit, owner, maxAttempts, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim welcome email jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]Job, 0, limit)
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.Audience, &job.RecipientEmail, &job.RecipientName,
			&job.Subject, &job.HTMLBody, &job.TextBody, &job.AttemptCount,
			&job.NextAttemptAt, &job.LeaseOwner); err != nil {
			return nil, fmt.Errorf("scan welcome email job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate welcome email jobs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit welcome email claim: %w", err)
	}
	return jobs, nil
}

func (r *Repository) MarkAccepted(ctx context.Context, job Job) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE welcome_email_jobs
		SET status='accepted',accepted_at=$3,completed_at=$3,lease_owner='',lease_expires_at=NULL,
			last_error_code='',updated_at=$3
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, job.ID, job.LeaseOwner, r.now())
	if err != nil {
		return fmt.Errorf("record accepted welcome email: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("welcome email lease was lost")
	}
	return nil
}

func (r *Repository) RecordFailure(ctx context.Context, job Job, disposition mailer.TransportDisposition, code string) error {
	status := "failed"
	var nextAttemptAt any = r.now()
	var completedAt any = r.now()
	if disposition == mailer.DispositionRetryable && job.AttemptCount < maxAttempts {
		status = "retry"
		nextAttemptAt = retryAt(r.now(), job.ID, job.AttemptCount)
		completedAt = nil
	} else if disposition == mailer.DispositionAmbiguous {
		status = "manual_review"
		completedAt = nil
	}
	if disposition == mailer.DispositionRetryable && job.AttemptCount >= maxAttempts {
		code = "smtp_retry_exhausted"
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE welcome_email_jobs
		SET status=$3,next_attempt_at=$4,lease_owner='',lease_expires_at=NULL,
			last_error_code=$5,completed_at=$6,updated_at=$7
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, job.ID, job.LeaseOwner, status, nextAttemptAt, boundedCode(code), completedAt, r.now())
	if err != nil {
		return fmt.Errorf("record welcome email failure: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("welcome email lease was lost")
	}
	return nil
}

func retryAt(now time.Time, id uuid.UUID, attempt int) time.Time {
	delay := 30 * time.Second
	for i := 1; i < attempt && delay < 30*time.Minute; i++ {
		delay *= 2
	}
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write(id[:])
	jitter := time.Duration(hasher.Sum32()%1000) * time.Millisecond
	return now.Add(delay + jitter)
}

func boundedCode(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 100 {
		return value[:100]
	}
	return value
}
