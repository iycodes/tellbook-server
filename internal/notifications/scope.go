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
		SELECT latest.id,booking.start_at,
			booking.id,booking.client_id,booking.marketplace_customer_id,
			booking.status,booking.payment_status,booking.agreement_status,booking.start_at,
			booking.reservation_expired_at,
			(booking.agreement_template_family_id_snapshot IS NOT NULL
			 OR booking.standalone_signature_required_snapshot),
			COALESCE(booking.customer_email_snapshot,''),
			COALESCE(booking.customer_whatsapp_e164_snapshot,''),
			booking.email_reminder_consent,booking.whatsapp_consent,
			booking.notification_consent_policy_revision,
			COALESCE(customer_preference.booking_email,TRUE),
			COALESCE(
				customer_preference.booking_email OR customer_preference.updated_at<=booking.created_at,
				TRUE
			),
			COALESCE(
				customer_preference.booking_whatsapp OR customer_preference.updated_at<=booking.created_at,
				TRUE
			),
			COALESCE(lower(btrim(client.email)),''),client.email_verified_at IS NOT NULL,
			COALESCE(provider_preference.booking_email,TRUE),
			COALESCE(provider_preference.booking_whatsapp,FALSE),
			COALESCE(provider_preference.appointment_reminder_enabled,TRUE),
			COALESCE(provider_preference.appointment_reminder_minutes,1440),
			COALESCE(provider_preference.whatsapp_e164,''),
			provider_preference.whatsapp_verified_at IS NOT NULL,
			COALESCE(provider_preference.preference_revision,1),latest.sequence,COALESCE(profile.booking_contact_phone,'')
		FROM bookings booking
		JOIN LATERAL (
			SELECT event.id,event.sequence FROM booking_domain_events event
			WHERE event.booking_id=booking.id ORDER BY event.sequence DESC LIMIT 1
		) latest ON TRUE
		JOIN clients client ON client.id=booking.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=booking.client_id
		LEFT JOIN provider_notification_preferences provider_preference
			ON provider_preference.client_id=booking.client_id
		LEFT JOIN marketplace_notification_preferences customer_preference
			ON customer_preference.marketplace_customer_id=booking.marketplace_customer_id
		WHERE booking.client_id=$1 AND booking.start_at>NOW()
		  AND ($2::timestamptz IS NULL OR (booking.start_at,booking.id)>($2,$3::uuid))
		ORDER BY booking.start_at,booking.id LIMIT $4
	`, job.ClientID, cursorStart, cursorID, scopeBookingBatch+1)
	if err != nil {
		return fmt.Errorf("load notification scope bookings: %w", err)
	}
	type scopeBooking struct {
		eventID uuid.UUID
		startAt time.Time
		state   bookingState
	}
	bookings := make([]scopeBooking, 0, scopeBookingBatch+1)
	for rows.Next() {
		var booking scopeBooking
		var marketplaceCustomerID uuid.NullUUID
		targets := []any{&booking.eventID, &booking.startAt}
		targets = append(targets, bookingStateScanTargets(&booking.state, &marketplaceCustomerID)...)
		if err := rows.Scan(targets...); err != nil {
			rows.Close()
			return fmt.Errorf("scan notification scope booking: %w", err)
		}
		setMarketplaceCustomerID(&booking.state, marketplaceCustomerID)
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
	suppressionCache := make(map[string]bool)
	for _, booking := range bookings {
		if err := r.reconcileBookingTx(ctx, tx, booking.state, nil, false, suppressionCache); err != nil {
			return err
		}
	}
	if hasMore {
		last := bookings[len(bookings)-1]
		if _, err := tx.Exec(ctx, `
			UPDATE notification_scope_replan_jobs
			SET status='pending',next_attempt_at=NOW(),lease_owner='',lease_expires_at=NULL,
				booking_cursor_start_at=$4,booking_cursor_id=$5,last_error_code='',updated_at=NOW()
			WHERE client_id=$1 AND preference_revision=$2 AND lease_owner=$3
		`, job.ClientID, job.PreferenceRevision, job.LeaseOwner, last.startAt, last.state.ID); err != nil {
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
