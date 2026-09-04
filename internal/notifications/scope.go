package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const scopeBookingBatch = 100

func (r *Repository) PlanScopeJob(ctx context.Context, job ScopeJob) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin notification scope planning: %w", err)
	}
	defer tx.Rollback(ctx)
	var cursorStart *time.Time
	var cursorID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT booking_cursor_start_at,booking_cursor_id
		FROM notification_scope_replan_jobs
		WHERE client_id=$1 AND preference_revision=$2
		  AND status='processing' AND lease_owner=$3
		FOR UPDATE
	`, job.ClientID, job.PreferenceRevision, job.LeaseOwner).Scan(&cursorStart, &cursorID); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("notification scope lease was lost")
	} else if err != nil {
		return fmt.Errorf("lock notification scope job: %w", err)
	}
	var currentRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(preference_revision,1)
		FROM provider_notification_preferences WHERE client_id=$1
	`, job.ClientID).Scan(&currentRevision); errors.Is(err, pgx.ErrNoRows) {
		currentRevision = 1
	} else if err != nil {
		return fmt.Errorf("load current provider notification revision: %w", err)
	}
	if currentRevision > job.PreferenceRevision {
		if _, err := tx.Exec(ctx, `
			UPDATE notification_scope_replan_jobs
			SET status='superseded',lease_owner='',lease_expires_at=NULL,
				completed_at=NOW(),last_error_code='',updated_at=NOW()
			WHERE client_id=$1 AND preference_revision=$2 AND lease_owner=$3
		`, job.ClientID, job.PreferenceRevision, job.LeaseOwner); err != nil {
			return fmt.Errorf("supersede notification scope job: %w", err)
		}
		return tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `
		SELECT booking.id,latest.id
		FROM bookings booking
		JOIN LATERAL (
			SELECT event.id FROM booking_domain_events event
			WHERE event.booking_id=booking.id ORDER BY event.sequence DESC LIMIT 1
		) latest ON TRUE
		WHERE booking.client_id=$1 AND booking.start_at>NOW()
		  AND ($2::timestamptz IS NULL OR (booking.start_at,booking.id)>($2,$3::uuid))
		ORDER BY booking.start_at,booking.id LIMIT $4
	`, job.ClientID, cursorStart, cursorID, scopeBookingBatch+1)
	if err != nil {
		return fmt.Errorf("load notification scope bookings: %w", err)
	}
	type scopeBooking struct{ bookingID, eventID uuid.UUID }
	bookings := make([]scopeBooking, 0, scopeBookingBatch+1)
	for rows.Next() {
		var booking scopeBooking
		if err := rows.Scan(&booking.bookingID, &booking.eventID); err != nil {
			rows.Close()
			return fmt.Errorf("scan notification scope booking: %w", err)
		}
		bookings = append(bookings, booking)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate notification scope bookings: %w", err)
	}
	rows.Close()
	hasMore := len(bookings) > scopeBookingBatch
	if hasMore {
		bookings = bookings[:scopeBookingBatch]
	}
	for _, booking := range bookings {
		state, err := loadBookingStateTx(ctx, tx, booking.eventID)
		if err != nil {
			return err
		}
		if err := r.reconcileBookingTx(ctx, tx, state, nil); err != nil {
			return err
		}
	}
	if hasMore {
		last := bookings[len(bookings)-1]
		var startAt time.Time
		if err := tx.QueryRow(ctx, `SELECT start_at FROM bookings WHERE id=$1`, last.bookingID).Scan(&startAt); err != nil {
			return fmt.Errorf("load notification scope cursor: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE notification_scope_replan_jobs
			SET status='pending',next_attempt_at=NOW(),lease_owner='',lease_expires_at=NULL,
				booking_cursor_start_at=$4,booking_cursor_id=$5,last_error_code='',updated_at=NOW()
			WHERE client_id=$1 AND preference_revision=$2 AND lease_owner=$3
		`, job.ClientID, job.PreferenceRevision, job.LeaseOwner, startAt, last.bookingID); err != nil {
			return fmt.Errorf("advance notification scope cursor: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE notification_scope_replan_jobs
			SET status='completed',lease_owner='',lease_expires_at=NULL,
				last_error_code='',completed_at=NOW(),updated_at=NOW()
			WHERE client_id=$1 AND preference_revision=$2 AND lease_owner=$3
		`, job.ClientID, job.PreferenceRevision, job.LeaseOwner); err != nil {
			return fmt.Errorf("complete notification scope job: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit notification scope planning: %w", err)
	}
	return nil
}
