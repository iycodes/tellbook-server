package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) ClaimDeliveries(
	ctx context.Context,
	channel, owner string,
	batch int,
	lease time.Duration,
) ([]Delivery, error) {
	channel = strings.TrimSpace(channel)
	owner = strings.TrimSpace(owner)
	if (channel != "email" && channel != "whatsapp") || owner == "" {
		return nil, errors.New("valid notification channel and lease owner are required")
	}
	if batch < 1 || batch > 100 {
		batch = defaultClaimBatch
	}
	if lease <= 0 {
		lease = defaultLeaseDuration
	}
	rows, err := r.db.Query(ctx, `
		WITH claimable AS (
			SELECT id FROM notification_deliveries
			WHERE channel=$1 AND scheduled_for<=NOW()
			  AND status IN ('pending','retry','processing')
			  AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
			ORDER BY (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
				scheduled_for,id
			FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE notification_deliveries delivery
		SET status='processing',attempt_count=attempt_count+1,lease_owner=$3,
			lease_expires_at=NOW()+($4::bigint*INTERVAL '1 millisecond'),updated_at=NOW()
		FROM claimable WHERE delivery.id=claimable.id
		RETURNING delivery.id,delivery.booking_id,delivery.booking_event_id,
			delivery.booking_event_sequence,delivery.audience_type,delivery.channel,
			delivery.notification_type,delivery.template_key,delivery.reminder_occurrence_at,
			delivery.scheduled_for,delivery.destination_hmac,delivery.preference_revision,
			delivery.attempt_count,delivery.lease_owner
	`, channel, batch, owner, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim notification deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := make([]Delivery, 0, batch)
	for rows.Next() {
		var item Delivery
		var eventID uuid.NullUUID
		if err := rows.Scan(
			&item.ID, &item.BookingID, &eventID, &item.BookingEventSequence,
			&item.AudienceType, &item.Channel, &item.NotificationType, &item.TemplateKey,
			&item.ReminderOccurrenceAt, &item.ScheduledFor, &item.DestinationHMAC,
			&item.PreferenceRevision, &item.AttemptCount, &item.LeaseOwner,
		); err != nil {
			return nil, fmt.Errorf("scan notification delivery: %w", err)
		}
		if eventID.Valid {
			id := eventID.UUID
			item.BookingEventID = &id
		}
		deliveries = append(deliveries, item)
	}
	return deliveries, rows.Err()
}

func (r *Repository) AuthorizeDispatch(ctx context.Context, delivery Delivery) (Delivery, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Delivery{}, fmt.Errorf("begin notification dispatch fence: %w", err)
	}
	defer tx.Rollback(ctx)
	var current Delivery
	var eventID uuid.NullUUID
	if err := tx.QueryRow(ctx, `
		SELECT id,booking_id,booking_event_id,booking_event_sequence,audience_type,channel,
			notification_type,template_key,reminder_occurrence_at,scheduled_for,
			destination_hmac,preference_revision,attempt_count,lease_owner
		FROM notification_deliveries
		WHERE id=$1 AND status='processing' AND lease_owner=$2
		FOR UPDATE
	`, delivery.ID, delivery.LeaseOwner).Scan(
		&current.ID, &current.BookingID, &eventID, &current.BookingEventSequence,
		&current.AudienceType, &current.Channel, &current.NotificationType, &current.TemplateKey,
		&current.ReminderOccurrenceAt, &current.ScheduledFor, &current.DestinationHMAC,
		&current.PreferenceRevision, &current.AttemptCount, &current.LeaseOwner,
	); errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrDispatchNotAuthorized
	} else if err != nil {
		return Delivery{}, fmt.Errorf("lock notification delivery: %w", err)
	}
	if eventID.Valid {
		id := eventID.UUID
		current.BookingEventID = &id
	}
	var latestEventID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence DESC LIMIT 1
	`, current.BookingID).Scan(&latestEventID); err != nil {
		if cancelErr := r.cancelUnauthorizedTx(ctx, tx, current, "booking_event_missing"); cancelErr != nil {
			return Delivery{}, cancelErr
		}
		return Delivery{}, ErrDispatchNotAuthorized
	}
	state, err := loadBookingStateTx(ctx, tx, latestEventID)
	if err != nil {
		return Delivery{}, err
	}
	authorized, code := r.deliveryAuthorizedTx(ctx, tx, current, state)
	if !authorized {
		if err := r.cancelUnauthorizedTx(ctx, tx, current, code); err != nil {
			return Delivery{}, err
		}
		return Delivery{}, ErrDispatchNotAuthorized
	}
	tag, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='dispatching',lease_owner='',lease_expires_at=NULL,
			dispatch_authorized_at=NOW(),authorized_booking_event_sequence=$3,
			authorized_preference_revision=$4,last_error_code='',updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, current.ID, current.LeaseOwner, state.CurrentBookingEventSequence, state.ProviderPreferenceRevision)
	if err != nil || tag.RowsAffected() != 1 {
		return Delivery{}, ErrDispatchNotAuthorized
	}
	if err := tx.Commit(ctx); err != nil {
		return Delivery{}, fmt.Errorf("commit notification dispatch fence: %w", err)
	}
	current.BookingEventSequence = state.CurrentBookingEventSequence
	current.PreferenceRevision = state.ProviderPreferenceRevision
	return current, nil
}

func (r *Repository) deliveryAuthorizedTx(
	ctx context.Context,
	tx pgx.Tx,
	delivery Delivery,
	state bookingState,
) (bool, string) {
	destination := ""
	preferenceEnabled := false
	switch delivery.AudienceType + ":" + delivery.Channel {
	case "provider:email":
		destination = state.ProviderEmail
		preferenceEnabled = state.ProviderBookingEmail && state.ProviderEmailVerified
	case "provider:whatsapp":
		destination = state.ProviderWhatsApp
		preferenceEnabled = state.ProviderBookingWhatsApp && state.ProviderWhatsAppVerified &&
			r.templateEnabled(whatsapp.TemplateKey(delivery.TemplateKey))
	case "customer:email":
		destination = state.CustomerEmail
		preferenceEnabled = state.ConsentPolicyRevision == 1 && state.MarketplaceBookingEmail
		if delivery.NotificationType == "appointment_reminder" {
			preferenceEnabled = preferenceEnabled && state.EmailReminderConsent
		}
	case "customer:whatsapp":
		destination = state.CustomerWhatsApp
		preferenceEnabled = state.ConsentPolicyRevision == 1 && state.MarketplaceBookingWhatsApp &&
			state.WhatsAppConsent && r.templateEnabled(whatsapp.TemplateKey(delivery.TemplateKey))
	}
	if !preferenceEnabled || destination == "" {
		return false, "preference_or_destination_changed"
	}
	expectedHMAC := r.destinationFingerprint(delivery.Channel, destination)
	if !hmacEqual(expectedHMAC, delivery.DestinationHMAC) {
		return false, "destination_changed"
	}
	var suppressed bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM notification_contact_suppressions
		 WHERE channel=$1 AND destination_hmac=$2)
	`, delivery.Channel, delivery.DestinationHMAC).Scan(&suppressed); err != nil || suppressed {
		return false, "destination_suppressed"
	}
	switch delivery.NotificationType {
	case "provider_new_booking":
		if !providerBookingStatusEligible(state.Status) || !bookingSecured(state) {
			return false, "booking_not_secured"
		}
	case "appointment_reminder":
		if delivery.ReminderOccurrenceAt == nil || !delivery.ReminderOccurrenceAt.Equal(state.StartAt) ||
			!bookingSecured(state) || !state.StartAt.After(r.now()) || isTerminalBookingStatus(state.Status) {
			return false, "reminder_no_longer_eligible"
		}
		if delivery.AudienceType == "customer" && state.Status != "confirmed" {
			return false, "customer_reminder_not_confirmed"
		}
	case "customer_booking_received":
		if isTerminalBookingStatus(state.Status) {
			return false, "booking_terminal"
		}
	case "booking_rescheduled", "booking_cancelled", "booking_expired",
		"payment_satisfied", "payment_failed", "payment_refunded", "payment_action_required":
		if current, code := materialEventStillCurrentTx(ctx, tx, delivery, state); !current {
			return false, code
		}
	default:
		return false, "unsupported_notification_type"
	}
	return true, ""
}

func materialEventStillCurrentTx(
	ctx context.Context,
	tx pgx.Tx,
	delivery Delivery,
	state bookingState,
) (bool, string) {
	if delivery.BookingEventID == nil {
		return false, "booking_event_missing"
	}
	var rawPayload []byte
	if err := tx.QueryRow(ctx, `
		SELECT payload FROM booking_domain_events WHERE id=$1 AND booking_id=$2
	`, *delivery.BookingEventID, delivery.BookingID).Scan(&rawPayload); err != nil {
		return false, "booking_event_missing"
	}
	var payload eventPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return false, "booking_event_invalid"
	}
	switch delivery.NotificationType {
	case "booking_rescheduled":
		return !payload.PreviousStartsAt.IsZero() && payload.StartsAt.Equal(state.StartAt) &&
				!payload.PreviousStartsAt.Equal(payload.StartsAt) && !isTerminalBookingStatus(state.Status),
			"booking_changed_after_planning"
	case "booking_cancelled", "booking_expired":
		currentType := eventNotificationType(state, bookingEvent{Payload: payload})
		return payload.Status == state.Status && currentType == delivery.NotificationType,
			"booking_changed_after_planning"
	case "payment_satisfied", "payment_failed", "payment_refunded", "payment_action_required":
		currentType := eventNotificationType(state, bookingEvent{Payload: payload})
		return payload.PaymentStatus == state.PaymentStatus && currentType == delivery.NotificationType,
			"booking_changed_after_planning"
	default:
		return false, "unsupported_notification_type"
	}
}

func (r *Repository) cancelUnauthorizedTx(
	ctx context.Context,
	tx pgx.Tx,
	delivery Delivery,
	code string,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='cancelled',lease_owner='',lease_expires_at=NULL,
			last_error_code=$3,completed_at=NOW(),updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, delivery.ID, delivery.LeaseOwner, boundedCode(code)); err != nil {
		return fmt.Errorf("cancel unauthorized notification delivery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit cancelled notification delivery: %w", err)
	}
	return nil
}

func hmacEqual(left, right []byte) bool {
	if len(left) != sha256Size || len(right) != sha256Size {
		return false
	}
	var value byte
	for index := range left {
		value |= left[index] ^ right[index]
	}
	return value == 0
}

const sha256Size = 32
