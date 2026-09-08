package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInboxAIBookingDetailsRequired = errors.New("authenticated booking details are incomplete")
	ErrInboxAIProposalStale          = errors.New("booking proposal is stale")
	ErrInboxAIAgreementRequired      = errors.New("booking agreement evidence is required")
)

type inboxAIBookingSessionRecord struct {
	ID                      uuid.UUID
	AISessionID             uuid.UUID
	ConversationID          uuid.UUID
	ClientID                uuid.UUID
	MarketplaceCustomerID   uuid.UUID
	State                   string
	SelectedServiceID       uuid.NullUUID
	SelectedStartAt         *time.Time
	SelectedEndAt           *time.Time
	QuoteID                 uuid.NullUUID
	QuoteToken              string
	QuoteExpiresAt          *time.Time
	CurrencyCode            string
	TotalAmountMinor        *int64
	ProposalID              uuid.NullUUID
	ProposalRevision        int64
	ProposalHash            string
	CustomerDetailsRevision *time.Time
	AgreementInstanceID     uuid.NullUUID
	ConfirmationID          uuid.NullUUID
	BookingID               uuid.NullUUID
	CurrentActionID         uuid.UUID
	Revision                int64
	ExpiresAt               time.Time
}

func (r *Repository) GetMarketplaceInboxAIBookingWorkflow(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
) (InboxAIBookingWorkflow, error) {
	return r.loadInboxAIBookingWorkflow(ctx, r.db, marketplaceCustomerID, conversationID)
}

func (r *Repository) loadInboxAIBookingWorkflow(
	ctx context.Context,
	q inboxQueryer,
	marketplaceCustomerID, conversationID uuid.UUID,
) (InboxAIBookingWorkflow, error) {
	var clientID uuid.UUID
	var emailReminderDefault, whatsappReminderDefault bool
	if err := q.QueryRow(ctx, `
		SELECT conversation.client_id,
			COALESCE(preference.booking_email, TRUE),
			COALESCE(preference.booking_whatsapp, FALSE)
		FROM inbox_conversations conversation
		LEFT JOIN marketplace_notification_preferences preference
			ON preference.marketplace_customer_id=conversation.marketplace_customer_id
		WHERE conversation.id=$1 AND conversation.marketplace_customer_id=$2
			AND conversation.disabled_at IS NULL
	`, conversationID, marketplaceCustomerID).Scan(
		&clientID, &emailReminderDefault, &whatsappReminderDefault,
	); errors.Is(err, pgx.ErrNoRows) {
		return InboxAIBookingWorkflow{}, ErrNotFound
	} else if err != nil {
		return InboxAIBookingWorkflow{}, fmt.Errorf("authorize inbox booking workflow: %w", err)
	}
	policy, err := loadInboxAIPolicyRecord(ctx, q, clientID)
	if err != nil {
		return InboxAIBookingWorkflow{}, err
	}
	control, err := loadInboxAIConversationControl(
		ctx, q, clientID, conversationID, policy, r.inboxAIAutomationAvailable(clientID),
	)
	if err != nil {
		return InboxAIBookingWorkflow{}, err
	}
	workflow := InboxAIBookingWorkflow{
		Available: control.EffectiveMode == InboxAIModeAutopilot && control.State == "active" &&
			!policy.paused && len(policy.enabledServiceIDs) > 0,
		Mode: control.EffectiveMode, State: "collecting_preferences", Revision: 0,
		Services: []InboxAIServiceOption{},
		Requirements: InboxAIBookingRequirements{
			MissingCustomerFields:   []string{},
			EmailReminderDefault:    emailReminderDefault,
			WhatsAppReminderDefault: whatsappReminderDefault,
		},
	}
	options, err := loadInboxAIServiceOptions(ctx, q, clientID, policy.enabledServiceIDs)
	if err != nil {
		return InboxAIBookingWorkflow{}, err
	}
	for _, option := range options {
		if option.Enabled && containsInboxAIMode(option.EligibleModes, InboxAIModeAutopilot) {
			workflow.Services = append(workflow.Services, option)
		}
	}

	record, found, err := loadInboxAIBookingSessionRecord(
		ctx, q, marketplaceCustomerID, conversationID, false,
	)
	if err != nil {
		return InboxAIBookingWorkflow{}, err
	}
	if !found {
		workflow.Requirements.MissingCustomerFields, err = loadInboxAIMissingCustomerFields(
			ctx, q, marketplaceCustomerID,
		)
		return workflow, err
	}
	workflow.State = record.State
	workflow.Revision = record.Revision
	workflow.ActionID = record.CurrentActionID.String()
	workflow.Requirements.MissingCustomerFields, err = loadInboxAIMissingCustomerFields(
		ctx, q, marketplaceCustomerID,
	)
	if err != nil {
		return InboxAIBookingWorkflow{}, err
	}
	if record.SelectedServiceID.Valid {
		var fulfillmentMode string
		var standaloneSignatureRequired bool
		var agreementFamilyID uuid.NullUUID
		var agreementTiming, agreementTitle, agreementMethod string
		if err := q.QueryRow(ctx, `
			SELECT service.fulfillment_mode, service.standalone_signature_required,
				service.agreement_template_family_id, COALESCE(service.agreement_timing,''),
				COALESCE(family.title,''), COALESCE(family.confirmation_method,'')
			FROM services service
			LEFT JOIN agreement_template_families family
				ON family.id=service.agreement_template_family_id
			WHERE service.id=$1 AND service.client_id=$2
		`, record.SelectedServiceID.UUID, clientID).Scan(
			&fulfillmentMode, &standaloneSignatureRequired, &agreementFamilyID,
			&agreementTiming, &agreementTitle, &agreementMethod,
		); err != nil {
			return InboxAIBookingWorkflow{}, fmt.Errorf("load inbox booking requirements: %w", err)
		}
		workflow.Requirements.CustomerLocationRequired = fulfillmentMode == "customer_location"
		workflow.Requirements.AgreementRequired = agreementFamilyID.Valid && agreementTiming == "before_payment"
		if standaloneSignatureRequired {
			workflow.Requirements.AgreementRequired = true
			agreementTitle = "Signature required"
			agreementMethod = "signature"
		}
		workflow.Requirements.AgreementTitle = agreementTitle
		workflow.Requirements.AgreementConfirmationMode = agreementMethod
	}
	if record.ProposalID.Valid {
		proposal, proposalErr := loadInboxAIBookingProposal(ctx, q, record)
		if proposalErr != nil {
			return InboxAIBookingWorkflow{}, proposalErr
		}
		workflow.Proposal = &proposal
	}
	if record.BookingID.Valid {
		var booking PublicBookingSummaryResponse
		var reservationExpiresAt *time.Time
		if err := q.QueryRow(ctx, `
			SELECT status, payment_status, agreement_status,
				COALESCE(agreement_title_snapshot,''), standalone_signature_required_snapshot,
				reservation_expires_at
			FROM bookings
			WHERE id=$1 AND client_id=$2 AND marketplace_customer_id=$3
		`, record.BookingID.UUID, clientID, marketplaceCustomerID).Scan(
			&booking.Status, &booking.PaymentStatus, &booking.AgreementStatus,
			&booking.AgreementTemplateTitle, &booking.StandaloneSignatureRequired,
			&reservationExpiresAt,
		); errors.Is(err, pgx.ErrNoRows) {
			return InboxAIBookingWorkflow{}, ErrInboxAIProposalStale
		} else if err != nil {
			return InboxAIBookingWorkflow{}, fmt.Errorf("load inbox reservation: %w", err)
		}
		lifecycle := ResolveBookingLifecycle(booking)
		workflow.Available = false
		workflow.Reservation = &InboxAIBookingReservation{
			BookingID: record.BookingID.UUID.String(), BookingStatus: booking.Status,
			NextStep:  lifecycle.NextStep,
			Href:      "/booking/" + record.BookingID.UUID.String(),
			ExpiresAt: reservationExpiresAt,
		}
	}
	return workflow, nil
}

func containsInboxAIMode(modes []string, target string) bool {
	for _, mode := range modes {
		if mode == target {
			return true
		}
	}
	return false
}

func loadInboxAIMissingCustomerFields(
	ctx context.Context,
	q inboxQueryer,
	marketplaceCustomerID uuid.UUID,
) ([]string, error) {
	var fullName, email, phone string
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(full_name,''),
			CASE WHEN email_verified_at IS NOT NULL THEN COALESCE(email,'') ELSE '' END,
			CASE WHEN phone_verified_at IS NOT NULL THEN COALESCE(phone_e164,'') ELSE '' END
		FROM marketplace_customers WHERE id=$1
	`, marketplaceCustomerID).Scan(&fullName, &email, &phone); err != nil {
		return nil, fmt.Errorf("load authenticated booking details: %w", err)
	}
	missing := make([]string, 0, 3)
	if strings.TrimSpace(fullName) == "" {
		missing = append(missing, "full_name")
	}
	if strings.TrimSpace(email) == "" {
		missing = append(missing, "email")
	}
	if strings.TrimSpace(phone) == "" {
		missing = append(missing, "phone")
	}
	return missing, nil
}

func loadInboxAIBookingSessionRecord(
	ctx context.Context,
	q inboxQueryer,
	marketplaceCustomerID, conversationID uuid.UUID,
	forUpdate bool,
) (inboxAIBookingSessionRecord, bool, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	var record inboxAIBookingSessionRecord
	err := q.QueryRow(ctx, `
		SELECT id, ai_session_id, conversation_id, client_id, marketplace_customer_id,
			state, selected_service_id, selected_start_at, selected_end_at, quote_id,
			quote_token, quote_expires_at, currency_code, total_amount_minor,
			proposal_id, proposal_revision, proposal_hash,
			proposal_customer_details_revision, agreement_instance_id,
			confirmation_id, booking_id, current_action_id, revision, expires_at
		FROM inbox_ai_booking_sessions
		WHERE conversation_id=$1 AND marketplace_customer_id=$2`+lock,
		conversationID, marketplaceCustomerID,
	).Scan(
		&record.ID, &record.AISessionID, &record.ConversationID, &record.ClientID,
		&record.MarketplaceCustomerID, &record.State, &record.SelectedServiceID,
		&record.SelectedStartAt, &record.SelectedEndAt, &record.QuoteID,
		&record.QuoteToken, &record.QuoteExpiresAt, &record.CurrencyCode,
		&record.TotalAmountMinor, &record.ProposalID, &record.ProposalRevision,
		&record.ProposalHash, &record.CustomerDetailsRevision,
		&record.AgreementInstanceID, &record.ConfirmationID, &record.BookingID,
		&record.CurrentActionID, &record.Revision, &record.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return inboxAIBookingSessionRecord{}, false, nil
	}
	if err != nil {
		return inboxAIBookingSessionRecord{}, false, fmt.Errorf("load inbox booking session: %w", err)
	}
	return record, true, nil
}

func loadInboxAIBookingProposal(
	ctx context.Context,
	q inboxQueryer,
	record inboxAIBookingSessionRecord,
) (InboxAIBookingProposal, error) {
	var proposal InboxAIBookingProposal
	var totalAmount int64
	var agreementRequired, agreementCompleted bool
	var agreementTitle, agreementMethod, agreementHTML string
	var confirmationID uuid.NullUUID
	var confirmedAt *time.Time
	var contactDetailsConfirmed, emailReminderConsent, whatsappConsent, smsConsent bool
	if err := q.QueryRow(ctx, `
		SELECT quote.service_id, quote.service_title, quote.appointment_start_at,
			quote.appointment_end_at, quote.timezone, quote.fulfillment_mode,
			quote.location_label, quote.currency_code, quote.total_amount_minor,
			COALESCE(agreement.title_snapshot,''),
			COALESCE(agreement.confirmation_method,''),
			COALESCE(agreement.rendered_html_snapshot,''),
			agreement.id IS NOT NULL,
			COALESCE(agreement.status='completed',false),
			confirmation.id, confirmation.confirmed_at,
			COALESCE(confirmation.contact_details_confirmed,false),
			COALESCE(confirmation.email_reminder_consent,false),
			COALESCE(confirmation.whatsapp_consent,false),
			COALESCE(confirmation.sms_consent,false)
		FROM booking_quotes quote
		LEFT JOIN agreement_instances agreement ON agreement.id=$2
		LEFT JOIN inbox_ai_booking_confirmations confirmation ON confirmation.id=$3
		WHERE quote.id=$1
	`, record.QuoteID, nullableInboxUUID(record.AgreementInstanceID),
		nullableInboxUUID(record.ConfirmationID)).Scan(
		&proposal.ServiceID, &proposal.ServiceTitle, &proposal.StartsAt, &proposal.EndsAt,
		&proposal.Timezone, &proposal.FulfillmentMode, &proposal.LocationLabel,
		&proposal.CurrencyCode, &totalAmount, &agreementTitle,
		&agreementMethod, &agreementHTML, &agreementRequired, &agreementCompleted,
		&confirmationID, &confirmedAt, &contactDetailsConfirmed, &emailReminderConsent,
		&whatsappConsent, &smsConsent,
	); err != nil {
		return InboxAIBookingProposal{}, fmt.Errorf("load inbox booking proposal: %w", err)
	}
	proposal.ID = record.ProposalID.UUID.String()
	proposal.Revision = record.ProposalRevision
	proposal.Hash = record.ProposalHash
	proposal.ExpiresAt = *record.QuoteExpiresAt
	proposal.TotalAmountMinor = money.Minor(totalAmount)
	proposal.PriceLabel, _ = formatMarketMoney(totalAmount, "NG", proposal.CurrencyCode)
	if proposal.PriceLabel == "" {
		proposal.PriceLabel = fmt.Sprintf("%s %d", proposal.CurrencyCode, totalAmount)
	}
	if totalAmount > 0 {
		proposal.PaymentRequirement = "Payment required after reservation"
	} else {
		proposal.PaymentRequirement = "No payment required"
	}
	proposal.Agreement = InboxAIBookingAgreement{
		Required: agreementRequired, Completed: agreementCompleted, Title: agreementTitle,
		ConfirmationMethod: agreementMethod, RenderedHTML: agreementHTML,
	}
	if agreementRequired {
		if agreementCompleted {
			proposal.AgreementRequirement = "Agreement completed"
		} else {
			proposal.AgreementRequirement = "Agreement required before confirmation"
		}
	} else {
		proposal.AgreementRequirement = "No agreement required before confirmation"
	}
	proposal.Confirmed = confirmationID.Valid
	if confirmationID.Valid {
		proposal.ConfirmationID = confirmationID.UUID.String()
		proposal.ConfirmedAt = confirmedAt
		proposal.ContactDetailsConfirmed = contactDetailsConfirmed
		proposal.EmailReminderConsent = emailReminderConsent
		proposal.WhatsAppConsent = whatsappConsent
		proposal.SMSConsent = smsConsent
	}
	return proposal, nil
}

func (r *Repository) lockInboxAIBookingAuthority(
	ctx context.Context,
	tx pgx.Tx,
	marketplaceCustomerID, conversationID uuid.UUID,
	requireSession bool,
) (inboxAIPolicyRecord, InboxAIConversationControl, inboxAIBookingSessionRecord, error) {
	var clientID uuid.UUID
	var disabled bool
	if err := tx.QueryRow(ctx, `
		SELECT client_id, disabled_at IS NOT NULL FROM inbox_conversations
		WHERE id=$1 AND marketplace_customer_id=$2 FOR UPDATE
	`, conversationID, marketplaceCustomerID).Scan(&clientID, &disabled); errors.Is(err, pgx.ErrNoRows) {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, ErrNotFound
	} else if err != nil {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, fmt.Errorf("lock inbox booking conversation: %w", err)
	}
	if disabled {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, ErrInboxConversationDisabled
	}
	var policyLock int
	if err := tx.QueryRow(ctx, `
		SELECT 1 FROM inbox_ai_policies WHERE client_id=$1 FOR SHARE
	`, clientID).Scan(&policyLock); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, fmt.Errorf("lock inbox AI policy: %w", err)
	}
	policy, err := loadInboxAIPolicyRecord(ctx, tx, clientID)
	if err != nil {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, err
	}
	control, err := loadInboxAIConversationControl(
		ctx, tx, clientID, conversationID, policy, r.inboxAIAutomationAvailable(clientID),
	)
	if err != nil {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, err
	}
	if control.EffectiveMode != InboxAIModeAutopilot || control.State != "active" || policy.paused {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, ErrInboxAIControlBlocked
	}
	record, found, err := loadInboxAIBookingSessionRecord(
		ctx, tx, marketplaceCustomerID, conversationID, requireSession,
	)
	if err != nil {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, err
	}
	if requireSession && !found {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, ErrInboxAIProposalStale
	}
	if found && record.ClientID != clientID {
		return inboxAIPolicyRecord{}, InboxAIConversationControl{}, inboxAIBookingSessionRecord{}, ErrInboxAIProposalStale
	}
	return policy, control, record, nil
}

func canonicalInboxAIProposalHash(
	proposal InboxAIBookingProposal,
	quoteToken string,
	customerDetailsRevision time.Time,
) (string, error) {
	encoded, err := json.Marshal(struct {
		ID                      string      `json:"id"`
		Revision                int64       `json:"revision"`
		QuoteToken              string      `json:"quote_token"`
		ExpiresAt               time.Time   `json:"expires_at"`
		ServiceID               string      `json:"service_id"`
		StartsAt                time.Time   `json:"starts_at"`
		EndsAt                  time.Time   `json:"ends_at"`
		Timezone                string      `json:"timezone"`
		FulfillmentMode         string      `json:"fulfillment_mode"`
		LocationLabel           string      `json:"location_label"`
		CurrencyCode            string      `json:"currency_code"`
		TotalAmount             money.Minor `json:"total_amount_minor"`
		CustomerDetailsRevision time.Time   `json:"customer_details_revision"`
	}{
		ID: proposal.ID, Revision: proposal.Revision, QuoteToken: quoteToken,
		ExpiresAt: proposal.ExpiresAt.UTC(), ServiceID: proposal.ServiceID,
		StartsAt: proposal.StartsAt.UTC(), EndsAt: proposal.EndsAt.UTC(),
		Timezone: proposal.Timezone, FulfillmentMode: proposal.FulfillmentMode,
		LocationLabel: proposal.LocationLabel, CurrencyCode: proposal.CurrencyCode,
		TotalAmount:             proposal.TotalAmountMinor,
		CustomerDetailsRevision: customerDetailsRevision.UTC(),
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func parseInboxAIUUID(raw, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %s is invalid", ErrInboxAIInvalidState, field)
	}
	return id, nil
}

func validateCurrentInboxAIBookingAction(
	record inboxAIBookingSessionRecord,
	actionID uuid.UUID,
	expectedRevision int64,
) error {
	if record.CurrentActionID != actionID || record.Revision != expectedRevision {
		return ErrInboxAIProposalStale
	}
	if !record.ExpiresAt.After(time.Now().UTC()) {
		return ErrInboxAIProposalStale
	}
	return nil
}

func normalizedInboxAIProposalDate(raw string) (time.Time, error) {
	value, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: from must be an ISO date", ErrInboxAIInvalidState)
	}
	return value, nil
}

func normalizedInboxAIProposalTime(raw string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: starts_at must be RFC3339", ErrInboxAIInvalidState)
	}
	return value, nil
}
