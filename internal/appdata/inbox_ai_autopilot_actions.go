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

func (r *Repository) StartMarketplaceInboxAIBooking(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	input StartInboxAIBookingInput,
) (InboxAIBookingActionResult, error) {
	idempotencyKey, err := parseInboxAIUUID(input.IdempotencyKey, "idempotency_key")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	safeInput := map[string]any{}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("begin inbox booking start: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replay, found, replayErr := loadInboxAIWorkflowMessageReplay(
		ctx, tx, marketplaceCustomerID, conversationID, idempotencyKey,
		"start_booking_session", safeInput,
	); replayErr != nil {
		return InboxAIBookingActionResult{}, replayErr
	} else if found {
		workflow, workflowErr := r.loadInboxAIBookingWorkflow(
			ctx, tx, marketplaceCustomerID, conversationID,
		)
		if workflowErr != nil {
			return InboxAIBookingActionResult{}, workflowErr
		}
		summary, summaryErr := loadMarketplaceConversationSummary(
			ctx, tx, marketplaceCustomerID, conversationID,
		)
		if summaryErr != nil {
			return InboxAIBookingActionResult{}, summaryErr
		}
		if err := tx.Commit(ctx); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		return InboxAIBookingActionResult{
			Workflow: workflow, Message: &replay, Conversation: &summary, Replayed: true,
		}, nil
	}
	policy, control, existingRecord, err := r.lockInboxAIBookingAuthority(
		ctx, tx, marketplaceCustomerID, conversationID, false,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if existingRecord.AgreementInstanceID.Valid {
		if _, err := tx.Exec(ctx, `
			UPDATE agreement_instances SET status='cancelled', updated_at=NOW()
			WHERE id=$1 AND status='awaiting_customer'
		`, existingRecord.AgreementInstanceID.UUID); err != nil {
			return InboxAIBookingActionResult{}, fmt.Errorf("cancel superseded inbox agreement: %w", err)
		}
	}
	var clientID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT client_id FROM inbox_conversations WHERE id=$1`, conversationID).Scan(&clientID); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	aiSessionID, err := ensureAutopilotAISession(
		ctx, tx, conversationID, clientID, policy, control.Revision,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	actionID := uuid.New()
	bookingSessionID := uuid.New()
	var revision int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_ai_booking_sessions (
			id, ai_session_id, conversation_id, client_id, marketplace_customer_id,
			state, current_action_id, revision, expires_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,'collecting_preferences',$6,1,
			NOW()+($7::int * INTERVAL '1 minute'),NOW(),NOW()
		)
		ON CONFLICT (conversation_id,client_id,marketplace_customer_id) DO UPDATE SET
			ai_session_id=EXCLUDED.ai_session_id,
			state='collecting_preferences', selected_service_id=NULL,
			requested_from=NULL, requested_days=NULL, timezone='',
			selected_start_at=NULL, selected_end_at=NULL, quote_id=NULL, quote_token='',
			quote_expires_at=NULL, currency_code='', total_amount_minor=NULL,
			proposal_id=NULL, proposal_revision=0, proposal_hash='',
			proposal_customer_details_revision=NULL,
			agreement_instance_id=NULL, confirmation_id=NULL, booking_id=NULL,
			current_action_id=EXCLUDED.current_action_id, revision=inbox_ai_booking_sessions.revision+1,
			expires_at=EXCLUDED.expires_at, updated_at=NOW()
		RETURNING id, revision
	`, bookingSessionID, aiSessionID, conversationID, clientID, marketplaceCustomerID,
		actionID, policy.inactivityTimeoutMinutes).Scan(&bookingSessionID, &revision); err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("create inbox booking session: %w", err)
	}
	choices, err := loadInboxAIServiceChoices(ctx, tx, clientID, policy.enabledServiceIDs)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if len(choices) == 0 {
		return InboxAIBookingActionResult{}, ErrInboxAIServiceUnavailable
	}
	presentationData, err := json.Marshal(map[string]any{
		"action_id": actionID.String(), "session_revision": revision, "choices": choices,
	})
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	presentation := InboxMessagePresentation{
		Kind: "service_choices", Version: 1, Data: presentationData,
	}
	message, summary, err := r.appendInboxAIWorkflowMessageTx(
		ctx, tx, clientID, marketplaceCustomerID, conversationID, idempotencyKey,
		"Choose the service you want, then I’ll show current available times.", presentation,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := auditInboxAIBookingAction(
		ctx, tx, aiSessionID, conversationID, clientID, idempotencyKey,
		"start_booking_session", safeInput,
		map[string]any{"booking_session_id": bookingSessionID.String(), "revision": revision},
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := appendInboxAISessionEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		"collecting_preferences", "list_relevant_services",
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("commit inbox booking start: %w", err)
	}
	return InboxAIBookingActionResult{
		Workflow: workflow, Message: &message, Conversation: &summary,
	}, nil
}

func (r *Repository) OfferMarketplaceInboxAIAvailability(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	input OfferInboxAIAvailabilityInput,
) (InboxAIBookingActionResult, error) {
	idempotencyKey, err := parseInboxAIUUID(input.IdempotencyKey, "idempotency_key")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	actionID, err := parseInboxAIUUID(input.ActionID, "action_id")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	serviceID, err := parseInboxAIUUID(input.ServiceID, "service_id")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	from, err := normalizedInboxAIProposalDate(input.From)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if input.Days < 1 || input.Days > 14 {
		return InboxAIBookingActionResult{}, fmt.Errorf("%w: days must be between 1 and 14", ErrInboxAIInvalidState)
	}
	safeInput := map[string]any{
		"action_id": actionID.String(), "expected_revision": input.ExpectedRevision,
		"service_id": serviceID.String(), "from": from.Format("2006-01-02"), "days": input.Days,
	}
	clientID, providerHandle, policy, err := r.authorizeInboxAIBookingRead(
		ctx, marketplaceCustomerID, conversationID, serviceID,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	availability, err := r.BookingApplication().SearchAvailability(ctx, SearchBookingAvailabilityCommand{
		ProviderHandle: providerHandle, ServiceID: serviceID, From: &from, Days: input.Days,
	})
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	availabilityLocation, err := time.LoadLocation(availability.Timezone)
	if err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("load canonical availability timezone: %w", err)
	}
	choices := make([]map[string]any, 0, 12)
	for _, day := range availability.Dates {
		for _, slot := range day.Slots {
			if len(choices) == 12 {
				break
			}
			startsAt, parseErr := time.Parse(time.RFC3339, slot.StartAt)
			if parseErr != nil {
				return InboxAIBookingActionResult{}, fmt.Errorf("invalid canonical availability time: %w", parseErr)
			}
			endsAt := startsAt.Add(time.Duration(availability.DurationMinutes) * time.Minute)
			slotID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(serviceID.String()+":"+startsAt.UTC().Format(time.RFC3339Nano)))
			choices = append(choices, map[string]any{
				"slot_id": slotID.String(), "starts_at": startsAt.UTC().Format(time.RFC3339),
				"ends_at": endsAt.UTC().Format(time.RFC3339),
				"label":   startsAt.In(availabilityLocation).Format("Mon, 2 Jan · 03:04 PM"),
			})
		}
		if len(choices) == 12 {
			break
		}
	}
	if len(choices) == 0 {
		return InboxAIBookingActionResult{}, ErrSlotUnavailable
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replay, found, replayErr := loadInboxAIWorkflowMessageReplay(
		ctx, tx, marketplaceCustomerID, conversationID, idempotencyKey,
		"search_availability", safeInput,
	); replayErr != nil {
		return InboxAIBookingActionResult{}, replayErr
	} else if found {
		return r.commitInboxAIBookingMessageReplay(
			ctx, tx, marketplaceCustomerID, conversationID, replay,
		)
	}
	lockedPolicy, _, record, err := r.lockInboxAIBookingAuthority(
		ctx, tx, marketplaceCustomerID, conversationID, true,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if lockedPolicy.revision != policy.revision || record.ClientID != clientID ||
		!inboxAIUUIDSliceContains(lockedPolicy.enabledServiceIDs, serviceID) {
		return InboxAIBookingActionResult{}, ErrInboxAIProposalStale
	}
	if err := validateCurrentInboxAIBookingAction(record, actionID, input.ExpectedRevision); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if record.AgreementInstanceID.Valid {
		if _, err := tx.Exec(ctx, `
			UPDATE agreement_instances SET status='cancelled', updated_at=NOW()
			WHERE id=$1 AND status='awaiting_customer'
		`, record.AgreementInstanceID.UUID); err != nil {
			return InboxAIBookingActionResult{}, fmt.Errorf("cancel superseded inbox agreement: %w", err)
		}
	}
	nextActionID := uuid.New()
	nextRevision := record.Revision + 1
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_booking_sessions SET
			state='offering_slots', selected_service_id=$2, requested_from=$3,
			requested_days=$4, timezone=$5, selected_start_at=NULL, selected_end_at=NULL,
			quote_id=NULL, quote_token='', quote_expires_at=NULL, currency_code='',
			total_amount_minor=NULL, proposal_id=NULL, proposal_revision=0,
			proposal_hash='', proposal_customer_details_revision=NULL,
			agreement_instance_id=NULL, confirmation_id=NULL, booking_id=NULL,
			current_action_id=$6, revision=$7, expires_at=$8, updated_at=NOW()
		WHERE id=$1 AND revision=$9
	`, record.ID, serviceID, from.Format("2006-01-02"), input.Days,
		availability.Timezone, nextActionID, nextRevision,
		expiresAt, record.Revision); err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("save inbox availability offer: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET state='offering_slots', selected_service_id=$2,
			revision=revision+1, expires_at=$3, updated_at=NOW()
		WHERE id=$1
	`, record.AISessionID, serviceID, expiresAt); err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("advance inbox AI availability state: %w", err)
	}
	presentationData, err := json.Marshal(map[string]any{
		"action_id": nextActionID.String(), "session_revision": nextRevision,
		"service_id": serviceID.String(), "timezone": availability.Timezone,
		"expires_at": expiresAt.Format(time.RFC3339), "choices": choices,
	})
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	presentation := InboxMessagePresentation{Kind: "availability_choices", Version: 1, Data: presentationData}
	message, summary, err := r.appendInboxAIWorkflowMessageTx(
		ctx, tx, clientID, marketplaceCustomerID, conversationID, idempotencyKey,
		"These are the nearest currently available times. Choose the exact time you want.", presentation,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := auditInboxAIBookingAction(
		ctx, tx, record.AISessionID, conversationID, clientID, idempotencyKey,
		"search_availability", safeInput,
		map[string]any{"choice_count": len(choices), "expires_at": expiresAt},
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := appendInboxAISessionEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		"offering_slots", "search_availability",
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	return InboxAIBookingActionResult{
		Workflow: workflow, Message: &message, Conversation: &summary,
	}, nil
}

func ensureAutopilotAISession(
	ctx context.Context,
	tx pgx.Tx,
	conversationID, clientID uuid.UUID,
	policy inboxAIPolicyRecord,
	controlRevision int64,
) (uuid.UUID, error) {
	var sessionID uuid.UUID
	var mode string
	err := tx.QueryRow(ctx, `
		SELECT id, mode FROM inbox_ai_sessions WHERE conversation_id=$1 FOR UPDATE
	`, conversationID).Scan(&sessionID, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		sessionID = uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO inbox_ai_sessions (
				id, conversation_id, client_id, mode, state, policy_revision,
				control_revision, max_turns_snapshot, expires_at, created_at, updated_at
			) VALUES (
				$1,$2,$3,'autopilot','collecting_preferences',$4,$5,$6,
				NOW()+($7::int * INTERVAL '1 minute'),NOW(),NOW()
			)
		`, sessionID, conversationID, clientID, policy.revision, controlRevision,
			policy.maxTurns, policy.inactivityTimeoutMinutes)
		if err != nil {
			return uuid.Nil, fmt.Errorf("create autopilot AI session: %w", err)
		}
		return sessionID, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("lock autopilot AI session: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET
			mode='autopilot', state='collecting_preferences', selected_service_id=NULL,
			booking_link_url='', booking_link_revision=0, policy_revision=$2,
			control_revision=$3, turn_count=0, max_turns_snapshot=$4,
			handoff_reason='', revision=revision+1,
			expires_at=NOW()+($5::int * INTERVAL '1 minute'), completed_at=NULL, updated_at=NOW()
		WHERE id=$1
	`, sessionID, policy.revision, controlRevision, policy.maxTurns,
		policy.inactivityTimeoutMinutes); err != nil {
		return uuid.Nil, fmt.Errorf("reset autopilot AI session: %w", err)
	}
	return sessionID, nil
}

func (r *Repository) authorizeInboxAIBookingRead(
	ctx context.Context,
	marketplaceCustomerID, conversationID, serviceID uuid.UUID,
) (uuid.UUID, string, inboxAIPolicyRecord, error) {
	var clientID uuid.UUID
	var providerHandle string
	if err := r.db.QueryRow(ctx, `
		SELECT conversation.client_id, profile.handle_slug
		FROM inbox_conversations conversation
		INNER JOIN client_profiles profile ON profile.client_id=conversation.client_id
		WHERE conversation.id=$1 AND conversation.marketplace_customer_id=$2
		  AND conversation.disabled_at IS NULL
	`, conversationID, marketplaceCustomerID).Scan(&clientID, &providerHandle); errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", inboxAIPolicyRecord{}, ErrNotFound
	} else if err != nil {
		return uuid.Nil, "", inboxAIPolicyRecord{}, err
	}
	policy, err := loadInboxAIPolicyRecord(ctx, r.db, clientID)
	if err != nil {
		return uuid.Nil, "", inboxAIPolicyRecord{}, err
	}
	control, err := loadInboxAIConversationControl(
		ctx, r.db, clientID, conversationID, policy, r.inboxAIAutomationAvailable(clientID),
	)
	if err != nil {
		return uuid.Nil, "", inboxAIPolicyRecord{}, err
	}
	if control.EffectiveMode != InboxAIModeAutopilot || control.State != "active" || policy.paused {
		return uuid.Nil, "", inboxAIPolicyRecord{}, ErrInboxAIControlBlocked
	}
	if !inboxAIUUIDSliceContains(policy.enabledServiceIDs, serviceID) {
		return uuid.Nil, "", inboxAIPolicyRecord{}, ErrInboxAIServiceUnavailable
	}
	requirements, err := r.BookingApplication().GetRequirements(ctx, providerHandle, serviceID)
	if err != nil {
		return uuid.Nil, "", inboxAIPolicyRecord{}, err
	}
	if !requirements.Autopilot.Eligible {
		return uuid.Nil, "", inboxAIPolicyRecord{}, fmt.Errorf("%w: %s", ErrInboxAIServiceUnavailable, requirements.Autopilot.BlockReason)
	}
	return clientID, providerHandle, policy, nil
}

func loadInboxAIServiceChoices(
	ctx context.Context,
	q inboxQueryer,
	clientID uuid.UUID,
	enabledServiceIDs []uuid.UUID,
) ([]map[string]any, error) {
	rows, err := q.Query(ctx, `
		SELECT service.id, service.title, service.duration_minutes,
			service.price_amount_minor, service.currency_code, service.fulfillment_mode,
			profile.country_code
		FROM services service
		INNER JOIN client_profiles profile ON profile.client_id=service.client_id
		WHERE service.client_id=$1 AND service.id=ANY($2::uuid[])
		  AND service.status='published' AND service.is_active AND NOT service.is_hidden
		  AND NOT service.standalone_signature_required
		ORDER BY service.sort_order, service.title, service.id
		LIMIT 6
	`, clientID, enabledServiceIDs)
	if err != nil {
		return nil, fmt.Errorf("load inbox autopilot service choices: %w", err)
	}
	defer rows.Close()
	choices := make([]map[string]any, 0, 6)
	for rows.Next() {
		var id uuid.UUID
		var title, currencyCode, fulfillmentMode, countryCode string
		var duration int
		var amount int64
		if err := rows.Scan(&id, &title, &duration, &amount, &currencyCode, &fulfillmentMode, &countryCode); err != nil {
			return nil, err
		}
		label, _ := formatMarketMoney(amount, countryCode, currencyCode)
		if label == "" {
			label = currencyCode + " 0"
		}
		choices = append(choices, map[string]any{
			"service_id": id.String(), "title": title, "duration_minutes": duration,
			"price_label": label, "fulfillment_mode": fulfillmentMode,
		})
	}
	return choices, rows.Err()
}

func auditInboxAIBookingAction(
	ctx context.Context,
	tx pgx.Tx,
	sessionID, conversationID, clientID, idempotencyKey uuid.UUID,
	actionName string,
	input, result map[string]any,
) error {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_actions (
			id, session_id, conversation_id, client_id, idempotency_key,
			action_name, status, safe_input, safe_result, started_at, completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,'succeeded',$7,$8,NOW(),NOW())
		ON CONFLICT (session_id,idempotency_key) DO UPDATE SET
			safe_result=EXCLUDED.safe_result, status='succeeded', error_code='', completed_at=NOW()
		WHERE inbox_ai_actions.action_name=EXCLUDED.action_name
		  AND inbox_ai_actions.safe_input=EXCLUDED.safe_input
	`, uuid.New(), sessionID, conversationID, clientID, idempotencyKey,
		actionName, inputJSON, resultJSON)
	if err != nil {
		return fmt.Errorf("audit inbox booking action: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxIdempotencyConflict
	}
	return nil
}

func beginInboxAIBookingAction(
	ctx context.Context,
	tx pgx.Tx,
	sessionID, conversationID, clientID, idempotencyKey uuid.UUID,
	actionName string,
	input map[string]any,
) error {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_actions (
			id, session_id, conversation_id, client_id, idempotency_key,
			action_name, status, safe_input, started_at
		) VALUES ($1,$2,$3,$4,$5,$6,'running',$7,NOW())
		ON CONFLICT (session_id,idempotency_key) DO NOTHING
	`, uuid.New(), sessionID, conversationID, clientID, idempotencyKey, actionName, inputJSON)
	if err != nil {
		return fmt.Errorf("begin inbox booking action: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var storedAction, status string
	var sameInput bool
	if err := tx.QueryRow(ctx, `
		SELECT action_name, status, safe_input=$3::jsonb
		FROM inbox_ai_actions WHERE session_id=$1 AND idempotency_key=$2
	`, sessionID, idempotencyKey, inputJSON).Scan(&storedAction, &status, &sameInput); err != nil {
		return err
	}
	if storedAction != actionName || !sameInput || status != "succeeded" {
		return ErrInboxIdempotencyConflict
	}
	return nil
}

func (r *Repository) failInboxAIBookingAction(
	ctx context.Context,
	conversationID, idempotencyKey uuid.UUID,
	errorCode string,
) {
	_, _ = r.db.Exec(ctx, `
		UPDATE inbox_ai_actions SET status='failed', error_code=$3, completed_at=NOW()
		WHERE conversation_id=$1 AND idempotency_key=$2 AND status='running'
	`, conversationID, idempotencyKey, errorCode)
}

func (r *Repository) discardUnattachedInboxAIQuote(ctx context.Context, quoteToken string) {
	_, _ = r.db.Exec(ctx, `
		DELETE FROM booking_quotes quote
		WHERE quote.public_token=$1 AND quote.booking_id IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM inbox_ai_booking_sessions session WHERE session.quote_id=quote.id
		  )
	`, strings.TrimSpace(quoteToken))
}

func loadInboxAIWorkflowMessageReplay(
	ctx context.Context,
	tx pgx.Tx,
	marketplaceCustomerID, conversationID, idempotencyKey uuid.UUID,
	actionName string,
	safeInput map[string]any,
) (InboxMessage, bool, error) {
	var clientID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT client_id FROM inbox_conversations
		WHERE id=$1 AND marketplace_customer_id=$2
	`, conversationID, marketplaceCustomerID).Scan(&clientID); errors.Is(err, pgx.ErrNoRows) {
		return InboxMessage{}, false, ErrNotFound
	} else if err != nil {
		return InboxMessage{}, false, err
	}
	inputJSON, err := json.Marshal(safeInput)
	if err != nil {
		return InboxMessage{}, false, err
	}
	var storedActionName, actionStatus string
	var sameInput bool
	err = tx.QueryRow(ctx, `
		SELECT action.action_name, action.status, action.safe_input=$4::jsonb
		FROM inbox_ai_actions action
		INNER JOIN inbox_ai_sessions session ON session.id=action.session_id
		WHERE action.conversation_id=$1 AND action.client_id=$2
		  AND action.idempotency_key=$3 AND session.conversation_id=$1
		ORDER BY action.started_at DESC, action.id DESC
		LIMIT 1
	`, conversationID, clientID, idempotencyKey, inputJSON).Scan(
		&storedActionName, &actionStatus, &sameInput,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboxMessage{}, false, nil
	}
	if err != nil {
		return InboxMessage{}, false, err
	}
	if storedActionName != actionName || !sameInput {
		return InboxMessage{}, false, ErrInboxIdempotencyConflict
	}
	if actionStatus != "succeeded" {
		return InboxMessage{}, false, ErrInboxIdempotencyConflict
	}

	var message InboxMessage
	var id, senderID, clientMessageID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id, conversation_id, sender_id, client_message_id, content,
			message_type, presentation, sent_at
		FROM inbox_messages
		WHERE conversation_id=$1 AND sender_type='ai' AND sender_id=$2 AND client_message_id=$3
	`, conversationID, clientID, idempotencyKey).Scan(
		&id, &message.ConversationID, &senderID, &clientMessageID, &message.Content,
		&message.MessageType, &message.Presentation, &message.SentAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboxMessage{}, false, fmt.Errorf("replayed inbox booking action has no message")
	}
	if err != nil {
		return InboxMessage{}, false, err
	}
	message.ID = id.String()
	message.ConversationID = conversationID.String()
	message.SenderType = "ai"
	message.SenderID = senderID.String()
	message.ClientMessageID = clientMessageID.String()
	return message, true, nil
}

func loadInboxAIBookingActionReplay(
	ctx context.Context,
	q inboxQueryer,
	conversationID, clientID, idempotencyKey uuid.UUID,
	actionName string,
	safeInput map[string]any,
) (bool, error) {
	inputJSON, err := json.Marshal(safeInput)
	if err != nil {
		return false, err
	}
	var storedActionName, status string
	var sameInput bool
	err = q.QueryRow(ctx, `
		SELECT action_name, status, safe_input=$4::jsonb
		FROM inbox_ai_actions
		WHERE conversation_id=$1 AND client_id=$2 AND idempotency_key=$3
		ORDER BY started_at DESC, id DESC LIMIT 1
	`, conversationID, clientID, idempotencyKey, inputJSON).Scan(
		&storedActionName, &status, &sameInput,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedActionName != actionName || !sameInput || status != "succeeded" {
		return false, ErrInboxIdempotencyConflict
	}
	return true, nil
}

func (r *Repository) commitInboxAIBookingMessageReplay(
	ctx context.Context,
	tx pgx.Tx,
	marketplaceCustomerID, conversationID uuid.UUID,
	message InboxMessage,
) (InboxAIBookingActionResult, error) {
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	summary, err := loadMarketplaceConversationSummary(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	return InboxAIBookingActionResult{
		Workflow: workflow, Message: &message, Conversation: &summary, Replayed: true,
	}, nil
}

func (r *Repository) appendInboxAIWorkflowMessageTx(
	ctx context.Context,
	tx pgx.Tx,
	clientID, marketplaceCustomerID, conversationID, clientMessageID uuid.UUID,
	content string,
	presentation InboxMessagePresentation,
) (InboxMessage, InboxConversationSummary, error) {
	return r.appendInboxWorkflowMessageTx(
		ctx, tx, clientID, marketplaceCustomerID, conversationID, clientMessageID,
		"ai", uuid.NullUUID{}, content, presentation,
	)
}

func (r *Repository) appendInboxCustomerWorkflowMessageTx(
	ctx context.Context,
	tx pgx.Tx,
	clientID, marketplaceCustomerID, conversationID, clientMessageID uuid.UUID,
	content string,
	presentation InboxMessagePresentation,
) (InboxMessage, InboxConversationSummary, error) {
	return r.appendInboxWorkflowMessageTx(
		ctx, tx, clientID, marketplaceCustomerID, conversationID, clientMessageID,
		"marketplace_customer", uuid.NullUUID{}, content, presentation,
	)
}

func (r *Repository) appendInboxSystemWorkflowMessageTx(
	ctx context.Context,
	tx pgx.Tx,
	clientID, marketplaceCustomerID, conversationID, clientMessageID, bookingID uuid.UUID,
	content string,
	presentation InboxMessagePresentation,
) (InboxMessage, InboxConversationSummary, error) {
	return r.appendInboxWorkflowMessageTx(
		ctx, tx, clientID, marketplaceCustomerID, conversationID, clientMessageID,
		"system", uuid.NullUUID{UUID: bookingID, Valid: true}, content, presentation,
	)
}

func (r *Repository) appendInboxWorkflowMessageTx(
	ctx context.Context,
	tx pgx.Tx,
	clientID, marketplaceCustomerID, conversationID, clientMessageID uuid.UUID,
	senderType string,
	bookingID uuid.NullUUID,
	content string,
	presentation InboxMessagePresentation,
) (InboxMessage, InboxConversationSummary, error) {
	content = normalizeInboxMessageContent(content)
	if err := validateInboxMessagePresentation(&presentation); err != nil {
		return InboxMessage{}, InboxConversationSummary{}, err
	}
	fingerprint, err := inboxMessageFingerprintWithPresentation(
		content, bookingID, uuid.NullUUID{}, &presentation,
	)
	if err != nil {
		return InboxMessage{}, InboxConversationSummary{}, err
	}
	messageID := uuid.New()
	senderID := clientID
	var senderIDValue any = senderID
	readerType := "provider"
	readerID := clientID
	otherType := "marketplace_customer"
	otherID := marketplaceCustomerID
	if senderType == "marketplace_customer" {
		senderID = marketplaceCustomerID
		readerType = "marketplace_customer"
		readerID = marketplaceCustomerID
		otherType = "provider"
		otherID = clientID
	} else if senderType == "system" {
		senderID = uuid.Nil
		senderIDValue = nil
		readerType = "marketplace_customer"
		readerID = marketplaceCustomerID
		otherType = "provider"
		otherID = clientID
	}
	message := InboxMessage{
		ID: messageID.String(), ConversationID: conversationID.String(), SenderType: senderType,
		ClientMessageID: clientMessageID.String(), Content: content,
		MessageType: "text", Presentation: &presentation,
	}
	if senderID != uuid.Nil {
		message.SenderID = senderID.String()
	}
	if bookingID.Valid {
		message.BookingID = bookingID.UUID.String()
	}
	var sequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_messages (
			id, conversation_id, sender_type, sender_id, client_message_id,
			request_fingerprint, booking_id, content, message_type, presentation
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'text',$9)
		RETURNING sent_at, sequence
	`, messageID, conversationID, senderType, senderIDValue, clientMessageID, fingerprint,
		nullableInboxUUID(bookingID), content, presentation).Scan(&message.SentAt, &sequence); err != nil {
		return InboxMessage{}, InboxConversationSummary{}, fmt.Errorf("insert inbox booking AI message: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_conversations SET preview=$2, last_message_sequence=$3,
			last_message_at=$4, updated_at=NOW() WHERE id=$1
	`, conversationID, inboxMessagePreview(content), sequence, message.SentAt); err != nil {
		return InboxMessage{}, InboxConversationSummary{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_participant_states (
			conversation_id, participant_type, participant_id, last_read_sequence, last_read_at
		) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (conversation_id,participant_type,participant_id) DO UPDATE SET
			last_read_sequence=GREATEST(inbox_participant_states.last_read_sequence,$4),
			last_read_at=CASE WHEN $4>inbox_participant_states.last_read_sequence
				THEN $5 ELSE inbox_participant_states.last_read_at END
	`, conversationID, readerType, readerID, sequence, message.SentAt); err != nil {
		return InboxMessage{}, InboxConversationSummary{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_participant_states SET archived_at=NULL
		WHERE conversation_id=$1 AND participant_type=$2 AND participant_id=$3
	`, conversationID, otherType, otherID); err != nil {
		return InboxMessage{}, InboxConversationSummary{}, err
	}
	var eventSequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_events (
			conversation_id, client_id, marketplace_customer_id, event_type, payload
		) VALUES (
			$1,$2,$3,'message.created',
			jsonb_build_object('conversation_id',$1::uuid::text,'message_id',$4::uuid::text,'message_sequence',$5::bigint)
		) RETURNING sequence
	`, conversationID, clientID, marketplaceCustomerID, messageID, sequence).Scan(&eventSequence); err != nil {
		return InboxMessage{}, InboxConversationSummary{}, err
	}
	if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
		return InboxMessage{}, InboxConversationSummary{}, err
	}
	summary, err := loadMarketplaceConversationSummary(ctx, tx, marketplaceCustomerID, conversationID)
	return message, summary, err
}

func normalizedInboxAIText(value string) string {
	return strings.TrimSpace(value)
}
