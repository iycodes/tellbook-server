package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type bookingEvent struct {
	ID       uuid.UUID
	Sequence int64
	Type     string
	Payload  eventPayload
}

type eventPayload struct {
	Status                  string    `json:"status"`
	PreviousStatus          string    `json:"previous_status"`
	PaymentStatus           string    `json:"payment_status"`
	PreviousPaymentStatus   string    `json:"previous_payment_status"`
	AgreementStatus         string    `json:"agreement_status"`
	PreviousAgreementStatus string    `json:"previous_agreement_status"`
	StartsAt                time.Time `json:"starts_at"`
	PreviousStartsAt        time.Time `json:"previous_starts_at"`
}

type bookingState struct {
	ProviderContactPhone        string
	ID                          uuid.UUID
	ClientID                    uuid.UUID
	MarketplaceCustomerID       *uuid.UUID
	Status                      string
	PaymentStatus               string
	AgreementStatus             string
	StartAt                     time.Time
	ReservationExpiredAt        *time.Time
	AgreementRequired           bool
	CustomerEmail               string
	CustomerWhatsApp            string
	EmailReminderConsent        bool
	WhatsAppConsent             bool
	ConsentPolicyRevision       int
	MarketplaceBookingEmail     bool
	MarketplaceReminderEmail    bool
	MarketplaceReminderWhatsApp bool
	ProviderEmail               string
	ProviderEmailVerified       bool
	ProviderBookingEmail        bool
	ProviderBookingWhatsApp     bool
	ProviderReminderEnabled     bool
	ProviderReminderMinutes     int
	ProviderWhatsApp            string
	ProviderWhatsAppVerified    bool
	ProviderPreferenceRevision  int64
	CurrentBookingEventSequence int64
}

type deliveryCandidate struct {
	audience           string
	channel            string
	notificationType   string
	templateKey        string
	scheduledFor       time.Time
	reminderOccurrence *time.Time
	reminderMinutes    *int
	destination        string
	preferenceRevision int64
	eventSequence      int64
	idempotencyKey     string
}

func (r *Repository) PlanEventJob(ctx context.Context, job EventJob) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin notification event planning: %w", err)
	}
	defer tx.Rollback(ctx)
	var event bookingEvent
	var jobOrigin string
	var payload []byte
	if err := tx.QueryRow(ctx, `
		SELECT event.id,event.sequence,event.event_type,event.payload,job.origin
		FROM notification_event_jobs job
		JOIN booking_domain_events event ON event.id=job.booking_event_id
		WHERE job.booking_event_id=$1 AND job.status='processing' AND job.lease_owner=$2
		FOR UPDATE OF job
	`, job.BookingEventID, job.LeaseOwner).Scan(
		&event.ID, &event.Sequence, &event.Type, &payload, &jobOrigin,
	); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("notification event lease was lost")
	} else if err != nil {
		return fmt.Errorf("lock notification event job: %w", err)
	}
	if err := json.Unmarshal(payload, &event.Payload); err != nil {
		return fmt.Errorf("decode notification event payload: %w", err)
	}
	state, err := loadBookingStateTx(ctx, tx, event.ID)
	if err != nil {
		return err
	}
	if err := r.reconcileBookingTx(ctx, tx, state, &event, jobOrigin == "backfill", nil); err != nil {
		return err
	}
	if err := completeEventJobTx(ctx, tx, job); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit notification event planning: %w", err)
	}
	return nil
}

func loadBookingStateTx(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) (bookingState, error) {
	var state bookingState
	var marketplaceCustomerID uuid.NullUUID
	err := tx.QueryRow(ctx, `
		SELECT booking.id,booking.client_id,booking.marketplace_customer_id,
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
			COALESCE(provider_preference.preference_revision,1),
			(SELECT COALESCE(MAX(sequence),0) FROM booking_domain_events latest
			 WHERE latest.booking_id=booking.id), COALESCE(profile.booking_contact_phone,'')
		FROM booking_domain_events event
		JOIN bookings booking ON booking.id=event.booking_id
		JOIN clients client ON client.id=booking.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=booking.client_id
		LEFT JOIN provider_notification_preferences provider_preference
			ON provider_preference.client_id=booking.client_id
		LEFT JOIN marketplace_notification_preferences customer_preference
			ON customer_preference.marketplace_customer_id=booking.marketplace_customer_id
		WHERE event.id=$1
	`, eventID).Scan(bookingStateScanTargets(&state, &marketplaceCustomerID)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return bookingState{}, errors.New("notification booking no longer exists")
	}
	if err != nil {
		return bookingState{}, fmt.Errorf("load notification booking state: %w", err)
	}
	if marketplaceCustomerID.Valid {
		id := marketplaceCustomerID.UUID
		state.MarketplaceCustomerID = &id
	}
	return state, nil
}

func bookingStateScanTargets(state *bookingState, marketplaceCustomerID *uuid.NullUUID) []any {
	return []any{
		&state.ID, &state.ClientID, marketplaceCustomerID,
		&state.Status, &state.PaymentStatus, &state.AgreementStatus, &state.StartAt,
		&state.ReservationExpiredAt, &state.AgreementRequired,
		&state.CustomerEmail, &state.CustomerWhatsApp,
		&state.EmailReminderConsent, &state.WhatsAppConsent, &state.ConsentPolicyRevision,
		&state.MarketplaceBookingEmail,
		&state.MarketplaceReminderEmail, &state.MarketplaceReminderWhatsApp,
		&state.ProviderEmail, &state.ProviderEmailVerified,
		&state.ProviderBookingEmail, &state.ProviderBookingWhatsApp,
		&state.ProviderReminderEnabled, &state.ProviderReminderMinutes,
		&state.ProviderWhatsApp, &state.ProviderWhatsAppVerified,
		&state.ProviderPreferenceRevision, &state.CurrentBookingEventSequence,
		&state.ProviderContactPhone,
	}
}

func setMarketplaceCustomerID(state *bookingState, marketplaceCustomerID uuid.NullUUID) {
	if marketplaceCustomerID.Valid {
		id := marketplaceCustomerID.UUID
		state.MarketplaceCustomerID = &id
	}
}

func (r *Repository) reconcileBookingTx(
	ctx context.Context,
	tx pgx.Tx,
	state bookingState,
	event *bookingEvent,
	backfill bool,
	suppressionCache map[string]bool,
) error {
	now := r.now()
	candidates := make([]deliveryCandidate, 0, 10)
	desiredBookingLifecycleKeys := make([]string, 0, 3)
	if r.emailEnabled && !backfill && event != nil && event.Type == "booking_created" && state.ConsentPolicyRevision == 1 &&
		state.CustomerEmail != "" && state.MarketplaceBookingEmail {
		candidates = append(candidates, deliveryCandidate{
			audience: "customer", channel: "email", notificationType: "customer_booking_received",
			templateKey: "customer_booking_received", scheduledFor: now,
			destination: state.CustomerEmail, idempotencyKey: "customer_booking_received:" + state.ID.String(),
		})
	}
	secured := bookingSecured(state)
	providerLifecycleEligible := providerBookingStatusEligible(state.Status) && secured
	if !backfill && providerLifecycleEligible {
		if r.emailEnabled && state.ProviderBookingEmail && state.ProviderEmailVerified && state.ProviderEmail != "" {
			candidate := deliveryCandidate{
				audience: "provider", channel: "email", notificationType: "provider_new_booking",
				templateKey: "provider_new_booking", scheduledFor: now, destination: state.ProviderEmail,
				preferenceRevision: state.ProviderPreferenceRevision,
				idempotencyKey:     "provider_new_booking:" + state.ID.String() + ":email",
			}
			candidates = append(candidates, candidate)
			desiredBookingLifecycleKeys = append(desiredBookingLifecycleKeys, candidate.idempotencyKey)
		}
		if r.whatsAppEnabled && state.ProviderBookingWhatsApp && state.ProviderWhatsAppVerified && state.ProviderWhatsApp != "" &&
			r.templateEnabled(whatsapp.TemplateProviderNewBooking) {
			candidate := deliveryCandidate{
				audience: "provider", channel: "whatsapp", notificationType: "provider_new_booking",
				templateKey: string(whatsapp.TemplateProviderNewBooking), scheduledFor: now,
				destination: state.ProviderWhatsApp, preferenceRevision: state.ProviderPreferenceRevision,
				idempotencyKey: "provider_new_booking:" + state.ID.String() + ":whatsapp",
			}
			candidates = append(candidates, candidate)
			desiredBookingLifecycleKeys = append(desiredBookingLifecycleKeys, candidate.idempotencyKey)
		}
		if r.emailEnabled && state.ConsentPolicyRevision == 1 && state.CustomerEmail != "" && state.MarketplaceBookingEmail {
			candidate := deliveryCandidate{
				audience: "customer", channel: "email", notificationType: "customer_booking_secured",
				templateKey: "customer_booking_secured", scheduledFor: now, destination: state.CustomerEmail,
				idempotencyKey: "customer_booking_secured:" + state.ID.String(),
			}
			candidates = append(candidates, candidate)
			desiredBookingLifecycleKeys = append(desiredBookingLifecycleKeys, candidate.idempotencyKey)
		}
	}
	if !backfill && event != nil {
		candidates = append(candidates, r.eventCandidates(state, *event, now)...)
	}
	reminders := r.reminderCandidates(state, now)
	if backfill {
		reminders = providerCandidates(reminders)
	}
	candidates = append(candidates, reminders...)
	desiredReminderKeys := make([]string, 0, 4)
	for _, candidate := range candidates {
		if candidate.notificationType == "appointment_reminder" {
			desiredReminderKeys = append(desiredReminderKeys, candidate.idempotencyKey)
		}
		if err := r.insertDeliveryTx(ctx, tx, state, event, candidate, suppressionCache); err != nil {
			return err
		}
	}
	if len(candidates) > 0 {
		if _, err := tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core','notification_delivery')`); err != nil {
			return fmt.Errorf("wake notification delivery workers: %w", err)
		}
	}
	reminderAudience := ""
	if backfill {
		reminderAudience = "provider"
	}
	if err := cancelUndesiredRemindersTx(ctx, tx, state.ID, desiredReminderKeys, reminderAudience); err != nil {
		return err
	}
	inAppEvent := event
	if backfill {
		inAppEvent = nil
	}
	if err := r.reconcileInAppTx(ctx, tx, state, inAppEvent); err != nil {
		return err
	}
	if !backfill {
		if _, err := tx.Exec(ctx, `
			UPDATE notification_deliveries SET status='cancelled',lease_owner='',lease_expires_at=NULL,
					completed_at=NOW(),last_error_code='booking_lifecycle_delivery_no_longer_eligible',updated_at=NOW()
				WHERE booking_id=$1 AND notification_type IN ('provider_new_booking','customer_booking_secured')
				  AND status IN ('pending','retry','processing')
				  AND NOT (idempotency_key=ANY($2::text[]))
			`, state.ID, desiredBookingLifecycleKeys); err != nil {
			return fmt.Errorf("cancel ineligible booking lifecycle deliveries: %w", err)
		}
	}
	if !backfill {
		return r.reconcileAdditionalTx(ctx, tx, state, event, suppressionCache)
	}
	return nil
}

func (r *Repository) eventCandidates(state bookingState, event bookingEvent, now time.Time) []deliveryCandidate {
	typeNames := eventNotificationTypes(state, event)
	if len(typeNames) == 0 {
		return nil
	}
	keySuffix := strconv.FormatInt(event.Sequence, 10)
	items := make([]deliveryCandidate, 0, len(typeNames)*4)
	for _, typeName := range typeNames {
		if typeName == "booking_completed" {
			continue
		}
		if r.emailEnabled && state.ProviderBookingEmail && state.ProviderEmailVerified && state.ProviderEmail != "" {
			items = append(items, deliveryCandidate{
				audience: "provider", channel: "email", notificationType: typeName,
				templateKey: typeName, scheduledFor: now, destination: state.ProviderEmail,
				preferenceRevision: state.ProviderPreferenceRevision,
				eventSequence:      event.Sequence,
				idempotencyKey:     typeName + ":" + state.ID.String() + ":provider:email:" + keySuffix,
			})
		}
		if r.whatsAppEnabled && state.ProviderBookingWhatsApp && state.ProviderWhatsAppVerified && state.ProviderWhatsApp != "" &&
			r.templateEnabled(whatsapp.TemplateBookingStatusUpdate) {
			items = append(items, deliveryCandidate{
				audience: "provider", channel: "whatsapp", notificationType: typeName,
				templateKey: string(whatsapp.TemplateBookingStatusUpdate), scheduledFor: now,
				destination: state.ProviderWhatsApp, preferenceRevision: state.ProviderPreferenceRevision,
				eventSequence:  event.Sequence,
				idempotencyKey: typeName + ":" + state.ID.String() + ":provider:whatsapp:" + keySuffix,
			})
		}
		if r.emailEnabled && state.ConsentPolicyRevision == 1 && state.CustomerEmail != "" && state.MarketplaceBookingEmail {
			items = append(items, deliveryCandidate{
				audience: "customer", channel: "email", notificationType: typeName,
				templateKey: typeName, scheduledFor: now, destination: state.CustomerEmail,
				eventSequence:  event.Sequence,
				idempotencyKey: typeName + ":" + state.ID.String() + ":customer:email:" + keySuffix,
			})
		}
		if r.whatsAppEnabled && state.ConsentPolicyRevision == 1 && state.WhatsAppConsent &&
			state.MarketplaceReminderWhatsApp && state.CustomerWhatsApp != "" &&
			r.templateEnabled(whatsapp.TemplateBookingStatusUpdate) {
			items = append(items, deliveryCandidate{
				audience: "customer", channel: "whatsapp", notificationType: typeName,
				templateKey: string(whatsapp.TemplateBookingStatusUpdate), scheduledFor: now,
				destination: state.CustomerWhatsApp, eventSequence: event.Sequence,
				idempotencyKey: typeName + ":" + state.ID.String() + ":customer:whatsapp:" + keySuffix,
			})
		}
	}
	return items
}

func eventNotificationTypes(state bookingState, event bookingEvent) []string {
	types := make([]string, 0, 3)
	if isTerminalBookingStatus(state.Status) && event.Payload.PreviousStatus != state.Status {
		switch state.Status {
		case "completed":
			types = append(types, "booking_completed")
		case "expired":
			types = append(types, "booking_expired")
		default:
			types = append(types, "booking_cancelled")
		}
	}
	if !event.Payload.PreviousStartsAt.IsZero() && !event.Payload.PreviousStartsAt.Equal(state.StartAt) {
		types = append(types, "booking_rescheduled")
	}
	if event.Payload.PreviousPaymentStatus != "" && event.Payload.PreviousPaymentStatus != state.PaymentStatus {
		switch state.PaymentStatus {
		case "deposit_paid_balance_due", "paid_in_full":
			if !paymentObligationSatisfied(event.Payload.PreviousPaymentStatus) {
				types = append(types, "payment_satisfied")
			}
		case "payment_failed":
			types = append(types, "payment_failed")
		case "refunded":
			types = append(types, "payment_refunded")
		case "disputed":
			types = append(types, "payment_action_required")
		}
	}
	return types
}

func paymentObligationSatisfied(status string) bool {
	return status == "deposit_paid_balance_due" || status == "paid_in_full"
}

func providerCandidates(candidates []deliveryCandidate) []deliveryCandidate {
	providers := candidates[:0]
	for _, candidate := range candidates {
		if candidate.audience == "provider" {
			providers = append(providers, candidate)
		}
	}
	return providers
}

func (r *Repository) reminderCandidates(state bookingState, now time.Time) []deliveryCandidate {
	if !bookingSecured(state) || isTerminalBookingStatus(state.Status) || !state.StartAt.After(now) {
		return nil
	}
	scheduledFor := state.StartAt.Add(-appointmentReminderMins * time.Minute)
	if !scheduledFor.After(now) {
		if state.StartAt.Sub(now) < 2*time.Hour {
			return nil
		}
		scheduledFor = now
	}
	occurrence := state.StartAt.UTC()
	offset := appointmentReminderMins
	occurrenceKey := strconv.FormatInt(occurrence.Unix(), 10)
	items := make([]deliveryCandidate, 0, 4)
	if state.ProviderReminderEnabled && providerBookingStatusEligible(state.Status) {
		if r.emailEnabled && state.ProviderBookingEmail && state.ProviderEmailVerified && state.ProviderEmail != "" {
			items = append(items, deliveryCandidate{
				audience: "provider", channel: "email", notificationType: "appointment_reminder",
				templateKey: "provider_booking_reminder", scheduledFor: scheduledFor,
				reminderOccurrence: &occurrence, reminderMinutes: &offset,
				destination: state.ProviderEmail, preferenceRevision: state.ProviderPreferenceRevision,
				idempotencyKey: "appointment_reminder:" + state.ID.String() + ":provider:email:" + occurrenceKey,
			})
		}
		if r.whatsAppEnabled && state.ProviderBookingWhatsApp && state.ProviderWhatsAppVerified && state.ProviderWhatsApp != "" &&
			r.templateEnabled(whatsapp.TemplateProviderBookingReminder) {
			items = append(items, deliveryCandidate{
				audience: "provider", channel: "whatsapp", notificationType: "appointment_reminder",
				templateKey: string(whatsapp.TemplateProviderBookingReminder), scheduledFor: scheduledFor,
				reminderOccurrence: &occurrence, reminderMinutes: &offset,
				destination: state.ProviderWhatsApp, preferenceRevision: state.ProviderPreferenceRevision,
				idempotencyKey: "appointment_reminder:" + state.ID.String() + ":provider:whatsapp:" + occurrenceKey,
			})
		}
	}
	if state.Status == "confirmed" && state.ConsentPolicyRevision == 1 {
		if r.emailEnabled && state.EmailReminderConsent && state.MarketplaceReminderEmail && state.CustomerEmail != "" {
			items = append(items, deliveryCandidate{
				audience: "customer", channel: "email", notificationType: "appointment_reminder",
				templateKey: "customer_booking_reminder", scheduledFor: scheduledFor,
				reminderOccurrence: &occurrence, reminderMinutes: &offset,
				destination:    state.CustomerEmail,
				idempotencyKey: "appointment_reminder:" + state.ID.String() + ":customer:email:" + occurrenceKey,
			})
		}
		if r.whatsAppEnabled && state.WhatsAppConsent && state.MarketplaceReminderWhatsApp && state.CustomerWhatsApp != "" &&
			state.ProviderContactPhone != "" && r.templateEnabled(whatsapp.TemplateUserReminder) {
			items = append(items, deliveryCandidate{
				audience: "customer", channel: "whatsapp", notificationType: "appointment_reminder",
				templateKey: string(whatsapp.TemplateUserReminder), scheduledFor: scheduledFor,
				reminderOccurrence: &occurrence, reminderMinutes: &offset,
				destination:    state.CustomerWhatsApp,
				idempotencyKey: "appointment_reminder:" + state.ID.String() + ":customer:whatsapp:" + occurrenceKey,
			})
		}
	}
	return items
}

func (r *Repository) insertDeliveryTx(
	ctx context.Context,
	tx pgx.Tx,
	state bookingState,
	event *bookingEvent,
	candidate deliveryCandidate,
	suppressionCache map[string]bool,
) error {
	destinationHMAC := r.destinationFingerprint(candidate.channel, candidate.destination)
	cacheKey := candidate.channel + ":" + string(destinationHMAC)
	suppressed, cached := suppressionCache[cacheKey]
	if !cached {
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM notification_contact_suppressions
			 WHERE channel=$1 AND destination_hmac=$2)
		`, candidate.channel, destinationHMAC).Scan(&suppressed); err != nil {
			return fmt.Errorf("check notification suppression: %w", err)
		}
		if suppressionCache != nil {
			suppressionCache[cacheKey] = suppressed
		}
	}
	if suppressed {
		return nil
	}
	var eventID any
	eventSequence := state.CurrentBookingEventSequence
	if event != nil {
		eventID = event.ID
	}
	if candidate.eventSequence > 0 {
		eventSequence = candidate.eventSequence
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO notification_deliveries (
			id,idempotency_key,client_id,marketplace_customer_id,booking_id,booking_event_id,
			booking_event_sequence,audience_type,channel,notification_type,template_key,
			reminder_occurrence_at,reminder_offset_minutes,scheduled_for,destination_hmac,
			preference_revision,status,next_attempt_at,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'pending',$14,NOW(),NOW())
		ON CONFLICT (idempotency_key) DO UPDATE SET
			booking_event_id=EXCLUDED.booking_event_id,
			booking_event_sequence=EXCLUDED.booking_event_sequence,
			scheduled_for=EXCLUDED.scheduled_for,next_attempt_at=EXCLUDED.next_attempt_at,
			destination_hmac=EXCLUDED.destination_hmac,
			preference_revision=EXCLUDED.preference_revision,status='pending',
            lease_owner='',lease_expires_at=NULL,
			last_error_code='',completed_at=NULL,updated_at=NOW()
		WHERE (notification_deliveries.status IN ('pending','retry','cancelled','failed')
           OR (notification_deliveries.notification_type='booking_step_reminder' AND notification_deliveries.status='processing'))
		  AND notification_deliveries.dispatch_authorized_at IS NULL
	`, uuid.New(), candidate.idempotencyKey, state.ClientID, state.MarketplaceCustomerID,
		state.ID, eventID, eventSequence, candidate.audience, candidate.channel,
		candidate.notificationType, candidate.templateKey, candidate.reminderOccurrence,
		candidate.reminderMinutes, candidate.scheduledFor, destinationHMAC, candidate.preferenceRevision)
	if err != nil {
		return fmt.Errorf("upsert notification delivery: %w", err)
	}
	return nil
}

func cancelUndesiredRemindersTx(
	ctx context.Context,
	tx pgx.Tx,
	bookingID uuid.UUID,
	desired []string,
	audience string,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification_deliveries SET status='cancelled',lease_owner='',lease_expires_at=NULL,
			completed_at=NOW(),last_error_code='reminder_no_longer_eligible',updated_at=NOW()
		WHERE booking_id=$1 AND notification_type='appointment_reminder'
		  AND status IN ('pending','retry','processing')
		  AND NOT (idempotency_key=ANY($2::text[]))
		  AND ($3='' OR audience_type=$3)
	`, bookingID, desired, audience)
	if err != nil {
		return fmt.Errorf("cancel stale appointment reminders: %w", err)
	}
	return nil
}

func bookingSecured(state bookingState) bool {
	paymentSatisfied := paymentObligationSatisfied(state.PaymentStatus)
	agreementSatisfied := !state.AgreementRequired || state.AgreementStatus == "accepted" || state.AgreementStatus == "signed"
	return paymentSatisfied && agreementSatisfied && state.ReservationExpiredAt == nil
}

func providerBookingStatusEligible(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "booked", "pending", "confirmed":
		return true
	default:
		return false
	}
}

func isTerminalBookingStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "cancelled", "canceled", "declined", "completed", "no_show", "expired":
		return true
	default:
		return false
	}
}
