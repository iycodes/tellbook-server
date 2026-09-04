package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	aisvc "booking/go-server/internal/ai"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type completeInboxAISemiPilotTurnCommand struct {
	Turn                inboxAISemiPilotPreparedTurn
	WorkerID            string
	Decision            aisvc.SemiPilotTurnDecision
	ToolCallCount       int
	BookingLinkActionID uuid.UUID
	Latency             time.Duration
}

func (r *Repository) completeInboxAISemiPilotTurn(
	ctx context.Context,
	command completeInboxAISemiPilotTurnCommand,
) error {
	decisionJSON, err := json.Marshal(command.Decision)
	if err != nil {
		return fmt.Errorf("encode semi-pilot decision: %w", err)
	}
	var decision aisvc.SemiPilotTurnDecision
	if command.Turn.Mode == InboxAIModeAutopilot {
		decision, err = aisvc.ParseAutopilotTurnDecision(decisionJSON)
	} else {
		decision, err = aisvc.ParseSemiPilotTurnDecision(decisionJSON)
	}
	if err != nil {
		return err
	}
	if command.ToolCallCount < 0 || command.ToolCallCount > aisvc.SemiPilotMaxToolCallsPerTurn {
		return errors.New("semi-pilot tool-call count is invalid")
	}
	content := normalizeInboxMessageContent(decision.Reply)
	if command.Turn.NeedsDisclosure {
		content = "I’m Tellbook AI, helping this provider with bookings.\n\n" + content
	}
	content = normalizeInboxMessageContent(content)
	if content == "" {
		return ErrInboxInvalidContent
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin semi-pilot completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	turn := command.Turn
	var latestSequence int64
	var disabled bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(last_message_sequence,0), disabled_at IS NOT NULL
		FROM inbox_conversations
		WHERE id=$1 AND client_id=$2 AND marketplace_customer_id=$3
		FOR UPDATE
	`, turn.Job.ConversationID, turn.Job.ClientID, turn.Job.MarketplaceCustomerID).Scan(
		&latestSequence, &disabled,
	); errors.Is(err, pgx.ErrNoRows) {
		return r.cancelInboxAISemiPilotCompletion(
			ctx, tx, turn, command.WorkerID, "conversation_missing",
		)
	} else if err != nil {
		return fmt.Errorf("lock semi-pilot completion conversation: %w", err)
	}
	if disabled || latestSequence != turn.Job.TriggerMessageSequence {
		return r.cancelInboxAISemiPilotCompletion(
			ctx, tx, turn, command.WorkerID, "message_superseded",
		)
	}
	policy, err := loadInboxAIPolicyRecord(ctx, tx, turn.Job.ClientID)
	if err != nil {
		return err
	}
	control, err := loadInboxAIConversationControl(
		ctx, tx, turn.Job.ClientID, turn.Job.ConversationID, policy,
		r.inboxAIAutomationAvailable(turn.Job.ClientID),
	)
	if err != nil {
		return err
	}
	if control.EffectiveMode != turn.Mode || control.State != "active" ||
		policy.paused || policy.revision != turn.PolicyRevision ||
		control.Revision != turn.ControlRevision {
		return r.cancelInboxAISemiPilotCompletion(
			ctx, tx, turn, command.WorkerID, "automation_fenced",
		)
	}
	var sessionMode, sessionState, bookingLink, providerHandle string
	var sessionPolicyRevision, sessionControlRevision, lastProcessedSequence int64
	var selectedService uuid.NullUUID
	var serviceTitle string
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT session.mode, session.state, session.selected_service_id,
			session.last_processed_message_sequence, session.booking_link_url,
			session.policy_revision, session.control_revision, session.expires_at,
			profile.handle_slug, COALESCE(service.title,'')
		FROM inbox_ai_sessions session
		INNER JOIN client_profiles profile ON profile.client_id=session.client_id
		LEFT JOIN services service ON service.id=session.selected_service_id
		WHERE session.id=$1 AND session.conversation_id=$2
		FOR UPDATE OF session
	`, turn.Job.SessionID, turn.Job.ConversationID).Scan(
		&sessionMode, &sessionState, &selectedService, &lastProcessedSequence,
		&bookingLink, &sessionPolicyRevision, &sessionControlRevision, &expiresAt,
		&providerHandle, &serviceTitle,
	); errors.Is(err, pgx.ErrNoRows) {
		return r.cancelInboxAISemiPilotCompletion(
			ctx, tx, turn, command.WorkerID, "session_missing",
		)
	} else if err != nil {
		return fmt.Errorf("lock semi-pilot completion session: %w", err)
	}
	if sessionMode != turn.Mode || isTerminalInboxAISessionState(sessionState) ||
		sessionPolicyRevision != policy.revision || sessionControlRevision != control.Revision ||
		lastProcessedSequence >= turn.Job.TriggerMessageSequence || !expiresAt.After(time.Now()) {
		return r.cancelInboxAISemiPilotCompletion(
			ctx, tx, turn, command.WorkerID, "session_fenced",
		)
	}
	var jobStatus, leaseOwner string
	if err := tx.QueryRow(ctx, `
		SELECT status, lease_owner FROM inbox_ai_turn_jobs WHERE id=$1 FOR UPDATE
	`, turn.Job.ID).Scan(&jobStatus, &leaseOwner); errors.Is(err, pgx.ErrNoRows) {
		return errInboxAISemiPilotTurnStale
	} else if err != nil {
		return fmt.Errorf("lock semi-pilot completion job: %w", err)
	}
	if jobStatus != "processing" || leaseOwner != command.WorkerID {
		return errInboxAISemiPilotTurnStale
	}
	var auditedToolCallCount int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inbox_ai_actions WHERE turn_job_id=$1
	`, turn.Job.ID).Scan(&auditedToolCallCount); err != nil {
		return fmt.Errorf("count semi-pilot turn actions: %w", err)
	}
	if auditedToolCallCount > aisvc.SemiPilotMaxToolCallsPerTurn {
		return errors.New("semi-pilot persisted tool-call budget exceeded")
	}

	var presentation *InboxMessagePresentation
	nextState := decision.NextState
	if turn.Mode == InboxAIModeSemiPilot && command.BookingLinkActionID != uuid.Nil && decision.NextState != "handoff" {
		if strings.TrimSpace(bookingLink) == "" {
			return errors.New("semi-pilot booking link is unavailable at completion")
		}
		data := map[string]any{
			"action_id":   command.BookingLinkActionID.String(),
			"provider_id": turn.Job.ClientID.String(), "provider_handle": providerHandle,
			"href": bookingLink, "label": "Continue to booking",
		}
		if selectedService.Valid {
			data["service_id"] = selectedService.UUID.String()
			data["service_title"] = strings.TrimSpace(serviceTitle)
		}
		presentationData, marshalErr := json.Marshal(data)
		if marshalErr != nil {
			return fmt.Errorf("encode semi-pilot booking card: %w", marshalErr)
		}
		presentation = &InboxMessagePresentation{
			Kind: "booking_link", Version: 1, Data: presentationData,
		}
		if err := validateInboxMessagePresentation(presentation); err != nil {
			return err
		}
		nextState = "link_sent"
	}

	if decision.NextState == "handoff" {
		nextRevision := control.Revision + 1
		if _, err := tx.Exec(ctx, `
			INSERT INTO inbox_ai_conversation_controls (
				conversation_id, client_id, state, reason, revision,
				updated_by_actor, updated_by_id, created_at, updated_at
			) VALUES ($1,$2,'handoff',$3,$4,'system',NULL,NOW(),NOW())
			ON CONFLICT (conversation_id) DO UPDATE SET
				state='handoff', reason=$3, revision=$4,
				updated_by_actor='system', updated_by_id=NULL, updated_at=NOW()
		`, turn.Job.ConversationID, turn.Job.ClientID, decision.HandoffReason, nextRevision); err != nil {
			return fmt.Errorf("save semi-pilot system handoff: %w", err)
		}
		if err := appendInboxAIControlEvent(
			ctx, tx, turn.Job.ConversationID, turn.Job.ClientID,
			turn.Job.MarketplaceCustomerID, "handoff", decision.HandoffReason, nextRevision,
		); err != nil {
			return err
		}
		sessionControlRevision = nextRevision
	}

	messageID := uuid.New()
	clientMessageID := turn.Job.ID
	fingerprint, err := inboxMessageFingerprintWithPresentation(
		content, uuid.NullUUID{}, uuid.NullUUID{}, presentation,
	)
	if err != nil {
		return err
	}
	var sentAt time.Time
	var messageSequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_messages (
			id, conversation_id, sender_type, sender_id, client_message_id,
			request_fingerprint, content, message_type, presentation
		) VALUES ($1,$2,'ai',$3,$4,$5,$6,'text',$7)
		ON CONFLICT (conversation_id,sender_type,sender_id,client_message_id)
			WHERE client_message_id IS NOT NULL DO NOTHING
		RETURNING sent_at, sequence
	`, messageID, turn.Job.ConversationID, turn.Job.ClientID, clientMessageID,
		fingerprint, content, presentation).Scan(&sentAt, &messageSequence); errors.Is(err, pgx.ErrNoRows) {
		return r.cancelInboxAISemiPilotCompletion(
			ctx, tx, turn, command.WorkerID, "duplicate_output",
		)
	} else if err != nil {
		return fmt.Errorf("insert semi-pilot AI message: %w", err)
	}
	conversationTag, err := tx.Exec(ctx, `
		UPDATE inbox_conversations
		SET preview=$2, last_message_sequence=$3, last_message_at=$4,
			updated_at=NOW()
		WHERE id=$1 AND COALESCE(last_message_sequence,0)=$5
	`, turn.Job.ConversationID, inboxMessagePreview(content), messageSequence, sentAt,
		turn.Job.TriggerMessageSequence)
	if err != nil {
		return fmt.Errorf("update semi-pilot conversation summary: %w", err)
	}
	if conversationTag.RowsAffected() != 1 {
		return errInboxAISemiPilotTurnStale
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_participant_states (
			conversation_id, participant_type, participant_id, last_read_sequence, last_read_at
		) VALUES ($1,'provider',$2,$3,$4)
		ON CONFLICT (conversation_id,participant_type,participant_id) DO UPDATE SET
			last_read_sequence=GREATEST(inbox_participant_states.last_read_sequence,$3),
			last_read_at=CASE WHEN $3>inbox_participant_states.last_read_sequence
				THEN $4 ELSE inbox_participant_states.last_read_at END
	`, turn.Job.ConversationID, turn.Job.ClientID, messageSequence, sentAt); err != nil {
		return fmt.Errorf("advance semi-pilot provider cursor: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_participant_states SET archived_at=NULL
		WHERE conversation_id=$1 AND participant_type='marketplace_customer'
		  AND participant_id=$2
	`, turn.Job.ConversationID, turn.Job.MarketplaceCustomerID); err != nil {
		return fmt.Errorf("unarchive semi-pilot recipient: %w", err)
	}
	var eventSequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_events (
			conversation_id, client_id, marketplace_customer_id, event_type, payload
		) VALUES (
			$1,$2,$3,'message.created',
			jsonb_build_object('conversation_id',$1::uuid::text,'message_id',$4::uuid::text,'message_sequence',$5::bigint)
		) RETURNING sequence
	`, turn.Job.ConversationID, turn.Job.ClientID, turn.Job.MarketplaceCustomerID,
		messageID, messageSequence).Scan(&eventSequence); err != nil {
		return fmt.Errorf("append semi-pilot message event: %w", err)
	}
	if err := notifyInboxEvent(
		ctx, tx, eventSequence, turn.Job.ClientID, turn.Job.MarketplaceCustomerID,
	); err != nil {
		return fmt.Errorf("notify semi-pilot message event: %w", err)
	}
	terminal := nextState == "completed" || nextState == "handoff"
	sessionTag, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET
			state=$2, last_processed_message_sequence=$3, last_ai_message_id=$4,
			control_revision=$5,
			handoff_reason=CASE WHEN $2='handoff' THEN $6 ELSE '' END,
			revision=revision+1,
			expires_at=NOW()+($7::int * INTERVAL '1 minute'),
			completed_at=CASE WHEN $8 THEN COALESCE(completed_at,NOW()) ELSE NULL END,
			updated_at=NOW()
		WHERE id=$1
	`, turn.Job.SessionID, nextState, turn.Job.TriggerMessageSequence, messageID,
		sessionControlRevision, decision.HandoffReason,
		turn.InactivityTimeoutMinute, terminal)
	if err != nil {
		return fmt.Errorf("complete semi-pilot session turn: %w", err)
	}
	if sessionTag.RowsAffected() != 1 {
		return errors.New("semi-pilot session completion was not persisted")
	}
	if err := appendInboxAISessionEvent(
		ctx, tx, turn.Job.ConversationID, turn.Job.ClientID,
		turn.Job.MarketplaceCustomerID, nextState, "turn_completed",
	); err != nil {
		return err
	}
	latencyMilliseconds := durationMilliseconds(command.Latency)
	runTag, err := tx.Exec(ctx, `
		UPDATE inbox_ai_runs SET
			status='completed', output_draft=$2, structured_decision=$3,
			tool_call_count=$4, resulting_message_id=$5, error_code='',
			latency_ms=$6, completed_at=NOW()
		WHERE id=$1 AND turn_job_id=$7 AND status='running'
	`, turn.RunID, content, decisionJSON, auditedToolCallCount, messageID,
		latencyMilliseconds, turn.Job.ID)
	if err != nil {
		return fmt.Errorf("complete semi-pilot run: %w", err)
	}
	if runTag.RowsAffected() != 1 {
		return errors.New("semi-pilot run completion lost its running audit row")
	}
	jobTag, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='completed', error_code='', lease_owner='', lease_expires_at=NULL,
			completed_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, turn.Job.ID, command.WorkerID)
	if err != nil {
		return fmt.Errorf("complete semi-pilot job: %w", err)
	}
	if jobTag.RowsAffected() != 1 {
		return errInboxAISemiPilotTurnStale
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit semi-pilot completion: %w", err)
	}
	return nil
}

func (r *Repository) cancelInboxAISemiPilotCompletion(
	ctx context.Context,
	tx pgx.Tx,
	turn inboxAISemiPilotPreparedTurn,
	workerID, code string,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code=$3, lease_owner='', lease_expires_at=NULL,
			completed_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, turn.Job.ID, workerID, code); err != nil {
		return fmt.Errorf("cancel semi-pilot completion: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_runs SET status='failed', output_draft='', error_code='stale',
			latency_ms=GREATEST(0,EXTRACT(EPOCH FROM (NOW()-created_at))*1000)::int,
			completed_at=NOW()
		WHERE id=$1 AND status='running'
	`, turn.RunID); err != nil {
		return fmt.Errorf("fence stale semi-pilot run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit stale semi-pilot completion: %w", err)
	}
	return errInboxAISemiPilotTurnStale
}
