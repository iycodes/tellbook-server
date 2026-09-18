package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultClaimBatch       = 32
	defaultLeaseDuration    = 90 * time.Second
	maxPlanningAttempts     = 10
	appointmentReminderMins = 24 * 60
)

var ErrDispatchNotAuthorized = errors.New("notification dispatch is no longer authorized")

type Repository struct {
	db               *pgxpool.Pool
	destinationKey   []byte
	enabledTemplates map[whatsapp.TemplateKey]struct{}
	additionalEmails bool
	emailEnabled     bool
	whatsAppEnabled  bool
	now              func() time.Time
}

func NewRepository(
	db *pgxpool.Pool,
	destinationHMACKey string,
	enabledTemplateKeys []string,
	emailEnabled bool,
	whatsAppEnabled bool,
) (*Repository, error) {
	if db == nil || len(destinationHMACKey) < 32 {
		return nil, errors.New("notification planner repository is unavailable")
	}
	if err := whatsapp.ValidateEnabledTemplateKeys(enabledTemplateKeys); err != nil {
		return nil, err
	}
	enabled := make(map[whatsapp.TemplateKey]struct{}, len(enabledTemplateKeys))
	for _, raw := range enabledTemplateKeys {
		key := whatsapp.TemplateKey(strings.TrimSpace(raw))
		if _, ok := whatsapp.LookupTemplate(key); !ok {
			return nil, fmt.Errorf("unknown enabled notification template %q", raw)
		}
		enabled[key] = struct{}{}
	}
	return &Repository{
		db: db, destinationKey: []byte(destinationHMACKey), enabledTemplates: enabled,
		emailEnabled: emailEnabled, whatsAppEnabled: whatsAppEnabled,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (r *Repository) WithAdditionalEmails(enabled bool) *Repository {
	r.additionalEmails = enabled && r.emailEnabled
	return r
}

type EventJob struct {
	BookingEventID uuid.UUID
	EventSequence  int64
	Origin         string
	AttemptCount   int
	LeaseOwner     string
}

type ScopeJob struct {
	ClientID           uuid.UUID
	PreferenceRevision int64
	AttemptCount       int
	LeaseOwner         string
}

type Delivery struct {
	ID                   uuid.UUID
	BookingID            uuid.UUID
	BookingEventID       *uuid.UUID
	BookingEventSequence int64
	AudienceType         string
	Channel              string
	NotificationType     string
	TemplateKey          string
	ReminderOccurrenceAt *time.Time
	ScheduledFor         time.Time
	DestinationHMAC      []byte
	PreferenceRevision   int64
	AttemptCount         int
	LeaseOwner           string
}

func (r *Repository) templateEnabled(key whatsapp.TemplateKey) bool {
	_, ok := r.enabledTemplates[key]
	return ok
}

func (r *Repository) destinationFingerprint(channel, destination string) []byte {
	mac := hmac.New(sha256.New, r.destinationKey)
	_, _ = mac.Write([]byte(channel))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strings.ToLower(strings.TrimSpace(destination))))
	return mac.Sum(nil)
}

func (r *Repository) ClaimEventJobs(ctx context.Context, owner string, batch int, lease time.Duration) ([]EventJob, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("notification event lease owner is required")
	}
	if batch < 1 || batch > 100 {
		batch = defaultClaimBatch
	}
	if lease <= 0 {
		lease = defaultLeaseDuration
	}
	rows, err := r.db.Query(ctx, `
		WITH claimable AS (
			SELECT booking_event_id
			FROM notification_event_jobs
			WHERE status IN ('pending','retry','processing')
			  AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
			ORDER BY (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
				event_sequence,booking_event_id
			FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE notification_event_jobs job
		SET status='processing',attempt_count=attempt_count+1,lease_owner=$2,
			lease_expires_at=NOW()+($3::bigint*INTERVAL '1 millisecond'),updated_at=NOW()
		FROM claimable WHERE job.booking_event_id=claimable.booking_event_id
		RETURNING job.booking_event_id,job.event_sequence,job.origin,job.attempt_count,job.lease_owner
	`, batch, owner, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim notification event jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]EventJob, 0, batch)
	for rows.Next() {
		var job EventJob
		if err := rows.Scan(
			&job.BookingEventID, &job.EventSequence, &job.Origin, &job.AttemptCount, &job.LeaseOwner,
		); err != nil {
			return nil, fmt.Errorf("scan notification event job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *Repository) ClaimScopeJobs(ctx context.Context, owner string, batch int, lease time.Duration) ([]ScopeJob, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("notification scope lease owner is required")
	}
	if batch < 1 || batch > 20 {
		batch = 4
	}
	if lease <= 0 {
		lease = defaultLeaseDuration
	}
	rows, err := r.db.Query(ctx, `
		WITH claimable AS (
			SELECT client_id,preference_revision
			FROM notification_scope_replan_jobs
			WHERE status IN ('pending','retry','processing')
			  AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
			ORDER BY (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
				created_at,client_id,preference_revision
			FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE notification_scope_replan_jobs job
		SET status='processing',attempt_count=attempt_count+1,lease_owner=$2,
			lease_expires_at=NOW()+($3::bigint*INTERVAL '1 millisecond'),updated_at=NOW()
		FROM claimable
		WHERE job.client_id=claimable.client_id
		  AND job.preference_revision=claimable.preference_revision
		RETURNING job.client_id,job.preference_revision,job.attempt_count,job.lease_owner
	`, batch, owner, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim notification scope jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]ScopeJob, 0, batch)
	for rows.Next() {
		var job ScopeJob
		if err := rows.Scan(&job.ClientID, &job.PreferenceRevision, &job.AttemptCount, &job.LeaseOwner); err != nil {
			return nil, fmt.Errorf("scan notification scope job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func completeEventJobTx(ctx context.Context, tx pgx.Tx, job EventJob) error {
	tag, err := tx.Exec(ctx, `
		UPDATE notification_event_jobs SET status='completed',lease_owner='',lease_expires_at=NULL,
			last_error_code='',completed_at=NOW(),updated_at=NOW()
		WHERE booking_event_id=$1 AND status='processing' AND lease_owner=$2
	`, job.BookingEventID, job.LeaseOwner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("notification event lease was lost")
	}
	return nil
}

func (r *Repository) FailEventJob(ctx context.Context, job EventJob, code string) error {
	return r.failPlanningJob(ctx, "notification_event_jobs", job.BookingEventID, uuid.Nil,
		0, job.LeaseOwner, job.AttemptCount, code)
}

func (r *Repository) FailScopeJob(ctx context.Context, job ScopeJob, code string) error {
	return r.failPlanningJob(ctx, "notification_scope_replan_jobs", uuid.Nil, job.ClientID,
		job.PreferenceRevision, job.LeaseOwner, job.AttemptCount, code)
}

func (r *Repository) failPlanningJob(
	ctx context.Context,
	table string,
	eventID, clientID uuid.UUID,
	revision int64,
	owner string,
	attempt int,
	code string,
) error {
	code = boundedCode(code)
	status := "retry"
	var completedAt any
	if attempt >= maxPlanningAttempts {
		status = "dead_letter"
		completedAt = r.now()
	}
	delay := retryDelay(attempt)
	var tag pgxTag
	var err error
	if table == "notification_event_jobs" {
		tag, err = r.db.Exec(ctx, `
			UPDATE notification_event_jobs SET status=$3,next_attempt_at=$4,lease_owner='',
				lease_expires_at=NULL,last_error_code=$5,completed_at=$6,updated_at=NOW()
			WHERE booking_event_id=$1 AND status='processing' AND lease_owner=$2
		`, eventID, owner, status, r.now().Add(delay), code, completedAt)
	} else {
		tag, err = r.db.Exec(ctx, `
			UPDATE notification_scope_replan_jobs SET status=$4,next_attempt_at=$5,lease_owner='',
				lease_expires_at=NULL,last_error_code=$6,completed_at=$7,updated_at=NOW()
			WHERE client_id=$1 AND preference_revision=$2 AND status='processing' AND lease_owner=$3
		`, clientID, revision, owner, status, r.now().Add(delay), code, completedAt)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("notification planning lease was lost")
	}
	return nil
}

type pgxTag interface{ RowsAffected() int64 }

func boundedCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "planning_failed"
	}
	if len(value) > 80 {
		return value[:80]
	}
	return value
}

func retryDelay(attempt int) time.Duration {
	shift := min(max(attempt-1, 0), 7)
	delay := 5 * time.Second * time.Duration(1<<shift)
	if delay > 10*time.Minute {
		return 10 * time.Minute
	}
	return delay
}
