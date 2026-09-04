package appdata

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) validateInboxAutopilotReservationAuthorityTx(
	ctx context.Context,
	tx pgx.Tx,
	providerHandle, quoteToken string,
	input CreatePublicBookingInput,
) (uuid.UUID, error) {
	authority := input.AutopilotAuthority
	if authority == nil || input.MarketplaceCustomerID == nil {
		return uuid.Nil, ErrInboxAIProposalStale
	}
	var conversationClientID uuid.UUID
	var disabled bool
	if err := tx.QueryRow(ctx, `
		SELECT client_id, disabled_at IS NOT NULL FROM inbox_conversations
		WHERE id=$1 AND marketplace_customer_id=$2 FOR UPDATE
	`, authority.ConversationID, *input.MarketplaceCustomerID).Scan(
		&conversationClientID, &disabled,
	); errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInboxAIProposalStale
	} else if err != nil {
		return uuid.Nil, err
	}
	var sessionClientID, sessionCustomerID, quoteID, quoteServiceID, proposalID, confirmationID uuid.UUID
	var confirmationBookingID uuid.NullUUID
	var quoteBookingID uuid.NullUUID
	var proposalRevision, confirmationRevision int64
	var proposalHash, confirmationHash, sessionState, storedQuoteToken string
	if err := tx.QueryRow(ctx, `
		SELECT session.client_id, session.marketplace_customer_id, session.quote_id,
			quote.service_id, quote.booking_id, quote.public_token,
			session.proposal_id, session.proposal_revision, session.proposal_hash,
			session.state, confirmation.id, confirmation.proposal_revision,
			confirmation.proposal_hash, confirmation.booking_id
		FROM inbox_ai_booking_sessions session
		INNER JOIN inbox_ai_booking_confirmations confirmation
			ON confirmation.id=session.confirmation_id
		INNER JOIN booking_quotes quote ON quote.id=session.quote_id
		INNER JOIN client_profile_handles handle
			ON handle.client_id=session.client_id AND handle.handle_slug=$3
		WHERE session.id=$1 AND session.conversation_id=$2
		FOR UPDATE OF session, confirmation
	`, authority.BookingSessionID, authority.ConversationID, strings.TrimSpace(providerHandle)).Scan(
		&sessionClientID, &sessionCustomerID, &quoteID, &quoteServiceID, &quoteBookingID,
		&storedQuoteToken, &proposalID,
		&proposalRevision, &proposalHash, &sessionState, &confirmationID,
		&confirmationRevision, &confirmationHash, &confirmationBookingID,
	); errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInboxAIProposalStale
	} else if err != nil {
		return uuid.Nil, err
	}
	if sessionClientID != conversationClientID || sessionCustomerID != *input.MarketplaceCustomerID ||
		storedQuoteToken != strings.TrimSpace(quoteToken) || proposalID != authority.ProposalID ||
		proposalRevision != authority.ProposalRevision || proposalHash != strings.TrimSpace(authority.ProposalHash) ||
		confirmationID != authority.ConfirmationID || confirmationRevision != authority.ProposalRevision ||
		confirmationHash != strings.TrimSpace(authority.ProposalHash) {
		return uuid.Nil, ErrInboxAIProposalStale
	}
	if confirmationBookingID.Valid && (!quoteBookingID.Valid || confirmationBookingID.UUID != quoteBookingID.UUID) {
		return uuid.Nil, ErrInboxAIProposalStale
	}
	if quoteBookingID.Valid {
		return quoteID, nil
	}
	if disabled {
		return uuid.Nil, ErrInboxConversationDisabled
	}
	if sessionState != "creating_reservation" {
		return uuid.Nil, ErrInboxAIProposalStale
	}
	policy, err := loadInboxAIPolicyRecord(ctx, tx, sessionClientID)
	if err != nil {
		return uuid.Nil, err
	}
	control, err := loadInboxAIConversationControl(
		ctx, tx, sessionClientID, authority.ConversationID, policy,
		r.inboxAIAutomationAvailable(sessionClientID),
	)
	if err != nil {
		return uuid.Nil, err
	}
	if policy.paused || control.State != "active" || control.EffectiveMode != InboxAIModeAutopilot ||
		!inboxAIUUIDSliceContains(policy.enabledServiceIDs, quoteServiceID) {
		return uuid.Nil, ErrInboxAIControlBlocked
	}
	return quoteID, nil
}
