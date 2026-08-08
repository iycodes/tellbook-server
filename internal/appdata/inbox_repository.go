package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	aiapi "booking/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const inboxComposeLockTTL = 45 * time.Second

type inboxAgentContext struct {
	Conversation     InboxConversationItem
	BusinessName     string
	BusinessCategory string
	HandleSlug       string
	RecentTurns      []aiapi.MessageTurn
	ContextItems     []aiapi.NamedValue
}

func (r *Repository) ListInboxConversations(ctx context.Context, clientID uuid.UUID) ([]InboxConversationItem, error) {
	const query = `
		SELECT
			ic.id,
			COALESCE(ic.customer_id::text, ''),
			COALESCE(c.full_name, ''),
			ic.lead_name,
			ic.lead_contact,
			ic.external_lead_id,
			ic.source,
			ic.status,
			ic.subject,
			ic.preview,
			COALESCE(ic.avatar_url, ''),
			ic.autopilot_mode,
			ic.agent_state,
			ic.human_takeover,
			CASE
				WHEN ic.human_composing = TRUE
				 AND ic.human_composing_expires_at IS NOT NULL
				 AND ic.human_composing_expires_at > NOW()
				THEN TRUE
				ELSE FALSE
			END,
			ic.last_message_at,
			ic.last_ai_reply_at
		FROM inbox_conversations ic
		LEFT JOIN customers c ON c.id = ic.customer_id
		WHERE ic.client_id = $1
		ORDER BY ic.last_message_at DESC, ic.created_at DESC
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("list inbox conversations: %w", err)
	}
	defer rows.Close()

	items := make([]InboxConversationItem, 0)
	for rows.Next() {
		item, err := scanInboxConversation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox conversations: %w", err)
	}

	return items, nil
}

func (r *Repository) GetInboxConversationDetails(ctx context.Context, clientID, conversationID uuid.UUID) (InboxConversationDetailsResponse, error) {
	conversation, err := r.getInboxConversation(ctx, clientID, conversationID)
	if err != nil {
		return InboxConversationDetailsResponse{}, err
	}

	const query = `
		SELECT
			id,
			sender_role,
			content,
			message_type,
			action_type,
			sent_at
		FROM inbox_messages
		WHERE conversation_id = $1
		ORDER BY sent_at ASC, created_at ASC
	`

	rows, err := r.db.Query(ctx, query, conversationID)
	if err != nil {
		return InboxConversationDetailsResponse{}, fmt.Errorf("list inbox messages: %w", err)
	}
	defer rows.Close()

	messages := make([]InboxMessageItem, 0)
	for rows.Next() {
		item, err := scanInboxMessage(rows)
		if err != nil {
			return InboxConversationDetailsResponse{}, err
		}
		messages = append(messages, item)
	}
	if err := rows.Err(); err != nil {
		return InboxConversationDetailsResponse{}, fmt.Errorf("iterate inbox messages: %w", err)
	}

	return InboxConversationDetailsResponse{
		Conversation: conversation,
		Messages:     messages,
	}, nil
}

func (r *Repository) UpdateInboxConversationControls(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	mode string,
	humanTakeover bool,
) (InboxConversationItem, error) {
	mode = normalizeInboxMode(mode)

	status := "open"
	agentState := "awaiting_customer"
	if humanTakeover || mode == "manual" {
		status = "needs_human"
		agentState = "manual_handoff"
	}

	const query = `
		UPDATE inbox_conversations
		SET
			autopilot_mode = $3,
			human_takeover = $4,
			status = $5,
			agent_state = $6,
			human_composing = FALSE,
			human_composing_started_at = NULL,
			human_composing_expires_at = NULL,
			updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`

	commandTag, err := r.db.Exec(ctx, query, clientID, conversationID, mode, humanTakeover, status, agentState)
	if err != nil {
		return InboxConversationItem{}, fmt.Errorf("update inbox conversation controls: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return InboxConversationItem{}, ErrNotFound
	}

	return r.getInboxConversation(ctx, clientID, conversationID)
}

func (r *Repository) UpdateInboxConversationComposeState(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	isComposing bool,
) (InboxConversationItem, error) {
	const query = `
		UPDATE inbox_conversations
		SET
			human_composing = $3,
			human_composing_started_at = CASE WHEN $3 THEN NOW() ELSE NULL END,
			human_composing_expires_at = CASE WHEN $3 THEN NOW() + $4::interval ELSE NULL END,
			updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`

	commandTag, err := r.db.Exec(ctx, query, clientID, conversationID, isComposing, intervalString(inboxComposeLockTTL))
	if err != nil {
		return InboxConversationItem{}, fmt.Errorf("update inbox conversation compose state: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return InboxConversationItem{}, ErrNotFound
	}

	return r.getInboxConversation(ctx, clientID, conversationID)
}

func (r *Repository) SendInboxConversationMessage(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	content string,
) (InboxMessageItem, InboxConversationItem, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return InboxMessageItem{}, InboxConversationItem{}, fmt.Errorf("message content is required")
	}

	message, err := r.AppendInboxMessage(ctx, clientID, conversationID, "client", content, "text", "", time.Now().UTC())
	if err != nil {
		return InboxMessageItem{}, InboxConversationItem{}, err
	}

	const query = `
		UPDATE inbox_conversations
		SET
			autopilot_mode = 'manual',
			human_takeover = TRUE,
			status = 'needs_human',
			agent_state = 'human_replied',
			human_composing = FALSE,
			human_composing_started_at = NULL,
			human_composing_expires_at = NULL,
			updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`
	if _, err := r.db.Exec(ctx, query, clientID, conversationID); err != nil {
		return InboxMessageItem{}, InboxConversationItem{}, fmt.Errorf("update inbox conversation after human reply: %w", err)
	}

	conversation, err := r.getInboxConversation(ctx, clientID, conversationID)
	if err != nil {
		return InboxMessageItem{}, InboxConversationItem{}, err
	}

	return message, conversation, nil
}

func (r *Repository) AppendInboxMessage(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	senderRole, content, messageType, actionType string,
	sentAt time.Time,
) (InboxMessageItem, error) {
	messageID := uuid.New()
	message := InboxMessageItem{
		ID:          messageID.String(),
		SenderRole:  strings.TrimSpace(senderRole),
		Content:     strings.TrimSpace(content),
		MessageType: strings.TrimSpace(messageType),
		ActionType:  strings.TrimSpace(actionType),
		SentAt:      sentAt.UTC(),
	}
	if message.MessageType == "" {
		message.MessageType = "text"
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxMessageItem{}, fmt.Errorf("begin append inbox message: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	const insertQuery = `
		INSERT INTO inbox_messages (
			id,
			conversation_id,
			sender_role,
			content,
			message_type,
			action_type,
			sent_at,
			created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NOW())
	`
	if _, err := tx.Exec(
		ctx,
		insertQuery,
		messageID,
		conversationID,
		message.SenderRole,
		message.Content,
		message.MessageType,
		message.ActionType,
		message.SentAt,
	); err != nil {
		return InboxMessageItem{}, fmt.Errorf("insert inbox message: %w", err)
	}

	const updateQuery = `
		UPDATE inbox_conversations
		SET
			preview = $3,
			last_message_at = $4,
			updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`
	if _, err := tx.Exec(
		ctx,
		updateQuery,
		clientID,
		conversationID,
		buildInboxPreview(message.Content),
		message.SentAt,
	); err != nil {
		return InboxMessageItem{}, fmt.Errorf("touch inbox conversation: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return InboxMessageItem{}, fmt.Errorf("commit inbox message: %w", err)
	}

	return message, nil
}

func (r *Repository) UpdateInboxConversationAfterAgentStep(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	mode string,
	step aiapi.ConversationAgentStepResponse,
	replySentAt *time.Time,
) (InboxConversationItem, error) {
	status := "awaiting_customer"
	humanTakeover := step.Action == aiapi.AgentActionHandoffToHuman || step.NeedsHumanReview
	if humanTakeover {
		status = "needs_human"
	}

	nextState := strings.TrimSpace(step.NextState)
	if nextState == "" {
		switch step.Action {
		case aiapi.AgentActionSendBookingLink:
			nextState = "booking_link_sent"
		case aiapi.AgentActionBookingReady:
			nextState = "booking_ready"
		case aiapi.AgentActionHandoffToHuman:
			nextState = "needs_human"
		default:
			nextState = "awaiting_customer"
		}
	}

	const query = `
		UPDATE inbox_conversations
		SET
			autopilot_mode = $3,
			status = $4,
			agent_state = $5,
			human_takeover = $6,
			last_ai_reply_at = COALESCE($7::timestamptz, last_ai_reply_at),
			updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`
	if _, err := r.db.Exec(
		ctx,
		query,
		clientID,
		conversationID,
		normalizeInboxMode(mode),
		status,
		nextState,
		humanTakeover,
		replySentAt,
	); err != nil {
		return InboxConversationItem{}, fmt.Errorf("update inbox conversation after agent step: %w", err)
	}

	return r.getInboxConversation(ctx, clientID, conversationID)
}

func (r *Repository) BuildInboxAgentContext(ctx context.Context, clientID, conversationID uuid.UUID) (inboxAgentContext, error) {
	conversation, err := r.getInboxConversation(ctx, clientID, conversationID)
	if err != nil {
		return inboxAgentContext{}, err
	}

	const profileQuery = `
		SELECT
			COALESCE(cp.business_name, c.full_name),
			COALESCE(cp.category, ''),
			COALESCE(cp.handle_slug, '')
		FROM clients c
		LEFT JOIN client_profiles cp ON cp.client_id = c.id
		WHERE c.id = $1
	`

	var context inboxAgentContext
	context.Conversation = conversation
	if err := r.db.QueryRow(ctx, profileQuery, clientID).Scan(
		&context.BusinessName,
		&context.BusinessCategory,
		&context.HandleSlug,
	); err != nil {
		return inboxAgentContext{}, fmt.Errorf("load inbox agent business context: %w", err)
	}

	const servicesQuery = `
		SELECT
			s.title,
			s.duration_minutes,
			s.price_amount_minor,
			s.currency_code,
			cp.country_code,
			s.fulfillment_mode,
			COALESCE(bl.label, ''),
			s.minimum_notice_minutes,
			s.travel_fee_minor,
			EXISTS (SELECT 1 FROM service_short_notice_rules snr WHERE snr.service_id = s.id)
		FROM services s
		INNER JOIN client_profiles cp ON cp.client_id = s.client_id
		LEFT JOIN business_locations bl
			ON bl.id = s.provider_location_id AND bl.client_id = s.client_id
		WHERE s.client_id = $1
		  AND s.status = 'published'
		  AND COALESCE(s.is_hidden, FALSE) = FALSE
		ORDER BY s.title ASC
		LIMIT 12
	`
	rows, err := r.db.Query(ctx, servicesQuery, clientID)
	if err != nil {
		return inboxAgentContext{}, fmt.Errorf("load inbox agent services: %w", err)
	}
	defer rows.Close()

	serviceLines := make([]string, 0)
	for rows.Next() {
		var title string
		var durationMinutes int
		var priceAmountMinor int64
		var currencyCode string
		var countryCode string
		var locationType string
		var businessLocationLabel string
		var minimumNoticeMinutes int
		var travelFeeMinor int64
		var hasShortNoticePricing bool
		if err := rows.Scan(
			&title, &durationMinutes, &priceAmountMinor, &currencyCode, &countryCode,
			&locationType, &businessLocationLabel, &minimumNoticeMinutes,
			&travelFeeMinor, &hasShortNoticePricing,
		); err != nil {
			return inboxAgentContext{}, fmt.Errorf("scan inbox agent service: %w", err)
		}
		line := strings.TrimSpace(title)
		if durationMinutes > 0 {
			line += fmt.Sprintf(" • %d min", durationMinutes)
		}
		if priceAmountMinor > 0 {
			price, err := formatMarketMoney(priceAmountMinor, countryCode, currencyCode)
			if err != nil {
				return inboxAgentContext{}, fmt.Errorf("format inbox agent service price: %w", err)
			}
			line += " • " + price
		}
		if derivedLocation := summarizeServiceLocation(locationType, businessLocationLabel); derivedLocation != "" {
			line += " • " + derivedLocation
		}
		if minimumNoticeMinutes > 0 {
			line += fmt.Sprintf(" • %d min minimum notice", minimumNoticeMinutes)
		}
		if travelFeeMinor > 0 {
			travelFee, err := formatMarketMoney(travelFeeMinor, countryCode, currencyCode)
			if err != nil {
				return inboxAgentContext{}, fmt.Errorf("format inbox agent travel fee: %w", err)
			}
			line += " • " + travelFee + " travel fee"
		}
		if hasShortNoticePricing {
			line += " • final price may include a short-notice fee"
		}
		serviceLines = append(serviceLines, line)
	}
	if err := rows.Err(); err != nil {
		return inboxAgentContext{}, fmt.Errorf("iterate inbox agent services: %w", err)
	}

	context.ContextItems = []aiapi.NamedValue{
		{Key: "business_name", Value: context.BusinessName},
		{Key: "business_category", Value: context.BusinessCategory},
		{Key: "conversation_source", Value: conversation.Source},
	}
	if strings.TrimSpace(conversation.LeadContact) != "" {
		context.ContextItems = append(context.ContextItems, aiapi.NamedValue{Key: "lead_contact", Value: conversation.LeadContact})
	}
	if len(serviceLines) > 0 {
		context.ContextItems = append(context.ContextItems, aiapi.NamedValue{
			Key:   "published_services",
			Value: strings.Join(serviceLines, "\n"),
		})
	}

	const turnsQuery = `
		SELECT sender_role, content
		FROM inbox_messages
		WHERE conversation_id = $1
		ORDER BY sent_at DESC, created_at DESC
		LIMIT 12
	`
	turnRows, err := r.db.Query(ctx, turnsQuery, conversationID)
	if err != nil {
		return inboxAgentContext{}, fmt.Errorf("load inbox agent turns: %w", err)
	}
	defer turnRows.Close()

	reversed := make([]aiapi.MessageTurn, 0)
	for turnRows.Next() {
		var senderRole string
		var content string
		if err := turnRows.Scan(&senderRole, &content); err != nil {
			return inboxAgentContext{}, fmt.Errorf("scan inbox agent turn: %w", err)
		}
		reversed = append(reversed, aiapi.MessageTurn{
			Role:    normalizeAgentTurnRole(senderRole),
			Content: content,
		})
	}
	if err := turnRows.Err(); err != nil {
		return inboxAgentContext{}, fmt.Errorf("iterate inbox agent turns: %w", err)
	}

	context.RecentTurns = make([]aiapi.MessageTurn, 0, len(reversed))
	for index := len(reversed) - 1; index >= 0; index-- {
		context.RecentTurns = append(context.RecentTurns, reversed[index])
	}

	return context, nil
}

func (r *Repository) GetClientIDByHandleSlug(ctx context.Context, slug string) (uuid.UUID, error) {
	const query = `
		SELECT client_id
		FROM client_profile_handles
		WHERE handle_slug = $1
	`

	var clientID uuid.UUID
	if err := r.db.QueryRow(ctx, query, strings.TrimSpace(slug)).Scan(&clientID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.UUID{}, ErrNotFound
		}
		return uuid.UUID{}, fmt.Errorf("get client by handle slug: %w", err)
	}

	return clientID, nil
}

func (r *Repository) getInboxConversation(ctx context.Context, clientID, conversationID uuid.UUID) (InboxConversationItem, error) {
	const query = `
		SELECT
			ic.id,
			COALESCE(ic.customer_id::text, ''),
			COALESCE(c.full_name, ''),
			ic.lead_name,
			ic.lead_contact,
			ic.external_lead_id,
			ic.source,
			ic.status,
			ic.subject,
			ic.preview,
			COALESCE(ic.avatar_url, ''),
			ic.autopilot_mode,
			ic.agent_state,
			ic.human_takeover,
			CASE
				WHEN ic.human_composing = TRUE
				 AND ic.human_composing_expires_at IS NOT NULL
				 AND ic.human_composing_expires_at > NOW()
				THEN TRUE
				ELSE FALSE
			END,
			ic.last_message_at,
			ic.last_ai_reply_at
		FROM inbox_conversations ic
		LEFT JOIN customers c ON c.id = ic.customer_id
		WHERE ic.client_id = $1 AND ic.id = $2
	`

	row := r.db.QueryRow(ctx, query, clientID, conversationID)
	item, err := scanInboxConversation(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboxConversationItem{}, ErrNotFound
		}
		return InboxConversationItem{}, err
	}
	return item, nil
}

type inboxConversationScanner interface {
	Scan(dest ...any) error
}

func scanInboxConversation(scanner inboxConversationScanner) (InboxConversationItem, error) {
	var item InboxConversationItem
	var id uuid.UUID
	if err := scanner.Scan(
		&id,
		&item.CustomerID,
		&item.CustomerName,
		&item.LeadName,
		&item.LeadContact,
		&item.ExternalLeadID,
		&item.Source,
		&item.Status,
		&item.Subject,
		&item.Preview,
		&item.AvatarURL,
		&item.AutopilotMode,
		&item.AgentState,
		&item.HumanTakeover,
		&item.HumanComposing,
		&item.LastMessageAt,
		&item.LastAIReplyAt,
	); err != nil {
		return InboxConversationItem{}, fmt.Errorf("scan inbox conversation: %w", err)
	}
	item.ID = id.String()
	return item, nil
}

type inboxMessageScanner interface {
	Scan(dest ...any) error
}

func scanInboxMessage(scanner inboxMessageScanner) (InboxMessageItem, error) {
	var item InboxMessageItem
	var id uuid.UUID
	if err := scanner.Scan(
		&id,
		&item.SenderRole,
		&item.Content,
		&item.MessageType,
		&item.ActionType,
		&item.SentAt,
	); err != nil {
		return InboxMessageItem{}, fmt.Errorf("scan inbox message: %w", err)
	}
	item.ID = id.String()
	return item, nil
}

func normalizeInboxMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "auto_pilot":
		return "auto_pilot"
	case "manual":
		return "manual"
	default:
		return "semi_pilot"
	}
}

func buildInboxPreview(content string) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if len(content) <= 160 {
		return content
	}
	return content[:157] + "..."
}

func (r *Repository) ShouldBlockInboxAutoReply(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	basedOnMessageID uuid.UUID,
) (bool, error) {
	conversation, err := r.getInboxConversation(ctx, clientID, conversationID)
	if err != nil {
		return true, err
	}
	if conversation.AutopilotMode == "manual" || conversation.HumanTakeover || conversation.HumanComposing {
		return true, nil
	}

	const query = `
		SELECT id, sender_role
		FROM inbox_messages
		WHERE conversation_id = $1
		ORDER BY sent_at DESC, created_at DESC
		LIMIT 1
	`

	var latestMessageID uuid.UUID
	var senderRole string
	if err := r.db.QueryRow(ctx, query, conversationID).Scan(&latestMessageID, &senderRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return true, fmt.Errorf("load latest inbox message: %w", err)
	}

	if latestMessageID != basedOnMessageID {
		switch strings.TrimSpace(senderRole) {
		case "client", "human":
			return true, nil
		}
	}

	return false, nil
}

func normalizeAgentTurnRole(senderRole string) string {
	switch strings.TrimSpace(senderRole) {
	case "assistant", "ai":
		return "assistant"
	case "human", "client":
		return "assistant"
	default:
		return "customer"
	}
}

func summarizeServiceLocation(locationType, businessLocationLabel string) string {
	switch strings.TrimSpace(locationType) {
	case "virtual":
		return "Online"
	case "customer_location":
		if strings.TrimSpace(businessLocationLabel) != "" {
			return "Customer address; travel starts from " + strings.TrimSpace(businessLocationLabel)
		}
		return "Customer address"
	case "provider_location":
		if strings.TrimSpace(businessLocationLabel) != "" {
			return strings.TrimSpace(businessLocationLabel)
		}
		return "Provider location"
	default:
		return ""
	}
}

func intervalString(duration time.Duration) string {
	seconds := int(duration.Seconds())
	if seconds <= 0 {
		seconds = 1
	}
	return fmt.Sprintf("%d seconds", seconds)
}
