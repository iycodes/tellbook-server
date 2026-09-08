package notifications

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type InAppJob struct {
	ID                    uuid.UUID
	BookingID             uuid.UUID
	ClientID              uuid.UUID
	MarketplaceCustomerID *uuid.UUID
	AudienceType          string
	ReminderOccurrenceAt  time.Time
	AttemptCount          int
	LeaseOwner            string
}

func (r *Repository) reconcileInAppTx(
	ctx context.Context,
	tx pgx.Tx,
	state bookingState,
	event *bookingEvent,
) error {
	now := r.now()
	if event != nil {
		for _, typeName := range eventNotificationTypes(state, *event) {
			if err := insertProviderLifecycleNotificationTx(ctx, tx, state, typeName); err != nil {
				return err
			}
		}
	}
	desired := make([]string, 0, 2)
	if bookingSecured(state) && state.StartAt.After(now) && !isTerminalBookingStatus(state.Status) {
		scheduledFor := state.StartAt.Add(-appointmentReminderMins * time.Minute)
		if !scheduledFor.After(now) && state.StartAt.Sub(now) >= 2*time.Hour {
			scheduledFor = now
		}
		if scheduledFor.After(now) || state.StartAt.Sub(now) >= 2*time.Hour {
			occurrenceKey := strconv.FormatInt(state.StartAt.UTC().Unix(), 10)
			if state.ProviderReminderEnabled && providerBookingStatusEligible(state.Status) {
				key := "in_app_reminder:" + state.ID.String() + ":provider:" + occurrenceKey
				desired = append(desired, key)
				if err := upsertInAppJobTx(ctx, tx, state, "provider", key, scheduledFor); err != nil {
					return err
				}
			}
			if state.Status == "confirmed" && state.MarketplaceCustomerID != nil {
				key := "in_app_reminder:" + state.ID.String() + ":customer:" + occurrenceKey
				desired = append(desired, key)
				if err := upsertInAppJobTx(ctx, tx, state, "customer", key, scheduledFor); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE notification_in_app_jobs
		SET status='cancelled',lease_owner='',lease_expires_at=NULL,
			last_error_code='reminder_no_longer_eligible',completed_at=NOW(),updated_at=NOW()
		WHERE booking_id=$1 AND status IN ('pending','retry','processing')
		  AND NOT (idempotency_key=ANY($2::text[]))
	`, state.ID, desired); err != nil {
		return fmt.Errorf("cancel stale in-app reminders: %w", err)
	}
	return nil
}

func upsertInAppJobTx(
	ctx context.Context,
	tx pgx.Tx,
	state bookingState,
	audience, key string,
	scheduledFor time.Time,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO notification_in_app_jobs (
			id,idempotency_key,client_id,marketplace_customer_id,booking_id,audience_type,
			reminder_occurrence_at,scheduled_for,status,next_attempt_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$8,NOW(),NOW())
		ON CONFLICT (idempotency_key) DO UPDATE SET
			scheduled_for=EXCLUDED.scheduled_for,next_attempt_at=EXCLUDED.next_attempt_at,
			status='pending',last_error_code='',completed_at=NULL,updated_at=NOW()
		WHERE notification_in_app_jobs.status IN ('pending','retry','cancelled','dead_letter')
	`, uuid.New(), key, state.ClientID, state.MarketplaceCustomerID, state.ID, audience,
		state.StartAt.UTC(), scheduledFor)
	if err != nil {
		return fmt.Errorf("upsert in-app reminder job: %w", err)
	}
	return nil
}

func insertProviderLifecycleNotificationTx(
	ctx context.Context,
	tx pgx.Tx,
	state bookingState,
	typeName string,
) error {
	title := map[string]string{
		"booking_rescheduled":     "Booking rescheduled",
		"booking_cancelled":       "Booking cancelled",
		"booking_expired":         "Booking expired",
		"booking_completed":       "Booking completed",
		"payment_satisfied":       "Booking payment secured",
		"payment_failed":          "Booking payment needs attention",
		"payment_refunded":        "Booking payment refunded",
		"payment_action_required": "Customer payment action required",
	}[typeName]
	if title == "" {
		return nil
	}
	key := typeName + ":" + state.ID.String() + ":" + strconv.FormatInt(state.CurrentBookingEventSequence, 10)
	_, err := tx.Exec(ctx, `
		INSERT INTO notifications (
			id,client_id,customer_id,booking_id,type,severity,title,description,
			action_label,action_route,icon_name,icon_tone,metadata,created_at,updated_at
		)
		SELECT $1,$2,booking.customer_id,booking.id,$3,'normal',$4,
			'Open the booking to review its latest state.','View booking','/bookings',
			'calendar_today','muted',jsonb_build_object('notification_planner_key',$5::text),NOW(),NOW()
		FROM bookings booking WHERE booking.id=$6
		ON CONFLICT (client_id,(metadata->>'notification_planner_key'))
		WHERE metadata ? 'notification_planner_key' DO NOTHING
	`, uuid.New(), state.ClientID, typeName, title, key, state.ID)
	if err != nil {
		return fmt.Errorf("insert provider lifecycle notification: %w", err)
	}
	return nil
}

func (r *Repository) ClaimInAppJobs(ctx context.Context, owner string, batch int, lease time.Duration) ([]InAppJob, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("in-app notification lease owner is required")
	}
	if batch < 1 || batch > 100 {
		batch = defaultClaimBatch
	}
	if lease <= 0 {
		lease = defaultLeaseDuration
	}
	rows, err := r.db.Query(ctx, `
		WITH claimable AS (
			SELECT id FROM notification_in_app_jobs
			WHERE scheduled_for<=NOW() AND status IN ('pending','retry','processing')
			  AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
			ORDER BY (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
				scheduled_for,id FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE notification_in_app_jobs job
		SET status='processing',attempt_count=attempt_count+1,lease_owner=$2,
			lease_expires_at=NOW()+($3::bigint*INTERVAL '1 millisecond'),updated_at=NOW()
		FROM claimable WHERE job.id=claimable.id
		RETURNING job.id,job.booking_id,job.client_id,job.marketplace_customer_id,
			job.audience_type,job.reminder_occurrence_at,job.attempt_count,job.lease_owner
	`, batch, owner, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim in-app notification jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]InAppJob, 0, batch)
	for rows.Next() {
		var job InAppJob
		var customerID uuid.NullUUID
		if err := rows.Scan(
			&job.ID, &job.BookingID, &job.ClientID, &customerID, &job.AudienceType,
			&job.ReminderOccurrenceAt, &job.AttemptCount, &job.LeaseOwner,
		); err != nil {
			return nil, fmt.Errorf("scan in-app notification job: %w", err)
		}
		if customerID.Valid {
			id := customerID.UUID
			job.MarketplaceCustomerID = &id
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *Repository) CompleteInAppJob(ctx context.Context, job InAppJob) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state bookingState
	var reminderEnabled bool
	if err := tx.QueryRow(ctx, `
		SELECT booking.status,booking.payment_status,booking.agreement_status,booking.start_at,
			booking.reservation_expired_at,
			(booking.agreement_template_family_id_snapshot IS NOT NULL
			 OR booking.standalone_signature_required_snapshot),
			COALESCE(preference.appointment_reminder_enabled,TRUE)
		FROM notification_in_app_jobs job
		JOIN bookings booking ON booking.id=job.booking_id
		LEFT JOIN provider_notification_preferences preference ON preference.client_id=booking.client_id
		WHERE job.id=$1 AND job.status='processing' AND job.lease_owner=$2
		FOR UPDATE OF job
	`, job.ID, job.LeaseOwner).Scan(
		&state.Status, &state.PaymentStatus, &state.AgreementStatus, &state.StartAt,
		&state.ReservationExpiredAt, &state.AgreementRequired, &reminderEnabled,
	); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("in-app notification lease was lost")
	} else if err != nil {
		return fmt.Errorf("lock in-app notification job: %w", err)
	}
	eligible := state.StartAt.Equal(job.ReminderOccurrenceAt) && state.StartAt.After(r.now()) &&
		bookingSecured(state) && !isTerminalBookingStatus(state.Status) &&
		bookingStatusReminderEligible(state.Status, job.AudienceType)
	if job.AudienceType == "provider" {
		eligible = eligible && reminderEnabled
	}
	if !eligible {
		if _, err := tx.Exec(ctx, `
			UPDATE notification_in_app_jobs SET status='cancelled',lease_owner='',lease_expires_at=NULL,
				last_error_code='reminder_no_longer_eligible',completed_at=NOW(),updated_at=NOW()
			WHERE id=$1 AND lease_owner=$2
		`, job.ID, job.LeaseOwner); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if job.AudienceType == "provider" {
		_, err = tx.Exec(ctx, `
			INSERT INTO notifications (
				id,client_id,customer_id,booking_id,type,severity,title,description,
				action_label,action_route,icon_name,icon_tone,metadata,created_at,updated_at
			)
			SELECT $1,booking.client_id,booking.customer_id,booking.id,'appointment_reminder','normal',
				'Appointment reminder','An upcoming appointment is approaching.',
				'View booking','/bookings','calendar_today','muted',
				jsonb_build_object('notification_planner_key',$2::text),NOW(),NOW()
			FROM bookings booking WHERE booking.id=$3
			ON CONFLICT (client_id,(metadata->>'notification_planner_key'))
			WHERE metadata ? 'notification_planner_key' DO NOTHING
		`, uuid.New(), "in_app_reminder:"+job.ID.String(), job.BookingID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO marketplace_notifications (
				marketplace_customer_id,kind,event_type,event_key,title,body,
				provider_id,booking_id,created_at
			) SELECT $1,'booking','appointment_reminder',$2,'Appointment reminder',
				'Your appointment is approaching.',booking.client_id,booking.id,NOW()
			FROM bookings booking WHERE booking.id=$3
			ON CONFLICT (marketplace_customer_id,event_key) DO NOTHING
		`, job.MarketplaceCustomerID, "in_app_reminder:"+job.ID.String(), job.BookingID)
	}
	if err != nil {
		return fmt.Errorf("insert in-app reminder: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE notification_in_app_jobs SET status='completed',lease_owner='',lease_expires_at=NULL,
			last_error_code='',completed_at=NOW(),updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, job.ID, job.LeaseOwner); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) FailInAppJob(ctx context.Context, job InAppJob, code string) error {
	status := "retry"
	var completedAt any
	if job.AttemptCount >= maxPlanningAttempts {
		status = "dead_letter"
		completedAt = r.now()
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE notification_in_app_jobs
		SET status=$3,next_attempt_at=$4,lease_owner='',lease_expires_at=NULL,
			last_error_code=$5,completed_at=$6,updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, job.ID, job.LeaseOwner, status, r.now().Add(retryDelay(job.AttemptCount)),
		boundedCode(code), completedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("in-app notification lease was lost")
	}
	return nil
}

func bookingStatusReminderEligible(status, audience string) bool {
	if audience == "customer" {
		return status == "confirmed"
	}
	return providerBookingStatusEligible(status)
}
