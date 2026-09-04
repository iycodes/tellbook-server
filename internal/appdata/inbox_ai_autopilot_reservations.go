package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type inboxAIReservationProgress struct {
	BookingSessionID     uuid.UUID
	AISessionID          uuid.UUID
	ClientID             uuid.UUID
	ProposalID           uuid.UUID
	ProposalRevision     int64
	ProposalHash         string
	ConfirmationID       uuid.UUID
	ConfirmationKey      uuid.UUID
	ConfirmedAt          time.Time
	EmailReminderConsent bool
	WhatsAppConsent      bool
	SMSConsent           bool
	QuoteToken           string
	ProviderHandle       string
	Customer             BookingCustomerDetails
	AgreementInstanceID  uuid.NullUUID
	QuoteBookingID       uuid.NullUUID
	SessionBookingID     uuid.NullUUID
}

func (r *Repository) ConfirmMarketplaceInboxAIBookingProposal(
	ctx context.Context,
	marketplaceCustomerID, conversationID, proposalID uuid.UUID,
	input ConfirmInboxAIBookingProposalInput,
) (InboxAIBookingConfirmationResult, error) {
	requestKey, err := parseInboxAIUUID(input.IdempotencyKey, "idempotency_key")
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	progress, found, err := r.loadInboxAIReservationProgress(
		ctx, marketplaceCustomerID, conversationID,
	)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	evidenceMessages := []InboxMessage{}
	evidenceReplayed := found
	if found {
		if err := validateInboxAIReservationProgress(progress, proposalID, requestKey, input); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
		if progress.SessionBookingID.Valid {
			return r.loadFinalizedInboxAIReservationResult(
				ctx, marketplaceCustomerID, conversationID, progress, true,
			)
		}
	} else {
		evidence, evidenceErr := r.recordMarketplaceInboxAIBookingConfirmationEvidence(
			ctx, marketplaceCustomerID, conversationID, proposalID, input,
		)
		if evidenceErr != nil {
			return InboxAIBookingConfirmationResult{}, evidenceErr
		}
		evidenceMessages = evidence.Messages
		evidenceReplayed = evidence.Replayed
		progress, found, err = r.loadInboxAIReservationProgress(
			ctx, marketplaceCustomerID, conversationID,
		)
		if err != nil || !found {
			if err == nil {
				err = ErrInboxAIProposalStale
			}
			return InboxAIBookingConfirmationResult{}, err
		}
		if err := validateInboxAIReservationProgress(progress, proposalID, requestKey, input); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
	}

	authority := &InboxAutopilotReservationAuthority{
		ConversationID: conversationID, BookingSessionID: progress.BookingSessionID,
		ConfirmationID: progress.ConfirmationID, ProposalID: progress.ProposalID,
		ProposalRevision: progress.ProposalRevision, ProposalHash: progress.ProposalHash,
	}
	var agreementID *uuid.UUID
	if progress.AgreementInstanceID.Valid {
		value := progress.AgreementInstanceID.UUID
		agreementID = &value
	}
	reservation, err := r.BookingApplication().CreateReservation(ctx, CreateBookingReservationCommand{
		ProviderHandle: progress.ProviderHandle, QuoteToken: progress.QuoteToken,
		Authority: BookingReservationAuthorityAutopilot, Source: "marketplace",
		Customer: progress.Customer,
		NotificationConsent: BookingNotificationConsent{
			EmailReminder: progress.EmailReminderConsent,
			WhatsApp:      progress.WhatsAppConsent,
			SMS:           progress.SMSConsent,
		},
		MarketplaceCustomerID: &marketplaceCustomerID,
		PrebookingAgreementID: agreementID,
		AutopilotAuthority:    authority,
	})
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	result, err := r.finalizeInboxAIReservation(
		ctx, marketplaceCustomerID, conversationID, progress, reservation,
	)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	result.Messages = append(evidenceMessages, result.Messages...)
	result.Replayed = evidenceReplayed || result.Replayed
	return result, nil
}

func (r *Repository) loadInboxAIReservationProgress(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
) (inboxAIReservationProgress, bool, error) {
	var progress inboxAIReservationProgress
	err := r.db.QueryRow(ctx, `
		SELECT session.id, session.ai_session_id, session.client_id,
			session.proposal_id, session.proposal_revision, session.proposal_hash,
			confirmation.id, confirmation.idempotency_key, confirmation.confirmed_at,
			confirmation.email_reminder_consent, confirmation.whatsapp_consent,
			confirmation.sms_consent,
			quote.public_token, profile.handle_slug,
			quote.customer_name_snapshot, quote.customer_email_normalized,
			quote.customer_phone_snapshot, quote.booking_notes_snapshot,
			session.agreement_instance_id, quote.booking_id, session.booking_id
		FROM inbox_ai_booking_sessions session
		INNER JOIN inbox_conversations conversation ON conversation.id=session.conversation_id
		INNER JOIN inbox_ai_booking_confirmations confirmation ON confirmation.id=session.confirmation_id
		INNER JOIN booking_quotes quote ON quote.id=session.quote_id
		INNER JOIN client_profiles profile ON profile.client_id=session.client_id
		WHERE session.conversation_id=$1 AND session.marketplace_customer_id=$2
			AND conversation.marketplace_customer_id=$2
	`, conversationID, marketplaceCustomerID).Scan(
		&progress.BookingSessionID, &progress.AISessionID, &progress.ClientID,
		&progress.ProposalID, &progress.ProposalRevision, &progress.ProposalHash,
		&progress.ConfirmationID, &progress.ConfirmationKey, &progress.ConfirmedAt,
		&progress.EmailReminderConsent, &progress.WhatsAppConsent,
		&progress.SMSConsent, &progress.QuoteToken,
		&progress.ProviderHandle, &progress.Customer.FullName, &progress.Customer.Email,
		&progress.Customer.Phone, &progress.Customer.Notes, &progress.AgreementInstanceID,
		&progress.QuoteBookingID, &progress.SessionBookingID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return inboxAIReservationProgress{}, false, nil
	}
	if err != nil {
		return inboxAIReservationProgress{}, false, fmt.Errorf("load inbox reservation progress: %w", err)
	}
	return progress, true, nil
}

func validateInboxAIReservationProgress(
	progress inboxAIReservationProgress,
	proposalID, requestKey uuid.UUID,
	input ConfirmInboxAIBookingProposalInput,
) error {
	if progress.ProposalID != proposalID || progress.ProposalRevision != input.ProposalRevision ||
		progress.ProposalHash != strings.TrimSpace(input.ProposalHash) {
		return ErrInboxAIProposalStale
	}
	if !input.ContactDetailsConfirmed || progress.EmailReminderConsent != input.EmailReminderConsent ||
		progress.WhatsAppConsent != input.WhatsAppConsent ||
		progress.SMSConsent != input.SMSConsent {
		return ErrInboxIdempotencyConflict
	}
	if requestKey == progress.ConfirmationKey {
		return nil
	}
	// A different key with identical evidence is a semantic replay of the one
	// immutable proposal confirmation; it must converge on the same booking.
	return nil
}

func (r *Repository) finalizeInboxAIReservation(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	progress inboxAIReservationProgress,
	reservation BookingReservationResult,
) (InboxAIBookingConfirmationResult, error) {
	bookingID, err := uuid.Parse(reservation.Booking.BookingID)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, fmt.Errorf("canonical reservation booking id is invalid: %w", err)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var conversationLock int
	if err := tx.QueryRow(ctx, `
		SELECT 1 FROM inbox_conversations
		WHERE id=$1 AND marketplace_customer_id=$2 FOR UPDATE
	`, conversationID, marketplaceCustomerID).Scan(&conversationLock); errors.Is(err, pgx.ErrNoRows) {
		return InboxAIBookingConfirmationResult{}, ErrNotFound
	} else if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	record, found, err := loadInboxAIBookingSessionRecord(
		ctx, tx, marketplaceCustomerID, conversationID, true,
	)
	if err != nil || !found {
		if err == nil {
			err = ErrInboxAIProposalStale
		}
		return InboxAIBookingConfirmationResult{}, err
	}
	if record.ID != progress.BookingSessionID || !record.ConfirmationID.Valid ||
		record.ConfirmationID.UUID != progress.ConfirmationID || !record.QuoteID.Valid ||
		!record.ProposalID.Valid || record.ProposalID.UUID != progress.ProposalID ||
		record.ProposalRevision != progress.ProposalRevision || record.ProposalHash != progress.ProposalHash {
		return InboxAIBookingConfirmationResult{}, ErrInboxAIProposalStale
	}
	if record.BookingID.Valid {
		if record.BookingID.UUID != bookingID {
			return InboxAIBookingConfirmationResult{}, ErrInboxAIProposalStale
		}
		result, loadErr := r.loadFinalizedInboxAIReservationResultTx(
			ctx, tx, marketplaceCustomerID, conversationID, progress, true,
		)
		if loadErr != nil {
			return InboxAIBookingConfirmationResult{}, loadErr
		}
		if err := tx.Commit(ctx); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
		return result, nil
	}
	var quoteBookingID uuid.NullUUID
	if err := tx.QueryRow(ctx, `SELECT booking_id FROM booking_quotes WHERE id=$1 FOR UPDATE`, record.QuoteID.UUID).Scan(
		&quoteBookingID,
	); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	if !quoteBookingID.Valid || quoteBookingID.UUID != bookingID {
		return InboxAIBookingConfirmationResult{}, ErrInboxAIProposalStale
	}
	state := inboxAIReservationState(reservation.Lifecycle.NextStep)
	endsAt, err := time.Parse(time.RFC3339, reservation.Booking.EndsAt)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, fmt.Errorf("canonical reservation end time is invalid: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_booking_confirmations
		SET booking_id=$2, reservation_created_at=NOW()
		WHERE id=$1 AND booking_id IS NULL
	`, progress.ConfirmationID, bookingID); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	nextRevision := record.Revision + 1
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_booking_sessions SET booking_id=$2, state=$3,
			current_action_id=$4, revision=$5, expires_at=$6, updated_at=NOW()
		WHERE id=$1 AND revision=$7
	`, record.ID, bookingID, state, progress.ConfirmationID, nextRevision,
		endsAt, record.Revision); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET state=$2, revision=revision+1,
			expires_at=$3,
			completed_at=CASE WHEN $2='completed' THEN NOW() ELSE NULL END,
			updated_at=NOW()
		WHERE id=$1
	`, record.AISessionID, state, endsAt); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	reservationPresentation, nextStepPresentation, content, nextStepContent, err :=
		inboxAIReservationPresentations(reservation, bookingID)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	reservationMessage, summary, err := r.appendInboxSystemWorkflowMessageTx(
		ctx, tx, record.ClientID, marketplaceCustomerID, conversationID,
		uuid.NewSHA1(uuid.NameSpaceOID, []byte("inbox-reservation:"+progress.ConfirmationID.String())),
		bookingID, content, reservationPresentation,
	)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	nextStepMessage, summary, err := r.appendInboxSystemWorkflowMessageTx(
		ctx, tx, record.ClientID, marketplaceCustomerID, conversationID,
		uuid.NewSHA1(uuid.NameSpaceOID, []byte("inbox-next-step:"+progress.ConfirmationID.String())),
		bookingID, nextStepContent, nextStepPresentation,
	)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	safeInput := map[string]any{
		"proposal_id": progress.ProposalID.String(), "proposal_revision": progress.ProposalRevision,
		"proposal_hash": progress.ProposalHash, "contact_details_confirmed": true,
		"email_reminder_consent": progress.EmailReminderConsent,
		"whatsapp_consent":       progress.WhatsAppConsent, "sms_consent": progress.SMSConsent,
	}
	if err := auditInboxAIBookingAction(
		ctx, tx, record.AISessionID, conversationID, record.ClientID,
		progress.ConfirmationKey, "confirm_booking_proposal", safeInput,
		map[string]any{
			"confirmation_id": progress.ConfirmationID.String(), "reservation_created": true,
			"booking_id": bookingID.String(), "booking_status": reservation.Booking.Status,
			"next_step": reservation.Lifecycle.NextStep,
		},
	); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	if err := appendInboxAISessionEvent(
		ctx, tx, conversationID, record.ClientID, marketplaceCustomerID,
		state, "reservation_created",
	); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	return InboxAIBookingConfirmationResult{
		Workflow: workflow, Messages: []InboxMessage{reservationMessage, nextStepMessage},
		Conversation: &summary, ConfirmationID: progress.ConfirmationID.String(),
		ConfirmedAt: progress.ConfirmedAt, ReservationCreated: true,
		BookingID: bookingID.String(), BookingStatus: reservation.Booking.Status,
		NextStep: reservation.Lifecycle.NextStep,
	}, nil
}

func inboxAIReservationState(nextStep BookingNextStep) string {
	switch nextStep {
	case BookingNextStepPayment:
		return "awaiting_payment"
	case BookingNextStepAgreement:
		return "awaiting_after_payment_agreement"
	case BookingNextStepProviderConfirmation:
		return "awaiting_provider_confirmation"
	default:
		return "completed"
	}
}

func inboxAIReservationPresentations(
	reservation BookingReservationResult,
	bookingID uuid.UUID,
) (InboxMessagePresentation, InboxMessagePresentation, string, string, error) {
	statusLabel := "Reservation created"
	content := "Your reservation has been created."
	nextLabel := "View reservation"
	nextContent := "Review your reservation details."
	switch reservation.Lifecycle.NextStep {
	case BookingNextStepPayment:
		statusLabel = "Reserved — payment required"
		content = "Your reservation has been created, but payment is still required."
		nextLabel = "Continue to payment"
		nextContent = "Complete payment to keep this reservation moving."
	case BookingNextStepAgreement:
		statusLabel = "Reserved — agreement required"
		content = "Your reservation has been created, but the agreement is still required."
		nextLabel = "Complete agreement"
		nextContent = "Review and complete the required agreement."
	case BookingNextStepProviderConfirmation:
		statusLabel = "Awaiting provider confirmation"
		content = "Your reservation has been created and is awaiting provider confirmation."
		nextLabel = "View reservation"
		nextContent = "The provider has been notified and still needs to confirm."
	case BookingNextStepComplete:
		statusLabel = "Confirmed"
		content = "Your booking is confirmed."
		nextLabel = "View confirmed booking"
		nextContent = "Your booking requirements are complete."
	}
	reservationPayload := map[string]any{
		"booking_id": bookingID.String(), "status": reservation.Booking.Status,
		"status_label": statusLabel, "service_title": reservation.Booking.ServiceTitle,
		"starts_at": reservation.Booking.StartsAt, "ends_at": reservation.Booking.EndsAt,
		"timezone": reservation.Booking.Timezone,
	}
	if reservation.Booking.ReservationExpiresAt != "" {
		reservationPayload["expires_at"] = reservation.Booking.ReservationExpiresAt
	}
	reservationData, err := json.Marshal(reservationPayload)
	if err != nil {
		return InboxMessagePresentation{}, InboxMessagePresentation{}, "", "", err
	}
	nextStepPayload := map[string]any{
		"booking_id": bookingID.String(), "next_step": reservation.Lifecycle.NextStep,
		"label": nextLabel, "href": "/booking/" + bookingID.String(),
	}
	if reservation.Lifecycle.NextStep == BookingNextStepPayment && reservation.Booking.ReservationExpiresAt != "" {
		nextStepPayload["expires_at"] = reservation.Booking.ReservationExpiresAt
	}
	nextStepData, err := json.Marshal(nextStepPayload)
	if err != nil {
		return InboxMessagePresentation{}, InboxMessagePresentation{}, "", "", err
	}
	return InboxMessagePresentation{Kind: "reservation_created", Version: 1, Data: reservationData},
		InboxMessagePresentation{Kind: "booking_next_step", Version: 1, Data: nextStepData},
		content, nextContent, nil
}

func (r *Repository) loadFinalizedInboxAIReservationResult(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	progress inboxAIReservationProgress,
	replayed bool,
) (InboxAIBookingConfirmationResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := r.loadFinalizedInboxAIReservationResultTx(
		ctx, tx, marketplaceCustomerID, conversationID, progress, replayed,
	)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	return result, nil
}

func (r *Repository) loadFinalizedInboxAIReservationResultTx(
	ctx context.Context,
	tx pgx.Tx,
	marketplaceCustomerID, conversationID uuid.UUID,
	progress inboxAIReservationProgress,
	replayed bool,
) (InboxAIBookingConfirmationResult, error) {
	var bookingID uuid.UUID
	var bookingStatus, paymentStatus, agreementStatus, agreementTitle string
	var standaloneSignature bool
	if err := tx.QueryRow(ctx, `
		SELECT confirmation.booking_id, booking.status, booking.payment_status,
			booking.agreement_status, COALESCE(booking.agreement_title_snapshot,''),
			booking.standalone_signature_required_snapshot
		FROM inbox_ai_booking_confirmations confirmation
		INNER JOIN bookings booking ON booking.id=confirmation.booking_id
		WHERE confirmation.id=$1 AND confirmation.conversation_id=$2
			AND confirmation.marketplace_customer_id=$3
	`, progress.ConfirmationID, conversationID, marketplaceCustomerID).Scan(
		&bookingID, &bookingStatus, &paymentStatus, &agreementStatus,
		&agreementTitle, &standaloneSignature,
	); errors.Is(err, pgx.ErrNoRows) {
		return InboxAIBookingConfirmationResult{}, ErrInboxAIProposalStale
	} else if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	lifecycle := ResolveBookingLifecycle(PublicBookingSummaryResponse{
		Status: bookingStatus, PaymentStatus: paymentStatus, AgreementStatus: agreementStatus,
		AgreementTemplateTitle: agreementTitle, StandaloneSignatureRequired: standaloneSignature,
	})
	messages, err := loadInboxAIReservationMessages(ctx, tx, conversationID, bookingID)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	summary, err := loadMarketplaceConversationSummary(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	return InboxAIBookingConfirmationResult{
		Workflow: workflow, Messages: messages, Conversation: &summary,
		ConfirmationID: progress.ConfirmationID.String(), ConfirmedAt: progress.ConfirmedAt,
		ReservationCreated: true, BookingID: bookingID.String(), BookingStatus: bookingStatus,
		NextStep: lifecycle.NextStep, Replayed: replayed,
	}, nil
}

func loadInboxAIReservationMessages(
	ctx context.Context,
	q inboxQueryer,
	conversationID, bookingID uuid.UUID,
) ([]InboxMessage, error) {
	rows, err := q.Query(ctx, `
		SELECT id, conversation_id, sender_type, sender_id, client_message_id, booking_id,
			content, message_type, presentation, sent_at
		FROM inbox_messages
		WHERE conversation_id=$1 AND booking_id=$2
			AND presentation->>'kind' IN ('reservation_created','booking_next_step')
		ORDER BY sequence
	`, conversationID, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]InboxMessage, 0, 2)
	for rows.Next() {
		var message InboxMessage
		var id, loadedConversationID uuid.UUID
		var senderID, clientMessageID, loadedBookingID uuid.NullUUID
		if err := rows.Scan(
			&id, &loadedConversationID, &message.SenderType, &senderID, &clientMessageID,
			&loadedBookingID, &message.Content, &message.MessageType, &message.Presentation,
			&message.SentAt,
		); err != nil {
			return nil, err
		}
		message.ID = id.String()
		message.ConversationID = loadedConversationID.String()
		if senderID.Valid {
			message.SenderID = senderID.UUID.String()
		}
		if clientMessageID.Valid {
			message.ClientMessageID = clientMessageID.UUID.String()
		}
		if loadedBookingID.Valid {
			message.BookingID = loadedBookingID.UUID.String()
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(messages) != 2 {
		return nil, fmt.Errorf("finalized inbox reservation transcript is incomplete")
	}
	return messages, nil
}
