package appdata

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	inboxAISemiPilotProviderTurnsPerMinute = 30
	inboxAISemiPilotCustomerTurnsPerMinute = 8
	inboxAISemiPilotProviderBacklogLimit   = 25
)

func (r *Repository) enqueueInboxAISemiPilotTurn(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID, marketplaceCustomerID, messageID uuid.UUID,
	messageSequence int64,
) error {
	return r.enqueueInboxAIAutomatedTurn(
		ctx, tx, conversationID, clientID, marketplaceCustomerID, messageID, messageSequence,
	)
}

func (r *Repository) enqueueInboxAIAutomatedTurn(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID, marketplaceCustomerID, messageID uuid.UUID,
	messageSequence int64,
) error {
	if !r.inboxAIAutomationAvailable(clientID) {
		return nil
	}
	policy, err := loadInboxAIPolicyRecord(ctx, tx, clientID)
	if err != nil {
		return err
	}
	if policy.paused || len(policy.enabledServiceIDs) == 0 {
		return nil
	}
	control, err := loadInboxAIConversationControl(ctx, tx, clientID, conversationID, policy, true)
	if err != nil {
		return err
	}
	if (control.EffectiveMode != InboxAIModeSemiPilot && control.EffectiveMode != InboxAIModeAutopilot) ||
		control.State != "active" {
		return nil
	}
	var sessionID uuid.UUID
	if control.EffectiveMode == InboxAIModeAutopilot {
		sessionID, _, err = ensureAutopilotTurnSession(
			ctx, tx, conversationID, clientID, policy, control.Revision,
		)
	} else {
		sessionID, _, err = ensureSemiPilotSession(
			ctx, tx, conversationID, clientID, policy, control.Revision,
		)
	}
	if errors.Is(err, errInboxAISessionExpired) {
		return appendInboxAISessionEvent(
			ctx, tx, conversationID, clientID, marketplaceCustomerID,
			"expired", "inactivity_expired",
		)
	}
	if errors.Is(err, ErrInboxAIControlBlocked) {
		return nil
	}
	if err != nil {
		return err
	}
	// The budget check and job insert must be one serialized decision for both
	// identities. Conversation row locks alone do not protect one provider across
	// customers or one customer across providers.
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended('inbox-ai-provider-rate:' || $1::uuid::text, 0)
		)
	`, clientID); err != nil {
		return fmt.Errorf("lock semi-pilot provider rate budget: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended('inbox-ai-customer-rate:' || $1::uuid::text, 0)
		)
	`, marketplaceCustomerID); err != nil {
		return fmt.Errorf("lock semi-pilot customer rate budget: %w", err)
	}
	var providerTurns, customerTurns, providerBacklog int
	if err := tx.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE client_id=$1 AND created_at>=NOW()-INTERVAL '1 minute'),
			COUNT(*) FILTER (WHERE marketplace_customer_id=$2 AND created_at>=NOW()-INTERVAL '1 minute'),
			COUNT(*) FILTER (WHERE client_id=$1 AND status IN ('queued','processing'))
		FROM inbox_ai_turn_jobs
		WHERE client_id=$1 OR marketplace_customer_id=$2
	`, clientID, marketplaceCustomerID).Scan(&providerTurns, &customerTurns, &providerBacklog); err != nil {
		return fmt.Errorf("load semi-pilot turn rate budget: %w", err)
	}
	if providerTurns >= inboxAISemiPilotProviderTurnsPerMinute ||
		customerTurns >= inboxAISemiPilotCustomerTurnsPerMinute ||
		providerBacklog >= inboxAISemiPilotProviderBacklogLimit {
		return r.handoffRateLimitedInboxAIAutomation(
			ctx, tx, conversationID, clientID, marketplaceCustomerID,
			control.Revision, control.EffectiveMode,
		)
	}

	// Only the newest customer message may own a live turn. Cancelling queued or
	// leased predecessors here avoids needless inference; every tool and final
	// send is independently fenced by the trigger sequence as well.
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code='superseded', lease_owner='',
			lease_expires_at=NULL, completed_at=NOW(), updated_at=NOW()
		WHERE conversation_id=$1 AND status IN ('queued','processing')
		  AND trigger_message_sequence < $2
	`, conversationID, messageSequence); err != nil {
		return fmt.Errorf("cancel superseded semi-pilot turns: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_turn_jobs (
			id, conversation_id, client_id, marketplace_customer_id, session_id,
			trigger_message_id, trigger_message_sequence, available_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,
			NOW()+($8::bigint * INTERVAL '1 millisecond'))
		ON CONFLICT (trigger_message_id) DO NOTHING
	`, uuid.New(), conversationID, clientID, marketplaceCustomerID, sessionID,
		messageID, messageSequence, r.inboxAISemiPilotReplyDelay.Milliseconds()); err != nil {
		return fmt.Errorf("enqueue semi-pilot turn: %w", err)
	}
	return nil
}

func (r *Repository) handoffRateLimitedInboxAISemiPilot(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID, marketplaceCustomerID uuid.UUID,
	controlRevision int64,
) error {
	return r.handoffRateLimitedInboxAIAutomation(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		controlRevision, InboxAIModeSemiPilot,
	)
}

func (r *Repository) handoffRateLimitedInboxAIAutomation(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID, marketplaceCustomerID uuid.UUID,
	controlRevision int64,
	mode string,
) error {
	nextRevision := controlRevision + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_conversation_controls (
			conversation_id, client_id, state, reason, revision,
			updated_by_actor, updated_by_id, created_at, updated_at
		) VALUES ($1,$2,'handoff','rate_limit',$3,'system',NULL,NOW(),NOW())
		ON CONFLICT (conversation_id) DO UPDATE SET
			state='handoff', reason='rate_limit', revision=$3,
			updated_by_actor='system', updated_by_id=NULL, updated_at=NOW()
	`, conversationID, clientID, nextRevision); err != nil {
		return fmt.Errorf("handoff rate-limited semi-pilot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions
		SET state='handoff', handoff_reason='rate_limit', control_revision=$2,
			revision=revision+1, completed_at=COALESCE(completed_at,NOW()), updated_at=NOW()
		WHERE conversation_id=$1 AND mode=$3
	`, conversationID, nextRevision, mode); err != nil {
		return fmt.Errorf("fence rate-limited semi-pilot session: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code='rate_limit', lease_owner='',
			lease_expires_at=NULL, completed_at=NOW(), updated_at=NOW()
		WHERE conversation_id=$1 AND status IN ('queued','processing')
	`, conversationID); err != nil {
		return fmt.Errorf("cancel rate-limited semi-pilot turns: %w", err)
	}
	if err := appendInboxAIControlEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		"handoff", "rate_limit", nextRevision,
	); err != nil {
		return err
	}
	return appendInboxAISessionEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		"handoff", "rate_limit",
	)
}

func (r *Repository) fenceInboxAISemiPilotForProviderMessage(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID, marketplaceCustomerID uuid.UUID,
) error {
	policy, err := loadInboxAIPolicyRecord(ctx, tx, clientID)
	if err != nil {
		return err
	}
	control, err := loadInboxAIConversationControl(
		ctx, tx, clientID, conversationID, policy, r.inboxAIAutomationAvailable(clientID),
	)
	if err != nil {
		return err
	}
	if control.State != "active" ||
		(control.EffectiveMode != InboxAIModeSemiPilot && control.EffectiveMode != InboxAIModeAutopilot) {
		return nil
	}
	nextRevision := control.Revision + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_conversation_controls (
			conversation_id, client_id, state, reason, revision,
			updated_by_actor, updated_by_id, created_at, updated_at
		) VALUES ($1,$2,'provider_takeover','provider_message',$3,'provider',$2,NOW(),NOW())
		ON CONFLICT (conversation_id) DO UPDATE SET
			state='provider_takeover', reason='provider_message', revision=$3,
			updated_by_actor='provider', updated_by_id=$2, updated_at=NOW()
	`, conversationID, clientID, nextRevision); err != nil {
		return fmt.Errorf("take over semi-pilot after provider message: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions
		SET state='provider_takeover', handoff_reason='provider_message',
			control_revision=$2, revision=revision+1,
			completed_at=COALESCE(completed_at,NOW()), updated_at=NOW()
		WHERE conversation_id=$1 AND mode=$3
	`, conversationID, nextRevision, control.EffectiveMode); err != nil {
		return fmt.Errorf("fence semi-pilot after provider message: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code='provider_takeover', lease_owner='',
			lease_expires_at=NULL, completed_at=NOW(), updated_at=NOW()
		WHERE conversation_id=$1 AND status IN ('queued','processing')
	`, conversationID); err != nil {
		return fmt.Errorf("cancel semi-pilot turns after provider message: %w", err)
	}
	return appendInboxAIControlEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		"provider_takeover", "provider_message", nextRevision,
	)
}
