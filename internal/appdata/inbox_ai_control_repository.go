package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) GetInboxAIConversationControl(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
) (InboxAIConversationControl, error) {
	if err := authorizeProviderInboxConversation(ctx, r.db, clientID, conversationID); err != nil {
		return InboxAIConversationControl{}, err
	}
	policy, err := loadInboxAIPolicyRecord(ctx, r.db, clientID)
	if err != nil {
		return InboxAIConversationControl{}, err
	}
	return loadInboxAIConversationControl(
		ctx, r.db, clientID, conversationID, policy, r.inboxAIAutomationAvailable(clientID),
	)
}

func (r *Repository) UpdateInboxAIConversationControl(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	input UpdateInboxAIConversationControlInput,
) (InboxAIConversationControl, error) {
	input.ModeOverride = strings.TrimSpace(input.ModeOverride)
	input.State = strings.TrimSpace(input.State)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.ExpectedRevision < 0 || !validInboxAIModeOverride(input.ModeOverride) {
		return InboxAIConversationControl{}, fmt.Errorf("%w: AI conversation control is invalid", ErrInboxAIInvalidState)
	}
	if input.State != "active" && input.State != "paused" && input.State != "provider_takeover" {
		return InboxAIConversationControl{}, fmt.Errorf("%w: AI conversation control state is invalid", ErrInboxAIInvalidState)
	}
	if input.State == "active" {
		input.Reason = ""
	} else if input.Reason == "" {
		if input.State == "paused" {
			input.Reason = "provider_paused"
		} else {
			input.Reason = "provider_takeover"
		}
	}
	if len([]rune(input.Reason)) > 500 {
		return InboxAIConversationControl{}, fmt.Errorf("%w: AI conversation control reason is too long", ErrInboxAIInvalidState)
	}
	if input.ModeOverride != "" && input.ModeOverride != InboxAIModeManual &&
		!r.inboxAIAutomationAvailable(clientID) {
		return InboxAIConversationControl{}, ErrInboxAIAutomationUnavailable
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIConversationControl{}, fmt.Errorf("begin AI control update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var marketplaceCustomerID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT marketplace_customer_id FROM inbox_conversations
		WHERE id=$1 AND client_id=$2 FOR UPDATE
	`, conversationID, clientID).Scan(&marketplaceCustomerID); errors.Is(err, pgx.ErrNoRows) {
		return InboxAIConversationControl{}, ErrNotFound
	} else if err != nil {
		return InboxAIConversationControl{}, fmt.Errorf("authorize AI control update: %w", err)
	}
	policy, err := loadInboxAIPolicyRecord(ctx, tx, clientID)
	if err != nil {
		return InboxAIConversationControl{}, err
	}
	configuredMode := input.ModeOverride
	if configuredMode == "" {
		configuredMode = policy.defaultMode
	}
	if configuredMode != InboxAIModeManual && len(policy.enabledServiceIDs) == 0 {
		return InboxAIConversationControl{}, ErrInboxAIServiceUnavailable
	}
	if configuredMode == InboxAIModeAutopilot {
		if err := validateInboxAIServiceSelection(
			ctx, tx, clientID, policy.enabledServiceIDs, InboxAIModeAutopilot,
		); err != nil {
			return InboxAIConversationControl{}, err
		}
	}

	var currentRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT revision FROM inbox_ai_conversation_controls
		WHERE conversation_id=$1 AND client_id=$2 FOR UPDATE
	`, conversationID, clientID).Scan(&currentRevision); errors.Is(err, pgx.ErrNoRows) {
		currentRevision = 0
	} else if err != nil {
		return InboxAIConversationControl{}, fmt.Errorf("lock AI conversation control: %w", err)
	}
	if currentRevision != input.ExpectedRevision {
		return InboxAIConversationControl{}, ErrInboxAIRevisionConflict
	}
	nextRevision := currentRevision + 1
	var modeOverride any
	if input.ModeOverride != "" {
		modeOverride = input.ModeOverride
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_conversation_controls (
			conversation_id, client_id, mode_override, state, reason, revision,
			updated_by_actor, updated_by_id, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,'provider',$2,NOW(),NOW())
		ON CONFLICT (conversation_id) DO UPDATE SET
			mode_override=EXCLUDED.mode_override, state=EXCLUDED.state,
			reason=EXCLUDED.reason, revision=EXCLUDED.revision,
			updated_by_actor='provider', updated_by_id=$2, updated_at=NOW()
	`, conversationID, clientID, modeOverride, input.State, input.Reason, nextRevision); err != nil {
		return InboxAIConversationControl{}, fmt.Errorf("save AI conversation control: %w", err)
	}
	if err := fenceInboxAISessionForControl(
		ctx, tx, conversationID, configuredMode, input.State, input.Reason,
		policy, nextRevision,
	); err != nil {
		return InboxAIConversationControl{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code='control_updated', lease_owner='',
			lease_expires_at=NULL, completed_at=NOW(), updated_at=NOW()
		WHERE conversation_id=$1 AND status IN ('queued','processing')
	`, conversationID); err != nil {
		return InboxAIConversationControl{}, fmt.Errorf("cancel AI turns after control update: %w", err)
	}
	if err := appendInboxAIControlEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		input.State, input.Reason, nextRevision,
	); err != nil {
		return InboxAIConversationControl{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIConversationControl{}, fmt.Errorf("commit AI conversation control: %w", err)
	}
	return r.GetInboxAIConversationControl(ctx, clientID, conversationID)
}

func (r *Repository) CustomerInboxAIHandoff(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	reason string,
) (CustomerInboxAIHandoffResult, error) {
	if strings.TrimSpace(reason) != "talk_to_provider" {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("%w: handoff reason is invalid", ErrInboxAIInvalidState)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("begin customer AI handoff: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var clientID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT client_id FROM inbox_conversations
		WHERE id=$1 AND marketplace_customer_id=$2 FOR UPDATE
	`, conversationID, marketplaceCustomerID).Scan(&clientID); errors.Is(err, pgx.ErrNoRows) {
		return CustomerInboxAIHandoffResult{}, ErrNotFound
	} else if err != nil {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("authorize customer AI handoff: %w", err)
	}
	var currentRevision int64
	var currentState string
	var effectiveAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT revision, state, updated_at FROM inbox_ai_conversation_controls
		WHERE conversation_id=$1 AND client_id=$2 FOR UPDATE
	`, conversationID, clientID).Scan(&currentRevision, &currentState, &effectiveAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("lock customer AI handoff: %w", err)
	}
	if err == nil && currentState == "handoff" {
		if err := tx.Commit(ctx); err != nil {
			return CustomerInboxAIHandoffResult{}, fmt.Errorf("commit replayed customer AI handoff: %w", err)
		}
		return CustomerInboxAIHandoffResult{
			ConversationID: conversationID.String(), Status: "handoff", EffectiveAt: effectiveAt,
		}, nil
	}
	nextRevision := currentRevision + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_conversation_controls (
			conversation_id, client_id, state, reason, revision,
			updated_by_actor, updated_by_id, created_at, updated_at
		) VALUES ($1,$2,'handoff','talk_to_provider',$3,'marketplace_customer',$4,NOW(),NOW())
		ON CONFLICT (conversation_id) DO UPDATE SET
			state='handoff', reason='talk_to_provider', revision=$3,
			updated_by_actor='marketplace_customer', updated_by_id=$4, updated_at=NOW()
	`, conversationID, clientID, nextRevision, marketplaceCustomerID); err != nil {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("save customer AI handoff: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions
		SET state='handoff', handoff_reason='talk_to_provider', control_revision=$2,
			revision=revision+1, completed_at=COALESCE(completed_at,NOW()), updated_at=NOW()
		WHERE conversation_id=$1
	`, conversationID, nextRevision); err != nil {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("fence customer AI handoff session: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code='customer_handoff', lease_owner='',
			lease_expires_at=NULL, completed_at=NOW(), updated_at=NOW()
		WHERE conversation_id=$1 AND status IN ('queued','processing')
	`, conversationID); err != nil {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("cancel customer-handoff AI turns: %w", err)
	}
	if err := appendInboxAIControlEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		"handoff", "talk_to_provider", nextRevision,
	); err != nil {
		return CustomerInboxAIHandoffResult{}, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT updated_at FROM inbox_ai_conversation_controls WHERE conversation_id=$1
	`, conversationID).Scan(&effectiveAt); err != nil {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("load customer AI handoff time: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CustomerInboxAIHandoffResult{}, fmt.Errorf("commit customer AI handoff: %w", err)
	}
	return CustomerInboxAIHandoffResult{
		ConversationID: conversationID.String(), Status: "handoff", EffectiveAt: effectiveAt,
	}, nil
}

func (r *Repository) GetInboxAISession(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
) (InboxAISessionResponse, error) {
	if err := authorizeProviderInboxConversation(ctx, r.db, clientID, conversationID); err != nil {
		return InboxAISessionResponse{}, err
	}
	session, err := loadInboxAISession(ctx, r.db, conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboxAISessionResponse{Session: nil}, nil
	}
	if err != nil {
		return InboxAISessionResponse{}, err
	}
	return InboxAISessionResponse{Session: &session}, nil
}

func loadInboxAIConversationControl(
	ctx context.Context,
	q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	clientID, conversationID uuid.UUID,
	policy inboxAIPolicyRecord,
	automationAvailable bool,
) (InboxAIConversationControl, error) {
	result := InboxAIConversationControl{
		ConversationID: conversationID.String(), ConfiguredMode: policy.defaultMode,
		EffectiveMode: policy.defaultMode, State: "active", PolicyRevision: policy.revision,
	}
	var updatedAt time.Time
	err := q.QueryRow(ctx, `
		SELECT COALESCE(mode_override,''), state, reason, revision, updated_at
		FROM inbox_ai_conversation_controls
		WHERE conversation_id=$1 AND client_id=$2
	`, conversationID, clientID).Scan(
		&result.ModeOverride, &result.State, &result.Reason, &result.Revision, &updatedAt,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return InboxAIConversationControl{}, fmt.Errorf("load AI conversation control: %w", err)
	}
	if err == nil {
		result.UpdatedAt = &updatedAt
		if result.ModeOverride != "" {
			result.ConfiguredMode = result.ModeOverride
		}
	}
	result.EffectiveMode = result.ConfiguredMode
	if !automationAvailable || policy.paused || result.State != "active" {
		result.EffectiveMode = InboxAIModeManual
	} else {
		result.EffectiveMode = result.ConfiguredMode
	}
	return result, nil
}

func authorizeProviderInboxConversation(
	ctx context.Context,
	q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	clientID, conversationID uuid.UUID,
) error {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM inbox_conversations WHERE id=$1 AND client_id=$2)
	`, conversationID, clientID).Scan(&exists); err != nil {
		return fmt.Errorf("authorize provider AI conversation: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func validInboxAIModeOverride(mode string) bool {
	return mode == "" || mode == InboxAIModeManual || mode == InboxAIModeSemiPilot ||
		mode == InboxAIModeAutopilot
}

func fenceInboxAISessionForControl(
	ctx context.Context,
	tx pgx.Tx,
	conversationID uuid.UUID,
	configuredMode, controlState, reason string,
	policy inboxAIPolicyRecord,
	controlRevision int64,
) error {
	if controlState == "active" && configuredMode != InboxAIModeManual {
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET
				mode=$2, state='qualifying', selected_service_id=NULL,
				booking_link_url='', booking_link_revision=0,
				policy_revision=$3, control_revision=$4, turn_count=0,
				max_turns_snapshot=$5, handoff_reason='', revision=revision+1,
				expires_at=NOW()+($6::int * INTERVAL '1 minute'), completed_at=NULL, updated_at=NOW()
			WHERE conversation_id=$1
		`, conversationID, configuredMode, policy.revision, controlRevision,
			policy.maxTurns, policy.inactivityTimeoutMinutes); err != nil {
			return fmt.Errorf("resume AI conversation session: %w", err)
		}
		return nil
	}
	if controlState == "paused" {
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET
				policy_revision=$2, control_revision=$3,
				revision=revision+1, updated_at=NOW()
			WHERE conversation_id=$1
		`, conversationID, policy.revision, controlRevision); err != nil {
			return fmt.Errorf("pause AI conversation session: %w", err)
		}
		return nil
	}
	terminalState := "provider_takeover"
	terminalReason := reason
	if terminalReason == "" {
		terminalReason = "manual_mode"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET
			state=$2, handoff_reason=$3, policy_revision=$4, control_revision=$5,
			revision=revision+1, completed_at=COALESCE(completed_at,NOW()), updated_at=NOW()
		WHERE conversation_id=$1
	`, conversationID, terminalState, terminalReason, policy.revision, controlRevision); err != nil {
		return fmt.Errorf("fence AI conversation session: %w", err)
	}
	return nil
}

func appendInboxAIControlEvent(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID, marketplaceCustomerID uuid.UUID,
	state, reason string,
	revision int64,
) error {
	var sequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_events (
			conversation_id, client_id, marketplace_customer_id, event_type, payload
		) VALUES (
			$1,$2,$3,'ai.control_updated',
			jsonb_build_object('conversation_id',$1::uuid::text,'state',$4::text,'reason',$5::text,'revision',$6::bigint)
		) RETURNING sequence
	`, conversationID, clientID, marketplaceCustomerID, state, reason, revision).Scan(&sequence); err != nil {
		return fmt.Errorf("append AI control event: %w", err)
	}
	if err := notifyInboxEvent(ctx, tx, sequence, clientID, marketplaceCustomerID); err != nil {
		return fmt.Errorf("notify AI control event: %w", err)
	}
	return nil
}

func loadInboxAISession(
	ctx context.Context,
	q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	conversationID uuid.UUID,
) (InboxAISession, error) {
	var result InboxAISession
	var id, selectedServiceID uuid.UUID
	var selectedService uuid.NullUUID
	if err := q.QueryRow(ctx, `
		SELECT id, conversation_id, mode,
			CASE
				WHEN expires_at<=NOW() AND state NOT IN ('completed','handoff','provider_takeover','expired','failed')
				THEN 'expired'
				ELSE state
			END,
			selected_service_id,
			last_processed_message_sequence, booking_link_url, booking_link_revision,
			policy_revision, control_revision, turn_count, max_turns_snapshot,
			CASE
				WHEN expires_at<=NOW() AND state NOT IN ('completed','handoff','provider_takeover','expired','failed')
				THEN 'inactivity_timeout'
				ELSE handoff_reason
			END,
			revision, expires_at, updated_at
		FROM inbox_ai_sessions WHERE conversation_id=$1
	`, conversationID).Scan(
		&id, &result.ConversationID, &result.Mode, &result.State, &selectedService,
		&result.LastProcessedMessageSequence, &result.BookingLinkURL, &result.BookingLinkRevision,
		&result.PolicyRevision, &result.ControlRevision, &result.TurnCount, &result.MaxTurns,
		&result.HandoffReason, &result.Revision, &result.ExpiresAt, &result.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboxAISession{}, pgx.ErrNoRows
		}
		return InboxAISession{}, fmt.Errorf("load AI session: %w", err)
	}
	result.ID = id.String()
	if selectedService.Valid {
		selectedServiceID = selectedService.UUID
		result.SelectedServiceID = selectedServiceID.String()
	}
	return result, nil
}
