package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/agreements/signature"
	"booking/go-server/internal/bookingdomain"
	"booking/go-server/internal/money"
	"booking/go-server/internal/publictoken"
	"booking/go-server/internal/securityemail"
	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type bookingQuoteRecord struct {
	ID                            uuid.UUID
	BookingID                     *uuid.UUID
	ClientID                      uuid.UUID
	ServiceID                     uuid.UUID
	ServiceTitle                  string
	BusinessName                  string
	ServiceImageURL               string
	DurationMinutes               int
	StartsAt                      time.Time
	EndsAt                        time.Time
	Timezone                      string
	LocationLabel                 string
	ProviderLocationLabel         string
	CountryCode                   string
	CurrencyCode                  string
	Locale                        string
	BaseAmountMinor               int64
	TotalAmountMinor              int64
	DepositAmountMinor            int64
	CustomerName                  string
	CustomerEmail                 string
	CustomerPhone                 string
	BookingNotes                  string
	ExpiresAt                     time.Time
	CancellationPolicy            string
	LatenessPolicy                string
	AgreementTemplateFamilyID     uuid.UUID
	AgreementTemplateVersionID    uuid.UUID
	AgreementTitle                string
	AgreementBookingSummaryJSON   []byte
	AgreementResolvedDocumentJSON []byte
	AgreementSchemaVersion        int
	AgreementRendererVersion      int
	AgreementRenderedHTML         string
	AgreementResolvedTermsHash    string
	AgreementConfirmationMethod   string
	AgreementTiming               string
	StandaloneSignatureRequired   bool
}

func (q bookingQuoteRecord) hasAgreement() bool {
	return q.AgreementTemplateFamilyID != uuid.Nil
}

type bookingAcceptanceEvidence struct {
	method    string
	signer    string
	signature *signature.Normalized
	accepted  bool
}

func (r *Repository) createPublicBooking(
	ctx context.Context,
	slug string,
	input CreatePublicBookingInput,
	authority BookingReservationAuthority,
) (PublicBookingSummaryResponse, error) {
	quoteToken := strings.TrimSpace(input.QuoteToken)
	if quoteToken == "" {
		return PublicBookingSummaryResponse{}, fmt.Errorf("quote_token is required")
	}
	normalizedEmail := strings.ToLower(strings.TrimSpace(input.Email))
	if normalizedEmail == "" {
		return PublicBookingSummaryResponse{}, fmt.Errorf("email is required")
	}
	bookingSource, err := normalizePublicBookingSource(input.Source)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PublicBookingSummaryResponse{}, fmt.Errorf("begin booking transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := securityemail.EnableTx(ctx, tx, false, r.additionalEmails); err != nil {
		return PublicBookingSummaryResponse{}, err
	}

	var authorizedQuoteID uuid.UUID
	if authority == BookingReservationAuthorityAutopilot &&
		input.MarketplaceCustomerID != nil && input.AutopilotAuthority != nil {
		authorizedQuoteID, err = r.validateInboxAutopilotReservationAuthorityTx(
			ctx, tx, slug, quoteToken, input,
		)
		if err != nil {
			return PublicBookingSummaryResponse{}, err
		}
	}
	quote, err := lockBookingQuote(ctx, tx, slug, quoteToken)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	if authority == BookingReservationAuthorityAutopilot && authorizedQuoteID == uuid.Nil {
		return PublicBookingSummaryResponse{}, ErrAutopilotReservationAuthorityRequired
	}
	if authority == BookingReservationAuthorityAutopilot && quote.ID != authorizedQuoteID {
		return PublicBookingSummaryResponse{}, ErrInboxAIProposalStale
	}
	bookingDetailsMatch := normalizedEmail == quote.CustomerEmail &&
		strings.TrimSpace(input.FullName) == quote.CustomerName &&
		strings.TrimSpace(input.Phone) == quote.CustomerPhone &&
		strings.TrimSpace(input.Notes) == quote.BookingNotes
	if quote.BookingID != nil && !bookingDetailsMatch {
		return PublicBookingSummaryResponse{}, ErrBookingReplayConflict
	}
	if quote.BookingID != nil {
		var bookingToken, existingSource, existingEmail, existingWhatsApp string
		var existingEmailConsent, existingWhatsAppConsent, existingSMSConsent bool
		if err := tx.QueryRow(ctx, `
			SELECT public_token, source, customer_email_snapshot,
				email_reminder_consent, COALESCE(customer_whatsapp_e164_snapshot, ''),
				whatsapp_consent, sms_consent
			FROM bookings WHERE id = $1
		`, *quote.BookingID).Scan(
			&bookingToken, &existingSource, &existingEmail, &existingEmailConsent,
			&existingWhatsApp, &existingWhatsAppConsent, &existingSMSConsent,
		); err != nil {
			return PublicBookingSummaryResponse{}, fmt.Errorf("load consumed quote booking: %w", err)
		}
		expectedWhatsApp := ""
		if input.WhatsAppConsent {
			expectedWhatsApp, err = whatsapp.NormalizeE164ForCountry(input.Phone, quote.CountryCode)
			if err != nil {
				return PublicBookingSummaryResponse{}, ErrBookingReplayConflict
			}
		}
		if existingSource != bookingSource || existingEmail != normalizedEmail ||
			existingEmailConsent != input.EmailReminderConsent ||
			existingWhatsApp != expectedWhatsApp ||
			existingWhatsAppConsent != input.WhatsAppConsent ||
			existingSMSConsent != input.SMSConsent {
			return PublicBookingSummaryResponse{}, ErrBookingReplayConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return PublicBookingSummaryResponse{}, fmt.Errorf("commit idempotent booking lookup: %w", err)
		}
		return r.GetPublicBookingSummary(ctx, bookingToken)
	}
	// Pin platform eligibility until the reservation commits. A concurrent staff
	// restriction waits for this transaction; old quotes cannot bypass a committed
	// restriction. Completed idempotent replays above remain available.
	if err := ensureBusinessAcceptsNewBookings(ctx, tx, quote.ClientID); err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	if !quote.ExpiresAt.After(time.Now().UTC()) {
		return PublicBookingSummaryResponse{}, ErrQuoteExpired
	}
	if !bookingDetailsMatch {
		return PublicBookingSummaryResponse{}, fmt.Errorf("booking details changed after the quote was created; refresh the quote")
	}
	var reservationExpiresAt *time.Time
	if authority == BookingReservationAuthorityAutopilot && quote.TotalAmountMinor > 0 {
		now := time.Now().UTC()
		deadline := now.Add(r.inboxAIAutopilotPaymentWindow)
		latestDeadline := quote.StartsAt.Add(-time.Minute)
		if deadline.After(latestDeadline) {
			deadline = latestDeadline
		}
		if !deadline.After(now) {
			return PublicBookingSummaryResponse{}, ErrAutopilotPaymentWindowUnavailable
		}
		reservationExpiresAt = &deadline
	}

	location, err := loadLocation(quote.Timezone)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	lockKey := quote.ClientID.String() + ":" + quote.StartsAt.In(location).Format("2006-01-02")
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return PublicBookingSummaryResponse{}, fmt.Errorf("lock booking day: %w", err)
	}
	service, err := r.getPublicServiceForBookingTx(ctx, tx, slug, quote.ServiceID)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	availability, err := loadPublicAvailabilityState(ctx, tx, service, quote.StartsAt, time.Now().UTC())
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	slotAvailable := false
	for _, slot := range availability.Slots {
		if slot.Start.Equal(quote.StartsAt) {
			slotAvailable = true
			break
		}
	}
	if !slotAvailable {
		return PublicBookingSummaryResponse{}, ErrSlotUnavailable
	}

	evidence, agreementStatus, err := r.validateBookingAgreementEvidenceForReservation(
		ctx, tx, quote, input,
	)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	customerID, err := upsertPublicCustomer(ctx, tx, quote.ClientID, input)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	bookingID := uuid.New()
	bookingToken, err := publictoken.New()
	if err != nil {
		return PublicBookingSummaryResponse{}, fmt.Errorf("create booking token: %w", err)
	}
	paymentStatus := initialBookingPaymentState(quote.TotalAmountMinor, quote.DepositAmountMinor)
	changePolicy := bookingdomain.StructuredPolicy(quote.CancellationPolicy)
	consentSource := bookingNotificationConsentSource(authority, bookingSource)
	consentRecordedAt := time.Now().UTC()
	var emailConsentAt, whatsappConsentAt *time.Time
	emailConsentSource := ""
	whatsappConsentSource := ""
	if input.EmailReminderConsent {
		emailConsentAt = &consentRecordedAt
		emailConsentSource = consentSource
	}
	var whatsappSnapshot *string
	if input.WhatsAppConsent {
		normalizedWhatsApp, normalizeErr := whatsapp.NormalizeE164ForCountry(input.Phone, quote.CountryCode)
		if normalizeErr != nil {
			return PublicBookingSummaryResponse{}, fmt.Errorf("%w: enter a valid WhatsApp number", ErrInvalidContact)
		}
		whatsappSnapshot = &normalizedWhatsApp
		whatsappConsentAt = &consentRecordedAt
		whatsappConsentSource = consentSource
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO bookings (
			id, public_token, client_id, customer_id, marketplace_customer_id, service_id, title, stylist_name,
			source, status, payment_status, agreement_status, start_at, end_at, timezone,
			base_service_amount_minor, total_amount_minor, deposit_amount_minor,
			currency_code, country_code, duration_minutes, notes, location_label, image_url,
			promotion_id, discount_name, discount_source, discount_code, discount_type,
			discount_percentage_bps, discount_value_minor, discount_amount_minor,
			original_amount_minor, booking_quote_id, discounted_service_amount_minor,
			short_notice_rule_id, short_notice_threshold_minutes,
			short_notice_surcharge_type, short_notice_surcharge_amount_minor,
			short_notice_surcharge_percentage_bps, short_notice_fee_minor, travel_fee_minor,
			fulfillment_mode, provider_location_label, provider_place_id,
			provider_latitude, provider_longitude, customer_location_label,
			customer_place_id, customer_latitude, customer_longitude, travel_distance_meters,
			prep_time_minutes, buffer_time_minutes, occupied_start_at, occupied_end_at,
			virtual_delivery_label, virtual_join_url, virtual_instructions,
			cancellation_policy_snapshot, lateness_policy_snapshot,
			cancellation_notice_minutes_snapshot, cancellation_refund_bps_snapshot,
			reschedule_notice_minutes_snapshot, reschedule_fee_minor_snapshot,
			automated_reschedule_snapshot,
			agreement_template_family_id_snapshot, agreement_template_version_id_snapshot,
			agreement_title_snapshot, agreement_booking_summary_snapshot,
			agreement_resolved_document_snapshot, agreement_schema_version_snapshot,
			agreement_renderer_version_snapshot, agreement_rendered_html_snapshot,
			agreement_resolved_terms_hash_snapshot, agreement_confirmation_method_snapshot,
			agreement_timing_snapshot, standalone_signature_required_snapshot,
			customer_email_snapshot, email_reminder_consent,
			email_reminder_consent_at, email_reminder_consent_source,
			customer_whatsapp_e164_snapshot, whatsapp_consent,
			whatsapp_consent_at, whatsapp_consent_source,
			notification_consent_policy_revision, sms_consent, reservation_expires_at,
			created_at, updated_at
		)
		SELECT
			$2, $3, bq.client_id, $4, $10, bq.service_id, bq.service_title, bq.business_name,
			$7, 'booked', $5, $6, bq.appointment_start_at,
			bq.appointment_end_at, bq.timezone, bq.base_service_amount_minor,
			bq.total_amount_minor, bq.deposit_amount_minor, bq.currency_code,
			bq.country_code, bq.duration_minutes, bq.booking_notes_snapshot, bq.location_label,
			NULLIF(bq.service_image_url, ''), bq.promotion_id, bq.discount_name,
			bq.discount_source, bq.discount_code, bq.discount_type,
			bq.discount_percentage_bps, bq.discount_value_minor, bq.discount_amount_minor,
			bq.base_service_amount_minor, bq.id, bq.discounted_service_amount_minor,
			bq.short_notice_rule_id, bq.short_notice_threshold_minutes,
			bq.short_notice_surcharge_type, bq.short_notice_surcharge_amount_minor,
			bq.short_notice_surcharge_percentage_bps, bq.short_notice_fee_minor,
			bq.travel_fee_minor, bq.fulfillment_mode, bq.provider_location_label,
			bq.provider_place_id, bq.provider_latitude, bq.provider_longitude,
			bq.customer_location_label, bq.customer_place_id, bq.customer_latitude,
			bq.customer_longitude, bq.travel_distance_meters, bq.prep_time_minutes,
			bq.buffer_time_minutes, bq.occupied_start_at, bq.occupied_end_at,
			bq.virtual_delivery_label, bq.virtual_join_url, bq.virtual_instructions,
			bq.cancellation_policy, bq.lateness_policy,
			$11, $12, $13, $14, $15,
			bq.agreement_template_family_id_snapshot, bq.agreement_template_version_id_snapshot,
			bq.agreement_title_snapshot, bq.agreement_booking_summary_snapshot,
			bq.agreement_resolved_document_snapshot, bq.agreement_schema_version_snapshot,
			bq.agreement_renderer_version_snapshot, bq.agreement_rendered_html_snapshot,
			bq.agreement_resolved_terms_hash_snapshot, bq.agreement_confirmation_method_snapshot,
			bq.agreement_timing_snapshot, bq.standalone_signature_required_snapshot,
			$17, $18, $19, $20, $21, $8, $22, $23, 1, $9, $16,
			NOW(), NOW()
		FROM booking_quotes bq
		WHERE bq.id = $1
	`, quote.ID, bookingID, bookingToken, customerID, paymentStatus, agreementStatus,
		bookingSource, input.WhatsAppConsent, input.SMSConsent, input.MarketplaceCustomerID,
		changePolicy.CancellationNoticeMinutes, changePolicy.CancellationRefundBPS,
		changePolicy.RescheduleNoticeMinutes, changePolicy.RescheduleFeeMinor,
		changePolicy.AutomatedReschedule, reservationExpiresAt,
		normalizedEmail, input.EmailReminderConsent, emailConsentAt, emailConsentSource,
		whatsappSnapshot, whatsappConsentAt, whatsappConsentSource); err != nil {
		return PublicBookingSummaryResponse{}, fmt.Errorf("insert booking from quote: %w", err)
	}
	if input.MarketplaceCustomerID != nil {
		if err := linkPreBookingConversationToBooking(
			ctx, tx, quote.ClientID, customerID, *input.MarketplaceCustomerID, bookingID,
		); err != nil {
			return PublicBookingSummaryResponse{}, err
		}
	}
	if err := convertQuotePromotionReservations(ctx, tx, quote, bookingID, customerID); err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	if input.PrebookingAgreementID != nil {
		if err := attachPrebookingAgreementToBooking(
			ctx, tx, *input.PrebookingAgreementID, bookingID, customerID,
		); err != nil {
			return PublicBookingSummaryResponse{}, err
		}
	} else if quote.hasAgreement() {
		if err := r.createBookingAgreementFromQuote(ctx, tx, quote, bookingID, customerID, evidence); err != nil {
			return PublicBookingSummaryResponse{}, err
		}
	} else if quote.StandaloneSignatureRequired {
		if err := insertStandaloneBookingSignature(ctx, tx, bookingID, evidence); err != nil {
			return PublicBookingSummaryResponse{}, err
		}
	}
	if err := insertBookingCreatedEventAndNotification(
		ctx, tx, quote, bookingID, customerID, bookingSource,
	); err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE booking_quotes
		SET booking_id = $2, consumed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND booking_id IS NULL
	`, quote.ID, bookingID); err != nil {
		return PublicBookingSummaryResponse{}, fmt.Errorf("consume booking quote: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PublicBookingSummaryResponse{}, fmt.Errorf("commit booking: %w", err)
	}
	return r.GetPublicBookingSummary(ctx, bookingToken)
}

func (r *Repository) validateBookingAgreementEvidenceForReservation(
	ctx context.Context,
	tx pgx.Tx,
	quote bookingQuoteRecord,
	input CreatePublicBookingInput,
) (bookingAcceptanceEvidence, string, error) {
	if input.PrebookingAgreementID == nil {
		return validateBookingAgreementEvidence(quote, input)
	}
	if !quote.hasAgreement() || quote.AgreementTiming != "before_payment" ||
		quote.StandaloneSignatureRequired {
		return bookingAcceptanceEvidence{}, "", ErrInboxAIAgreementRequired
	}
	var clientID, familyID, versionID uuid.UUID
	var bookingID, customerID uuid.NullUUID
	var termsHash, method, timing, status string
	var expiresAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT client_id, template_family_id, template_version_id,
			resolved_terms_hash, confirmation_method, timing, status,
			booking_id, customer_id, expires_at
		FROM agreement_instances WHERE id=$1 FOR UPDATE
	`, *input.PrebookingAgreementID).Scan(
		&clientID, &familyID, &versionID, &termsHash, &method, &timing, &status,
		&bookingID, &customerID, &expiresAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return bookingAcceptanceEvidence{}, "", ErrInboxAIAgreementRequired
	} else if err != nil {
		return bookingAcceptanceEvidence{}, "", err
	}
	if clientID != quote.ClientID || familyID != quote.AgreementTemplateFamilyID ||
		versionID != quote.AgreementTemplateVersionID || termsHash != quote.AgreementResolvedTermsHash ||
		method != quote.AgreementConfirmationMethod || timing != "before_payment" || status != "completed" ||
		bookingID.Valid || customerID.Valid || expiresAt == nil || !expiresAt.After(time.Now().UTC()) {
		return bookingAcceptanceEvidence{}, "", ErrInboxAIAgreementRequired
	}
	var acceptanceMethod string
	if err := tx.QueryRow(ctx, `
		SELECT method FROM agreement_acceptances
		WHERE agreement_id=$1 AND resolved_terms_hash=$2
	`, *input.PrebookingAgreementID, termsHash).Scan(&acceptanceMethod); errors.Is(err, pgx.ErrNoRows) {
		return bookingAcceptanceEvidence{}, "", ErrInboxAIAgreementRequired
	} else if err != nil {
		return bookingAcceptanceEvidence{}, "", err
	}
	if acceptanceMethod != method {
		return bookingAcceptanceEvidence{}, "", ErrInboxAIAgreementRequired
	}
	agreementStatus := "accepted"
	if method == "signature" {
		agreementStatus = "signed"
	}
	return bookingAcceptanceEvidence{}, agreementStatus, nil
}

func attachPrebookingAgreementToBooking(
	ctx context.Context,
	tx pgx.Tx,
	agreementID, bookingID, customerID uuid.UUID,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE agreement_instances
		SET booking_id=$2, customer_id=$3, updated_at=NOW()
		WHERE id=$1 AND booking_id IS NULL AND customer_id IS NULL AND status='completed'
	`, agreementID, bookingID, customerID)
	if err != nil {
		return fmt.Errorf("attach prebooking agreement: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxAIAgreementRequired
	}
	return nil
}

func linkPreBookingConversationToBooking(
	ctx context.Context,
	tx pgx.Tx,
	clientID, customerID, marketplaceCustomerID, bookingID uuid.UUID,
) error {
	var conversationID uuid.UUID
	var linkedCustomerID uuid.NullUUID
	if err := tx.QueryRow(ctx, `
		SELECT id, customer_id
		FROM inbox_conversations
		WHERE client_id=$1 AND marketplace_customer_id=$2
		FOR UPDATE
	`, clientID, marketplaceCustomerID).Scan(&conversationID, &linkedCustomerID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("load pre-booking conversation: %w", err)
	}
	if linkedCustomerID.Valid && linkedCustomerID.UUID != customerID {
		return fmt.Errorf("marketplace customer maps to multiple provider customer records")
	}
	if !linkedCustomerID.Valid {
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_conversations
			SET customer_id=$2, updated_at=NOW()
			WHERE id=$1 AND customer_id IS NULL
		`, conversationID, customerID); err != nil {
			return fmt.Errorf("link provider customer to pre-booking conversation: %w", err)
		}
	}

	commandTag, err := tx.Exec(ctx, `
		INSERT INTO inbox_conversation_bookings (conversation_id, booking_id, linked_by_actor)
		VALUES ($1,$2,'system')
		ON CONFLICT (booking_id) DO NOTHING
	`, conversationID, bookingID)
	if err != nil {
		return fmt.Errorf("link booking to pre-booking conversation: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return nil
	}

	var eventSequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_events (
			conversation_id, client_id, marketplace_customer_id, event_type, payload
		) VALUES (
			$1,$2,$3,'conversation.updated',
			jsonb_build_object('conversation_id',$1::uuid::text,'booking_id',$4::uuid::text)
		)
		RETURNING sequence
	`, conversationID, clientID, marketplaceCustomerID, bookingID).Scan(&eventSequence); err != nil {
		return fmt.Errorf("append pre-booking link event: %w", err)
	}
	if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
		return fmt.Errorf("notify pre-booking link event: %w", err)
	}
	return nil
}

func normalizePublicBookingSource(source string) (string, error) {
	switch strings.TrimSpace(source) {
	case "", "direct_public_page":
		return "direct_public_page", nil
	case "marketplace":
		return "marketplace", nil
	default:
		return "", fmt.Errorf("source must be direct_public_page or marketplace")
	}
}

func bookingNotificationConsentSource(authority BookingReservationAuthority, source string) string {
	if authority == BookingReservationAuthorityAutopilot {
		return "inbox_autopilot"
	}
	if source == "marketplace" {
		return "marketplace_checkout"
	}
	return "public_checkout"
}

func insertBookingCreatedEventAndNotification(
	ctx context.Context,
	tx pgx.Tx,
	quote bookingQuoteRecord,
	bookingID uuid.UUID,
	customerID uuid.UUID,
	source string,
) error {
	eventID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO booking_domain_events (
			id, client_id, booking_id, event_type, dedupe_key, payload, created_at
		) VALUES (
			$1::uuid, $2::uuid, $3::uuid, 'booking_created', $4,
			jsonb_build_object('booking_id', ($3::uuid)::text, 'source', $5::text, 'starts_at', $6::timestamptz),
			NOW()
		)
	`, eventID, quote.ClientID, bookingID, "booking-created:"+bookingID.String(), source, quote.StartsAt); err != nil {
		return fmt.Errorf("record booking-created event: %w", err)
	}
	title := "New direct booking"
	if source == "marketplace" {
		title = "New marketplace booking"
	}
	description := fmt.Sprintf(
		"%s booked %s for %s",
		quote.CustomerName,
		quote.ServiceTitle,
		quote.StartsAt.Format(time.RFC3339),
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO notifications (
			id, client_id, customer_id, booking_id, type, severity, title, description,
			action_label, action_route, icon_name, icon_tone, metadata, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,'booking_created','normal',$5,$6,
			'View booking','/bookings','calendar_today','muted',
			jsonb_build_object('booking_event_id', $7::text, 'source', $8::text),NOW(),NOW()
		)
	`, uuid.New(), quote.ClientID, customerID, bookingID, title, description, eventID, source); err != nil {
		return fmt.Errorf("create provider booking notification: %w", err)
	}
	return nil
}

func lockBookingQuote(ctx context.Context, tx pgx.Tx, slug, token string) (bookingQuoteRecord, error) {
	const query = `
		SELECT
			bq.id, bq.booking_id, bq.client_id, bq.service_id, bq.service_title,
			bq.business_name, bq.service_image_url, bq.duration_minutes,
			bq.appointment_start_at, bq.appointment_end_at, bq.timezone,
			bq.location_label, bq.provider_location_label, bq.country_code,
			bq.currency_code, bq.locale, bq.base_service_amount_minor,
			bq.total_amount_minor, bq.deposit_amount_minor,
			bq.customer_name_snapshot, bq.customer_email_normalized,
			bq.customer_phone_snapshot, bq.booking_notes_snapshot, bq.expires_at,
			bq.cancellation_policy, bq.lateness_policy,
			COALESCE(bq.agreement_template_family_id_snapshot, '00000000-0000-0000-0000-000000000000'::uuid),
			COALESCE(bq.agreement_template_version_id_snapshot, '00000000-0000-0000-0000-000000000000'::uuid),
			bq.agreement_title_snapshot, bq.agreement_booking_summary_snapshot,
			COALESCE(bq.agreement_resolved_document_snapshot, '{}'::jsonb),
			COALESCE(bq.agreement_schema_version_snapshot, 0),
			COALESCE(bq.agreement_renderer_version_snapshot, 0),
			bq.agreement_rendered_html_snapshot, bq.agreement_resolved_terms_hash_snapshot,
			bq.agreement_confirmation_method_snapshot, bq.agreement_timing_snapshot,
			bq.standalone_signature_required_snapshot
		FROM booking_quotes bq
		INNER JOIN client_profile_handles cph
			ON cph.client_id = bq.client_id AND cph.handle_slug = $1
		WHERE bq.public_token = $2
		FOR UPDATE OF bq
	`
	var quote bookingQuoteRecord
	if err := tx.QueryRow(ctx, query, strings.TrimSpace(slug), token).Scan(
		&quote.ID, &quote.BookingID, &quote.ClientID, &quote.ServiceID,
		&quote.ServiceTitle, &quote.BusinessName, &quote.ServiceImageURL,
		&quote.DurationMinutes, &quote.StartsAt, &quote.EndsAt, &quote.Timezone,
		&quote.LocationLabel, &quote.ProviderLocationLabel, &quote.CountryCode,
		&quote.CurrencyCode, &quote.Locale, &quote.BaseAmountMinor,
		&quote.TotalAmountMinor, &quote.DepositAmountMinor, &quote.CustomerName,
		&quote.CustomerEmail, &quote.CustomerPhone, &quote.BookingNotes,
		&quote.ExpiresAt, &quote.CancellationPolicy, &quote.LatenessPolicy,
		&quote.AgreementTemplateFamilyID, &quote.AgreementTemplateVersionID,
		&quote.AgreementTitle, &quote.AgreementBookingSummaryJSON,
		&quote.AgreementResolvedDocumentJSON, &quote.AgreementSchemaVersion,
		&quote.AgreementRendererVersion, &quote.AgreementRenderedHTML,
		&quote.AgreementResolvedTermsHash, &quote.AgreementConfirmationMethod,
		&quote.AgreementTiming, &quote.StandaloneSignatureRequired,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bookingQuoteRecord{}, ErrNotFound
		}
		return bookingQuoteRecord{}, fmt.Errorf("lock booking quote: %w", err)
	}
	return quote, nil
}

func (r *Repository) getPublicServiceForBookingTx(ctx context.Context, tx pgx.Tx, slug string, serviceID uuid.UUID) (publicBookingServiceInfo, error) {
	return getPublicServiceForBooking(ctx, tx, slug, serviceID)
}

func validateBookingAgreementEvidence(quote bookingQuoteRecord, input CreatePublicBookingInput) (bookingAcceptanceEvidence, string, error) {
	if !quote.hasAgreement() && !quote.StandaloneSignatureRequired {
		return bookingAcceptanceEvidence{}, "not_required", nil
	}
	if quote.hasAgreement() && quote.AgreementTiming == "after_payment" {
		return bookingAcceptanceEvidence{}, "pending", nil
	}
	if !input.AgreementAccepted {
		return bookingAcceptanceEvidence{}, "", fmt.Errorf("agreement must be accepted before payment")
	}

	method := quote.AgreementConfirmationMethod
	if quote.StandaloneSignatureRequired {
		method = "signature"
	}
	evidence := bookingAcceptanceEvidence{method: method, accepted: true}
	if method == "confirmation" {
		return evidence, "accepted", nil
	}
	if method != "signature" {
		return bookingAcceptanceEvidence{}, "", fmt.Errorf("booking confirmation method is invalid")
	}
	evidence.signer = strings.TrimSpace(input.AgreementFullName)
	if evidence.signer == "" {
		return bookingAcceptanceEvidence{}, "", fmt.Errorf("agreement_full_name is required")
	}
	normalized, err := signature.NormalizeDataURL(input.AgreementSignatureDataURL)
	if err != nil {
		return bookingAcceptanceEvidence{}, "", err
	}
	evidence.signature = &normalized
	return evidence, "signed", nil
}

func (r *Repository) createBookingAgreementFromQuote(
	ctx context.Context,
	tx pgx.Tx,
	quote bookingQuoteRecord,
	bookingID uuid.UUID,
	customerID uuid.UUID,
	evidence bookingAcceptanceEvidence,
) error {
	if r.agreementTokens == nil {
		return fmt.Errorf("agreement token encryption is not configured")
	}
	agreementID := uuid.New()
	token, err := r.agreementTokens.Generate(quote.ClientID, agreementID)
	if err != nil {
		return fmt.Errorf("create agreement public token: %w", err)
	}
	status := "awaiting_customer"
	var completedAt any
	if quote.AgreementTiming == "before_payment" {
		status = "completed"
		completedAt = time.Now().UTC()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_instances (
			id, client_id, customer_id, booking_id, template_family_id, template_version_id,
			title_snapshot, booking_summary_snapshot, resolved_document_snapshot,
			schema_version_snapshot, renderer_version_snapshot, rendered_html_snapshot,
			resolved_terms_hash, confirmation_method, timing, status,
			public_token_hash, public_token_ciphertext, public_token_nonce,
			public_token_key_version, sent_to_email, completed_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
			$17,$18,$19,$20,$21,$22,NOW(),NOW()
		)
	`, agreementID, quote.ClientID, customerID, bookingID,
		quote.AgreementTemplateFamilyID, quote.AgreementTemplateVersionID,
		quote.AgreementTitle, quote.AgreementBookingSummaryJSON,
		quote.AgreementResolvedDocumentJSON, quote.AgreementSchemaVersion,
		quote.AgreementRendererVersion, quote.AgreementRenderedHTML,
		quote.AgreementResolvedTermsHash, quote.AgreementConfirmationMethod,
		quote.AgreementTiming, status, token.Hash, token.Ciphertext.Data,
		token.Ciphertext.Nonce, token.Ciphertext.KeyVersion, quote.CustomerEmail,
		completedAt,
	); err != nil {
		return fmt.Errorf("insert booking agreement: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
		VALUES ($1,$2,'created','system','created',NOW())
	`, uuid.New(), agreementID); err != nil {
		return fmt.Errorf("record agreement creation: %w", err)
	}
	if status == "completed" {
		if err := insertAgreementAcceptance(ctx, tx, agreementID, quote.AgreementResolvedTermsHash, evidence); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
			VALUES ($1,$2,'completed','customer','completed',NOW())
		`, uuid.New(), agreementID); err != nil {
			return fmt.Errorf("record agreement completion: %w", err)
		}
	}
	return nil
}

func insertAgreementAcceptance(
	ctx context.Context,
	tx pgx.Tx,
	agreementID uuid.UUID,
	termsHash string,
	evidence bookingAcceptanceEvidence,
) error {
	png := []byte{}
	var checksum string
	if evidence.signature != nil {
		png = evidence.signature.PNG
		checksum = evidence.signature.SHA256
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_acceptances (
			agreement_id, method, signer_name, signature_png, signature_sha256,
			accepted_at, resolved_terms_hash, created_at
		) VALUES ($1,$2,$3,$4,$5,NOW(),$6,NOW())
	`, agreementID, evidence.method, evidence.signer, png, checksum, termsHash); err != nil {
		return fmt.Errorf("store agreement acceptance: %w", err)
	}
	return nil
}

func insertStandaloneBookingSignature(ctx context.Context, tx pgx.Tx, bookingID uuid.UUID, evidence bookingAcceptanceEvidence) error {
	if evidence.signature == nil {
		return fmt.Errorf("booking signature is required")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO booking_standalone_signatures (
			booking_id, signer_name, signature_png, signature_sha256, accepted_at, created_at
		) VALUES ($1,$2,$3,$4,NOW(),NOW())
	`, bookingID, evidence.signer, evidence.signature.PNG, evidence.signature.SHA256); err != nil {
		return fmt.Errorf("store booking signature: %w", err)
	}
	return nil
}

func convertQuotePromotionReservations(ctx context.Context, tx pgx.Tx, quote bookingQuoteRecord, bookingID, customerID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO promotion_redemptions (
			id, promotion_id, client_id, booking_id, customer_id, customer_email,
			code_used, discount_amount_minor, currency_code, created_at
		)
		SELECT
			gen_random_uuid(), bqp.promotion_id, $2, $3, $4,
			bqp.customer_email_normalized, bqp.code_used, bqp.discount_amount_minor,
			bqp.currency_code, NOW()
		FROM booking_quote_promotions bqp
		WHERE bqp.booking_quote_id = $1
	`, quote.ID, quote.ClientID, bookingID, customerID); err != nil {
		return fmt.Errorf("convert quote promotion reservations: %w", err)
	}
	return nil
}

func (r *Repository) GetPublicBookingSummary(ctx context.Context, bookingToken string) (PublicBookingSummaryResponse, error) {
	const query = `
		SELECT
			b.id, b.public_token, b.source, cp.business_name, cp.handle_slug,
			COALESCE(cp.avatar_url, ''), c.full_name, c.email, c.phone,
			b.email_reminder_consent, b.whatsapp_consent, b.sms_consent,
			b.title, COALESCE(b.image_url, ''), b.duration_minutes,
			b.start_at, b.end_at, b.location_label, b.fulfillment_mode,
			b.provider_location_label, b.customer_location_label, b.travel_distance_meters,
			b.virtual_delivery_label, COALESCE(b.virtual_join_url, ''),
			COALESCE(b.virtual_instructions, ''), b.cancellation_policy_snapshot,
			b.lateness_policy_snapshot, b.original_amount_minor, b.discount_name,
			b.discount_source, b.discount_code, b.discount_type,
			b.discount_percentage_bps, b.discount_value_minor, b.discount_amount_minor,
			b.discounted_service_amount_minor, b.short_notice_fee_minor, b.travel_fee_minor,
			b.total_amount_minor, b.deposit_amount_minor, b.country_code, b.currency_code,
			b.timezone, cp.locale, b.status, b.payment_status, b.agreement_status,
			b.agreement_timing_snapshot, b.agreement_confirmation_method_snapshot,
			b.agreement_title_snapshot, b.standalone_signature_required_snapshot,
			b.reservation_expires_at, b.reservation_expired_at, b.reservation_expiry_reason,
			COALESCE(latest_payment.public_token, ''), COALESCE(latest_payment.provider, ''),
			COALESCE(latest_payment.reference, ''), COALESCE(cp.booking_contact_phone, '')
		FROM bookings b
		INNER JOIN client_profiles cp ON cp.client_id = b.client_id
		INNER JOIN customers c ON c.id = b.customer_id
		LEFT JOIN LATERAL (
			SELECT p.public_token, p.provider, p.reference
			FROM payments p WHERE p.booking_id = b.id
			ORDER BY p.created_at DESC LIMIT 1
		) latest_payment ON TRUE
		WHERE b.public_token = $1
	`
	var response PublicBookingSummaryResponse
	var bookingID uuid.UUID
	var startAt time.Time
	var endAt time.Time
	var durationMinutes int
	var travelDistance *int
	var originalAmount int64
	var discountValue int64
	var discountAmount int64
	var discountedAmount int64
	var shortNoticeFee int64
	var travelFee int64
	var totalAmount int64
	var depositAmount int64
	var reservationExpiresAt, reservationExpiredAt *time.Time
	if err := r.db.QueryRow(ctx, query, strings.TrimSpace(bookingToken)).Scan(
		&bookingID, &response.BookingToken, &response.Source, &response.ProviderName,
		&response.ProviderHandle, &response.ProviderAvatarURL, &response.CustomerName,
		&response.CustomerEmail, &response.CustomerPhone, &response.EmailReminderConsent,
		&response.WhatsAppConsent, &response.SMSConsent, &response.ServiceTitle,
		&response.ServiceImageURL, &durationMinutes, &startAt, &endAt,
		&response.LocationLabel, &response.FulfillmentMode,
		&response.ProviderLocationLabel, &response.CustomerLocationLabel,
		&travelDistance, &response.VirtualDeliveryLabel, &response.VirtualJoinURL,
		&response.VirtualInstructions, &response.CancellationPolicy,
		&response.LatenessPolicy, &originalAmount, &response.DiscountName,
		&response.DiscountSource, &response.DiscountCode, &response.DiscountType,
		&response.DiscountPercentageBPS, &discountValue, &discountAmount,
		&discountedAmount, &shortNoticeFee, &travelFee, &totalAmount, &depositAmount,
		&response.CountryCode, &response.CurrencyCode, &response.Timezone,
		&response.Locale, &response.Status, &response.PaymentStatus,
		&response.AgreementStatus, &response.AgreementTiming,
		&response.AgreementConfirmationMethod,
		&response.AgreementTemplateTitle, &response.StandaloneSignatureRequired,
		&reservationExpiresAt, &reservationExpiredAt, &response.ReservationExpiryReason,
		&response.PaymentToken, &response.PaymentProvider, &response.PaymentReference,
		&response.ProviderContactPhone,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicBookingSummaryResponse{}, ErrNotFound
		}
		return PublicBookingSummaryResponse{}, fmt.Errorf("get public booking summary: %w", err)
	}
	location, err := loadLocation(response.Timezone)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	localStart := startAt.In(location)
	localEnd := endAt.In(location)
	response.BookingID = bookingID.String()
	response.DurationLabel = fmt.Sprintf("%d mins", durationMinutes)
	response.DateLabel = localStart.Format("Monday, Jan 2")
	response.TimeLabel = fmt.Sprintf("%s - %s", localStart.Format("03:04 PM"), localEnd.Format("03:04 PM"))
	response.StartsAt = localStart.Format(time.RFC3339)
	response.EndsAt = localEnd.Format(time.RFC3339)
	response.TravelDistanceMeters = travelDistance
	response.OriginalAmountMinor = money.Minor(originalAmount)
	response.DiscountApplied = discountAmount > 0
	response.DiscountValueMinor = money.Minor(discountValue)
	response.DiscountAmountMinor = money.Minor(discountAmount)
	response.DiscountedServiceAmountMinor = money.Minor(discountedAmount)
	response.ShortNoticeFeeMinor = money.Minor(shortNoticeFee)
	response.TravelFeeMinor = money.Minor(travelFee)
	response.TotalAmountMinor = money.Minor(totalAmount)
	response.DepositAmountMinor = money.Minor(depositAmount)
	response.RemainingAmountMinor = money.Minor(totalAmount - depositAmount)
	if reservationExpiresAt != nil {
		response.ReservationExpiresAt = reservationExpiresAt.UTC().Format(time.RFC3339)
	}
	if reservationExpiredAt != nil {
		response.ReservationExpiredAt = reservationExpiredAt.UTC().Format(time.RFC3339)
	}
	response.DeliveryStatus, err = r.loadBookingDeliveryStatus(ctx, bookingID)
	if err != nil {
		return PublicBookingSummaryResponse{}, err
	}
	if !publicBookingSecured(response) {
		response.VirtualJoinURL = ""
		response.VirtualInstructions = ""
	}
	return response, nil
}

func publicBookingAgreementSatisfied(booking PublicBookingSummaryResponse) bool {
	agreementRequired := strings.TrimSpace(booking.AgreementTemplateTitle) != "" || booking.StandaloneSignatureRequired
	if !agreementRequired {
		return true
	}
	return booking.AgreementStatus == "accepted" || booking.AgreementStatus == "signed"
}

func publicBookingSecured(booking PublicBookingSummaryResponse) bool {
	return initialBookingObligationSatisfied(booking.PaymentStatus) && publicBookingAgreementSatisfied(booking)
}
