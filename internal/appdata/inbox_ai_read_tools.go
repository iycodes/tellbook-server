package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const inboxAISemiPilotServiceResultLimit = 5

var errInboxAISessionExpired = errors.New("inbox AI session expired")

func (r *Repository) ExecuteSemiPilotReadAction(
	ctx context.Context,
	command ExecuteSemiPilotReadActionCommand,
) (InboxAISemiPilotReadActionResult, error) {
	return r.executeAutomatedReadAction(ctx, command, InboxAIModeSemiPilot)
}

func (r *Repository) executeAutopilotReadAction(
	ctx context.Context,
	command ExecuteSemiPilotReadActionCommand,
) (InboxAISemiPilotReadActionResult, error) {
	return r.executeAutomatedReadAction(ctx, command, InboxAIModeAutopilot)
}

func (r *Repository) executeAutomatedReadAction(
	ctx context.Context,
	command ExecuteSemiPilotReadActionCommand,
	requiredMode string,
) (InboxAISemiPilotReadActionResult, error) {
	clientID, err := uuid.Parse(strings.TrimSpace(command.ClientID))
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("client ID is invalid")
	}
	conversationID, err := uuid.Parse(strings.TrimSpace(command.ConversationID))
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("conversation ID is invalid")
	}
	idempotencyKey, err := uuid.Parse(strings.TrimSpace(command.IdempotencyKey))
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("idempotency key is invalid")
	}
	var turnJobID uuid.NullUUID
	if strings.TrimSpace(command.TurnJobID) != "" {
		parsedTurnJobID, parseErr := uuid.Parse(strings.TrimSpace(command.TurnJobID))
		if parseErr != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf("turn job ID is invalid")
		}
		turnJobID = uuid.NullUUID{UUID: parsedTurnJobID, Valid: true}
	}
	command.ActionName = strings.TrimSpace(command.ActionName)
	command.Query = strings.TrimSpace(command.Query)
	if utf8.RuneCountInString(command.Query) > 160 {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("service query is too long")
	}
	var serviceID uuid.UUID
	if strings.TrimSpace(command.ServiceID) != "" {
		serviceID, err = uuid.Parse(strings.TrimSpace(command.ServiceID))
		if err != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf("service ID is invalid")
		}
	}
	if command.ActionName != "list_relevant_services" &&
		command.ActionName != "get_service_details" && command.ActionName != "get_booking_link" {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("semi-pilot read action is invalid")
	}
	if requiredMode == InboxAIModeAutopilot && command.ActionName == "get_booking_link" {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("autopilot read action is invalid")
	}
	if command.ActionName == "get_service_details" && serviceID == uuid.Nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("service ID is required")
	}
	if !r.inboxAIAutomationAvailable(clientID) {
		return InboxAISemiPilotReadActionResult{}, ErrInboxAIAutomationUnavailable
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("begin semi-pilot read action: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var marketplaceCustomerID uuid.UUID
	var latestMessageSequence int64
	if err := tx.QueryRow(ctx, `
		SELECT marketplace_customer_id, COALESCE(last_message_sequence,0) FROM inbox_conversations
		WHERE id=$1 AND client_id=$2 AND disabled_at IS NULL
		FOR UPDATE
	`, conversationID, clientID).Scan(&marketplaceCustomerID, &latestMessageSequence); errors.Is(err, pgx.ErrNoRows) {
		return InboxAISemiPilotReadActionResult{}, ErrNotFound
	} else if err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("authorize semi-pilot read action: %w", err)
	}
	if command.ExpectedMessageSequence > 0 && latestMessageSequence != command.ExpectedMessageSequence {
		return InboxAISemiPilotReadActionResult{}, ErrInboxAIControlBlocked
	}
	policy, err := loadInboxAIPolicyRecord(ctx, tx, clientID)
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, err
	}
	control, err := loadInboxAIConversationControl(ctx, tx, clientID, conversationID, policy, true)
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, err
	}
	if control.EffectiveMode != requiredMode || control.State != "active" || policy.paused {
		return InboxAISemiPilotReadActionResult{}, ErrInboxAIControlBlocked
	}
	if len(policy.enabledServiceIDs) == 0 {
		return InboxAISemiPilotReadActionResult{}, ErrInboxAIServiceUnavailable
	}
	if serviceID != uuid.Nil && !inboxAIUUIDSliceContains(policy.enabledServiceIDs, serviceID) {
		return InboxAISemiPilotReadActionResult{}, ErrInboxAIServiceUnavailable
	}

	var sessionID uuid.UUID
	var sessionState string
	if requiredMode == InboxAIModeAutopilot {
		sessionID, sessionState, err = ensureAutopilotTurnSession(
			ctx, tx, conversationID, clientID, policy, control.Revision,
		)
	} else {
		sessionID, sessionState, err = ensureSemiPilotSession(
			ctx, tx, conversationID, clientID, policy, control.Revision,
		)
	}
	if errors.Is(err, errInboxAISessionExpired) {
		if err := appendInboxAISessionEvent(
			ctx, tx, conversationID, clientID, marketplaceCustomerID,
			"expired", "inactivity_expired",
		); err != nil {
			return InboxAISemiPilotReadActionResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf("commit expired semi-pilot session: %w", err)
		}
		return InboxAISemiPilotReadActionResult{}, ErrInboxAIControlBlocked
	}
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, err
	}
	if turnJobID.Valid {
		var authorizedJobID uuid.UUID
		if err := tx.QueryRow(ctx, `
			SELECT id FROM inbox_ai_turn_jobs
			WHERE id=$1 AND conversation_id=$2 AND client_id=$3 AND session_id=$4
			  AND trigger_message_sequence=$5 AND status='processing'
			FOR UPDATE
		`, turnJobID.UUID, conversationID, clientID, sessionID,
			command.ExpectedMessageSequence).Scan(&authorizedJobID); errors.Is(err, pgx.ErrNoRows) {
			return InboxAISemiPilotReadActionResult{}, ErrInboxAIControlBlocked
		} else if err != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf("authorize semi-pilot turn action: %w", err)
		}
	}
	safeInput, err := json.Marshal(map[string]any{
		"query": command.Query, "service_id": inboxAIUUIDString(serviceID),
	})
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("encode semi-pilot action input: %w", err)
	}
	actionID := uuid.New()
	commandTag, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_actions (
			id, session_id, conversation_id, client_id, idempotency_key,
			action_name, status, safe_input, turn_job_id, started_at
		) VALUES ($1,$2,$3,$4,$5,$6,'running',$7,$8,NOW())
		ON CONFLICT (session_id,idempotency_key) DO NOTHING
	`, actionID, sessionID, conversationID, clientID, idempotencyKey,
		command.ActionName, safeInput, nullableInboxUUID(turnJobID))
	if err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("start semi-pilot action: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		var actionName, status, errorCode string
		var sameInput, sameTurn bool
		var resultJSON []byte
		if err := tx.QueryRow(ctx, `
			SELECT id, action_name, status, safe_result, error_code,
				safe_input=$3::jsonb, turn_job_id IS NOT DISTINCT FROM $4::uuid
			FROM inbox_ai_actions
			WHERE session_id=$1 AND idempotency_key=$2
		`, sessionID, idempotencyKey, safeInput, nullableInboxUUID(turnJobID)).Scan(
			&actionID, &actionName, &status, &resultJSON, &errorCode, &sameInput, &sameTurn,
		); err != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf("load replayed semi-pilot action: %w", err)
		}
		if actionName != command.ActionName || !sameInput || !sameTurn {
			return InboxAISemiPilotReadActionResult{}, ErrInboxIdempotencyConflict
		}
		if status == "failed" {
			if err := tx.Commit(ctx); err != nil {
				return InboxAISemiPilotReadActionResult{}, fmt.Errorf("commit failed semi-pilot action replay: %w", err)
			}
			return InboxAISemiPilotReadActionResult{}, semiPilotActionError(errorCode)
		}
		if status != "succeeded" {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf("semi-pilot action is not replayable")
		}
		if err := tx.Commit(ctx); err != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf("commit semi-pilot action replay: %w", err)
		}
		return InboxAISemiPilotReadActionResult{
			ActionID: actionID.String(), ActionName: actionName,
			Replayed: true, State: sessionState, Result: resultJSON,
		}, nil
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT semi_pilot_action_execution`); err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("save semi-pilot action execution point: %w", err)
	}
	resultJSON, nextState, selectedServiceID, bookingLink, err := executeSemiPilotReadAction(
		ctx, tx, clientID, marketplaceCustomerID, conversationID, policy.enabledServiceIDs,
		command.ActionName, command.Query, serviceID,
	)
	if err != nil {
		executionErr := err
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if _, rollbackErr := tx.Exec(failureCtx, `ROLLBACK TO SAVEPOINT semi_pilot_action_execution`); rollbackErr != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf(
				"rollback failed semi-pilot action: %v (execution error: %w)", rollbackErr, executionErr,
			)
		}
		if _, updateErr := tx.Exec(failureCtx, `
			UPDATE inbox_ai_actions
			SET status='failed', error_code=$2, completed_at=NOW()
			WHERE id=$1 AND status='running'
		`, actionID, semiPilotActionErrorCode(executionErr)); updateErr != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf(
				"audit failed semi-pilot action: %v (execution error: %w)", updateErr, executionErr,
			)
		}
		if commitErr := tx.Commit(failureCtx); commitErr != nil {
			return InboxAISemiPilotReadActionResult{}, fmt.Errorf(
				"commit failed semi-pilot action: %v (execution error: %w)", commitErr, executionErr,
			)
		}
		return InboxAISemiPilotReadActionResult{}, executionErr
	}
	if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT semi_pilot_action_execution`); err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("release semi-pilot action execution point: %w", err)
	}
	if requiredMode == InboxAIModeAutopilot && nextState == "qualifying" && sessionState != "qualifying" {
		nextState = sessionState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_actions
		SET status='succeeded', safe_result=$2, completed_at=NOW()
		WHERE id=$1 AND status='running'
	`, actionID, resultJSON); err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("complete semi-pilot action: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET
			state=$2,
			selected_service_id=COALESCE($3,selected_service_id),
			booking_link_url=CASE WHEN $4='' THEN booking_link_url ELSE $4 END,
			booking_link_revision=CASE WHEN $4='' THEN booking_link_revision ELSE booking_link_revision+1 END,
			revision=revision+1,
			expires_at=NOW()+($5::int * INTERVAL '1 minute'), updated_at=NOW()
		WHERE id=$1
	`, sessionID, nextState, selectedServiceID, bookingLink,
		policy.inactivityTimeoutMinutes); err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("advance semi-pilot session: %w", err)
	}
	if err := appendInboxAISessionEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		nextState, command.ActionName,
	); err != nil {
		return InboxAISemiPilotReadActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAISemiPilotReadActionResult{}, fmt.Errorf("commit semi-pilot action: %w", err)
	}
	return InboxAISemiPilotReadActionResult{
		ActionID: actionID.String(), ActionName: command.ActionName,
		State: nextState, Result: resultJSON,
	}, nil
}

func ensureAutopilotTurnSession(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID uuid.UUID,
	policy inboxAIPolicyRecord,
	controlRevision int64,
) (uuid.UUID, string, error) {
	var sessionID uuid.UUID
	var state, mode string
	var policyRevision, storedControlRevision int64
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, state, mode, policy_revision, control_revision, expires_at
		FROM inbox_ai_sessions WHERE conversation_id=$1 FOR UPDATE
	`, conversationID).Scan(
		&sessionID, &state, &mode, &policyRevision, &storedControlRevision, &expiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		sessionID = uuid.New()
		state = "qualifying"
		if _, err := tx.Exec(ctx, `
			INSERT INTO inbox_ai_sessions (
				id, conversation_id, client_id, mode, state, policy_revision,
				control_revision, max_turns_snapshot, expires_at, created_at, updated_at
			) VALUES (
				$1,$2,$3,'autopilot','qualifying',$4,$5,$6,
				NOW()+($7::int * INTERVAL '1 minute'),NOW(),NOW()
			)
		`, sessionID, conversationID, clientID, policy.revision, controlRevision,
			policy.maxTurns, policy.inactivityTimeoutMinutes); err != nil {
			return uuid.Nil, "", fmt.Errorf("create autopilot turn session: %w", err)
		}
		return sessionID, state, nil
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("lock autopilot turn session: %w", err)
	}
	if mode != InboxAIModeAutopilot || policyRevision != policy.revision || storedControlRevision != controlRevision {
		state = "qualifying"
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET
				mode='autopilot', state='qualifying', selected_service_id=NULL,
				booking_link_url='', booking_link_revision=0,
				policy_revision=$2, control_revision=$3, turn_count=0,
				max_turns_snapshot=$4, handoff_reason='', revision=revision+1,
				expires_at=NOW()+($5::int * INTERVAL '1 minute'), completed_at=NULL, updated_at=NOW()
			WHERE id=$1
		`, sessionID, policy.revision, controlRevision, policy.maxTurns,
			policy.inactivityTimeoutMinutes); err != nil {
			return uuid.Nil, "", fmt.Errorf("reset autopilot turn session: %w", err)
		}
		return sessionID, state, nil
	}
	if isTerminalInboxAISessionState(state) {
		return uuid.Nil, "", ErrInboxAIControlBlocked
	}
	if !expiresAt.After(time.Now()) {
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET state='expired', handoff_reason='inactivity_timeout',
				revision=revision+1, completed_at=COALESCE(completed_at,NOW()), updated_at=NOW()
			WHERE id=$1
		`, sessionID); err != nil {
			return uuid.Nil, "", fmt.Errorf("expire autopilot turn session: %w", err)
		}
		return sessionID, "expired", errInboxAISessionExpired
	}
	return sessionID, state, nil
}

func ensureSemiPilotSession(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID uuid.UUID,
	policy inboxAIPolicyRecord,
	controlRevision int64,
) (uuid.UUID, string, error) {
	var sessionID uuid.UUID
	var state, mode string
	var policyRevision, storedControlRevision int64
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, state, mode, policy_revision, control_revision, expires_at
		FROM inbox_ai_sessions WHERE conversation_id=$1 FOR UPDATE
	`, conversationID).Scan(
		&sessionID, &state, &mode, &policyRevision, &storedControlRevision, &expiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		sessionID = uuid.New()
		state = "qualifying"
		if _, err := tx.Exec(ctx, `
			INSERT INTO inbox_ai_sessions (
				id, conversation_id, client_id, mode, state, policy_revision,
				control_revision, max_turns_snapshot, expires_at, created_at, updated_at
			) VALUES (
				$1,$2,$3,'semi_pilot','qualifying',$4,$5,$6,
				NOW()+($7::int * INTERVAL '1 minute'),NOW(),NOW()
			)
		`, sessionID, conversationID, clientID, policy.revision, controlRevision,
			policy.maxTurns, policy.inactivityTimeoutMinutes); err != nil {
			return uuid.Nil, "", fmt.Errorf("create semi-pilot session: %w", err)
		}
		return sessionID, state, nil
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("lock semi-pilot session: %w", err)
	}
	if mode != InboxAIModeSemiPilot || policyRevision != policy.revision ||
		storedControlRevision != controlRevision {
		state = "qualifying"
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET
				mode='semi_pilot', state='qualifying', selected_service_id=NULL,
				booking_link_url='', booking_link_revision=0,
				policy_revision=$2, control_revision=$3, turn_count=0,
				max_turns_snapshot=$4, handoff_reason='', revision=revision+1,
				expires_at=NOW()+($5::int * INTERVAL '1 minute'), completed_at=NULL, updated_at=NOW()
			WHERE id=$1
		`, sessionID, policy.revision, controlRevision, policy.maxTurns,
			policy.inactivityTimeoutMinutes); err != nil {
			return uuid.Nil, "", fmt.Errorf("reset semi-pilot session: %w", err)
		}
		return sessionID, state, nil
	}
	if isTerminalInboxAISessionState(state) {
		return uuid.Nil, "", ErrInboxAIControlBlocked
	}
	if !expiresAt.After(time.Now()) {
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET
				state='expired', handoff_reason='inactivity_timeout',
				revision=revision+1, completed_at=COALESCE(completed_at,NOW()), updated_at=NOW()
			WHERE id=$1
		`, sessionID); err != nil {
			return uuid.Nil, "", fmt.Errorf("expire semi-pilot session: %w", err)
		}
		return sessionID, "expired", errInboxAISessionExpired
	}
	return sessionID, state, nil
}

func appendInboxAISessionEvent(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID, marketplaceCustomerID uuid.UUID,
	state, actionName string,
) error {
	var eventSequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_events (
			conversation_id, client_id, marketplace_customer_id, event_type, payload
		) VALUES (
			$1,$2,$3,'ai.session_updated',
			jsonb_build_object('conversation_id',$1::uuid::text,'state',$4::text,'action_name',$5::text)
		) RETURNING sequence
	`, conversationID, clientID, marketplaceCustomerID, state, actionName).Scan(&eventSequence); err != nil {
		return fmt.Errorf("append semi-pilot session event: %w", err)
	}
	if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
		return fmt.Errorf("notify semi-pilot session event: %w", err)
	}
	return nil
}

func executeSemiPilotReadAction(
	ctx context.Context,
	tx pgx.Tx,
	clientID, marketplaceCustomerID, conversationID uuid.UUID,
	enabledServiceIDs []uuid.UUID,
	actionName, query string,
	serviceID uuid.UUID,
) ([]byte, string, any, string, error) {
	switch actionName {
	case "list_relevant_services":
		pattern := "%" + strings.ToLower(query) + "%"
		rows, err := tx.Query(ctx, `
			SELECT id, title, fulfillment_mode, duration_minutes, price_amount_minor, currency_code
			FROM services
			WHERE client_id=$1 AND id=ANY($2::uuid[]) AND status='published'
			  AND is_active AND NOT is_hidden
			  AND ($3='' OR LOWER(title) LIKE $4 OR LOWER(category) LIKE $4 OR LOWER(description) LIKE $4)
			ORDER BY
			  CASE WHEN LOWER(title)=LOWER($3) THEN 0 WHEN LOWER(title) LIKE LOWER($3)||'%' THEN 1 ELSE 2 END,
			  sort_order, title, id
			LIMIT $5
		`, clientID, enabledServiceIDs, query, pattern, inboxAISemiPilotServiceResultLimit)
		if err != nil {
			return nil, "", nil, "", fmt.Errorf("list relevant semi-pilot services: %w", err)
		}
		defer rows.Close()
		items := make([]InboxAISemiPilotService, 0)
		for rows.Next() {
			var item InboxAISemiPilotService
			if err := rows.Scan(&item.ID, &item.Title, &item.FulfillmentMode,
				&item.DurationMinutes, &item.AmountMinor, &item.CurrencyCode); err != nil {
				return nil, "", nil, "", fmt.Errorf("scan relevant semi-pilot service: %w", err)
			}
			item.Title = truncateInboxAIReadToolText(item.Title, 200)
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return nil, "", nil, "", fmt.Errorf("iterate relevant semi-pilot services: %w", err)
		}
		encoded, err := json.Marshal(map[string]any{"services": items})
		return encoded, "qualifying", nil, "", err
	case "get_service_details":
		var item InboxAISemiPilotService
		if err := tx.QueryRow(ctx, `
			SELECT service.id, service.title, service.description, service.fulfillment_mode,
				service.duration_minutes, service.price_amount_minor, service.currency_code,
				CASE
				  WHEN service.fulfillment_mode='provider_location' THEN COALESCE(location.formatted_address,'')
				  WHEN service.fulfillment_mode='customer_location' THEN 'Customer location'
				  ELSE service.virtual_delivery_label
				END,
				service.cancellation_policy, service.lateness_policy
			FROM services service
			LEFT JOIN business_locations location ON location.id=service.provider_location_id
			WHERE service.client_id=$1 AND service.id=$2 AND service.id=ANY($3::uuid[])
			  AND service.status='published' AND service.is_active AND NOT service.is_hidden
		`, clientID, serviceID, enabledServiceIDs).Scan(
			&item.ID, &item.Title, &item.Description, &item.FulfillmentMode,
			&item.DurationMinutes, &item.AmountMinor, &item.CurrencyCode,
			&item.LocationLabel, &item.CancellationPolicy, &item.LatenessPolicy,
		); errors.Is(err, pgx.ErrNoRows) {
			return nil, "", nil, "", ErrInboxAIServiceUnavailable
		} else if err != nil {
			return nil, "", nil, "", fmt.Errorf("load semi-pilot service details: %w", err)
		}
		item.Title = truncateInboxAIReadToolText(item.Title, 200)
		item.Description = truncateInboxAIReadToolText(item.Description, 1200)
		item.LocationLabel = truncateInboxAIReadToolText(item.LocationLabel, 300)
		item.CancellationPolicy = truncateInboxAIReadToolText(item.CancellationPolicy, 600)
		item.LatenessPolicy = truncateInboxAIReadToolText(item.LatenessPolicy, 600)
		encoded, err := json.Marshal(map[string]any{"service": item})
		return encoded, "service_identified", serviceID, "", err
	case "get_booking_link":
		var selected any
		var servicePointer *uuid.UUID
		if serviceID != uuid.Nil {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM services WHERE client_id=$1 AND id=$2
					  AND id=ANY($3::uuid[]) AND status='published' AND is_active AND NOT is_hidden
				)
			`, clientID, serviceID, enabledServiceIDs).Scan(&exists); err != nil {
				return nil, "", nil, "", fmt.Errorf("authorize semi-pilot booking link service: %w", err)
			}
			if !exists {
				return nil, "", nil, "", ErrInboxAIServiceUnavailable
			}
			servicePointer = &serviceID
			selected = serviceID
		}
		canonicalLink, err := buildCanonicalBookingLink(
			ctx,
			tx,
			BuildCanonicalBookingLinkCommand{
				MarketplaceCustomerID: marketplaceCustomerID,
				ConversationID:        conversationID,
				ServiceID:             servicePointer,
			},
		)
		if err != nil {
			return nil, "", nil, "", err
		}
		encoded, err := json.Marshal(map[string]any{
			"href": canonicalLink.Href, "provider_handle": canonicalLink.ProviderHandle,
			"service_id": inboxAIUUIDString(serviceID),
		})
		return encoded, "link_ready", selected, canonicalLink.Href, err
	default:
		return nil, "", nil, "", fmt.Errorf("semi-pilot read action is invalid")
	}
}

func inboxAIUUIDSliceContains(items []uuid.UUID, target uuid.UUID) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func inboxAIUUIDString(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id.String()
}

func truncateInboxAIReadToolText(value string, maximumBytes int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximumBytes {
		return value
	}
	end := maximumBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.TrimSpace(value[:end])
}

func isTerminalInboxAISessionState(state string) bool {
	switch state {
	case "completed", "handoff", "provider_takeover", "expired", "failed":
		return true
	default:
		return false
	}
}

func semiPilotActionErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrInboxAIServiceUnavailable):
		return "service_unavailable"
	case errors.Is(err, ErrInboxAIControlBlocked):
		return "automation_blocked"
	default:
		return "internal_error"
	}
}

func semiPilotActionError(code string) error {
	switch strings.TrimSpace(code) {
	case "not_found":
		return ErrNotFound
	case "service_unavailable":
		return ErrInboxAIServiceUnavailable
	case "automation_blocked":
		return ErrInboxAIControlBlocked
	default:
		return fmt.Errorf("semi-pilot action failed")
	}
}
