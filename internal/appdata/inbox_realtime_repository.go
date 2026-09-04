package appdata

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const inboxEventDrainLimit = 100

type inboxStoredEvent struct {
	Sequence       int64
	ConversationID uuid.UUID
	Type           string
	Payload        json.RawMessage
	CreatedAt      time.Time
}

type inboxStoredEventPayload struct {
	MessageID       string `json:"message_id"`
	ParticipantType string `json:"participant_type"`
}

func decodeInboxSyncCursor(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	if len(raw) > 32 {
		return 0, ErrInboxInvalidCursor
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 36, 64)
	if err != nil || value < 0 {
		return 0, ErrInboxInvalidCursor
	}
	return value, nil
}

func (r *Repository) ListInboxEventsAfter(
	ctx context.Context,
	actorType string,
	actorID uuid.UUID,
	after string,
	limit int,
) (InboxEventDrain, error) {
	if actorType != "provider" && actorType != "marketplace_customer" {
		return InboxEventDrain{}, ErrNotFound
	}
	if limit < 1 || limit > inboxEventDrainLimit {
		limit = inboxEventDrainLimit
	}
	afterSequence, err := decodeInboxSyncCursor(after)
	if err != nil {
		return InboxEventDrain{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return InboxEventDrain{}, fmt.Errorf("begin inbox event drain: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var globalMin, globalMax int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MIN(sequence),0), COALESCE(MAX(sequence),0) FROM inbox_events
	`).Scan(&globalMin, &globalMax); err != nil {
		return InboxEventDrain{}, fmt.Errorf("load inbox event retention bounds: %w", err)
	}
	actorSyncSequence, err := loadInboxActorSyncSequence(ctx, tx, actorType, actorID)
	if err != nil {
		return InboxEventDrain{}, err
	}
	if (globalMax == 0 && afterSequence > 0) || afterSequence > globalMax ||
		(afterSequence > 0 && globalMin > 0 && afterSequence < globalMin-1) {
		if err := tx.Commit(ctx); err != nil {
			return InboxEventDrain{}, fmt.Errorf("commit inbox event reset snapshot: %w", err)
		}
		return InboxEventDrain{
			Events: make([]InboxRealtimeEvent, 0), Cursor: encodeInboxSequenceCursor(actorSyncSequence),
			LatestCursor: encodeInboxSequenceCursor(actorSyncSequence), Reset: true,
		}, nil
	}

	actorColumn := "marketplace_customer_id"
	if actorType == "provider" {
		actorColumn = "client_id"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT sequence, conversation_id, event_type, payload, created_at
		FROM inbox_events
		WHERE %s=$1 AND sequence>$2
		ORDER BY sequence ASC
		LIMIT $3
	`, actorColumn), actorID, afterSequence, limit+1)
	if err != nil {
		return InboxEventDrain{}, fmt.Errorf("list %s inbox events: %w", actorType, err)
	}
	stored := make([]inboxStoredEvent, 0, limit+1)
	for rows.Next() {
		var event inboxStoredEvent
		if err := rows.Scan(
			&event.Sequence, &event.ConversationID, &event.Type, &event.Payload, &event.CreatedAt,
		); err != nil {
			rows.Close()
			return InboxEventDrain{}, fmt.Errorf("scan inbox event: %w", err)
		}
		stored = append(stored, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return InboxEventDrain{}, fmt.Errorf("iterate inbox events: %w", err)
	}
	rows.Close()
	hasMore := len(stored) > limit
	if hasMore {
		stored = stored[:limit]
	}
	cursorSequence := afterSequence
	if len(stored) > 0 {
		cursorSequence = stored[len(stored)-1].Sequence
	}

	conversationIDs := make([]uuid.UUID, 0, len(stored))
	messageIDs := make([]uuid.UUID, 0, len(stored))
	seenConversations := make(map[uuid.UUID]struct{}, len(stored))
	for _, event := range stored {
		if _, seen := seenConversations[event.ConversationID]; !seen {
			seenConversations[event.ConversationID] = struct{}{}
			conversationIDs = append(conversationIDs, event.ConversationID)
		}
		var payload inboxStoredEventPayload
		if event.Type == "message.created" && json.Unmarshal(event.Payload, &payload) == nil {
			if messageID, parseErr := uuid.Parse(payload.MessageID); parseErr == nil {
				messageIDs = append(messageIDs, messageID)
			}
		}
	}
	summaries, err := loadInboxEventSummaries(ctx, tx, actorType, actorID, conversationIDs)
	if err != nil {
		return InboxEventDrain{}, err
	}
	messages, err := loadInboxEventMessages(ctx, tx, messageIDs)
	if err != nil {
		return InboxEventDrain{}, err
	}
	unreadTotal, _, err := loadInboxActorCounts(ctx, tx, actorType, actorID)
	if err != nil {
		return InboxEventDrain{}, err
	}

	events := make([]InboxRealtimeEvent, 0, len(stored))
	for _, storedEvent := range stored {
		summary, ok := summaries[storedEvent.ConversationID]
		if !ok {
			continue
		}
		var payload inboxStoredEventPayload
		_ = json.Unmarshal(storedEvent.Payload, &payload)
		event := InboxRealtimeEvent{
			Cursor: encodeInboxSequenceCursor(storedEvent.Sequence), Type: storedEvent.Type,
			ConversationID: storedEvent.ConversationID.String(), Conversation: summary,
			ParticipantType: payload.ParticipantType, UnreadTotal: unreadTotal, CreatedAt: storedEvent.CreatedAt,
		}
		if storedEvent.Type == "message.created" {
			messageID, parseErr := uuid.Parse(payload.MessageID)
			if message, exists := messages[messageID]; parseErr == nil && exists {
				messageCopy := message
				event.Message = &messageCopy
			}
		}
		events = append(events, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxEventDrain{}, fmt.Errorf("commit inbox event drain: %w", err)
	}
	return InboxEventDrain{
		Events: events, Cursor: encodeInboxSequenceCursor(cursorSequence),
		LatestCursor: encodeInboxSequenceCursor(actorSyncSequence), HasMore: hasMore,
	}, nil
}

func loadInboxEventSummaries(
	ctx context.Context,
	queryer inboxQueryer,
	actorType string,
	actorID uuid.UUID,
	conversationIDs []uuid.UUID,
) (map[uuid.UUID]InboxConversationSummary, error) {
	result := make(map[uuid.UUID]InboxConversationSummary, len(conversationIDs))
	if len(conversationIDs) == 0 {
		return result, nil
	}
	var rows pgx.Rows
	var err error
	if actorType == "provider" {
		rows, err = queryer.Query(ctx, `
			SELECT conversation.id, conversation.channel, conversation.marketplace_customer_id,
				COALESCE(
					NULLIF(BTRIM(customer.full_name),''), NULLIF(customer.email,''),
					NULLIF(customer.phone_e164,''), NULLIF(customer.whatsapp_e164,''),
					'Tellbook customer'
				), '', '', conversation.preview,
				(
					SELECT COUNT(*)::int FROM inbox_messages message
					WHERE message.conversation_id=conversation.id
					  AND message.sequence > COALESCE(state.last_read_sequence,0)
					  AND message.sender_type NOT IN ('provider','ai')
					),
					EXISTS (
						SELECT 1 FROM inbox_ai_conversation_controls ai_control
						WHERE ai_control.conversation_id=conversation.id AND ai_control.state='handoff'
					),
					conversation.last_message_at, last_message.sender_type,
				state.archived_at IS NOT NULL, conversation.disabled_at IS NOT NULL,
				conversation.created_at, conversation.updated_at
			FROM inbox_conversations conversation
			INNER JOIN marketplace_customers customer ON customer.id=conversation.marketplace_customer_id
			LEFT JOIN inbox_participant_states state
				ON state.conversation_id=conversation.id
				AND state.participant_type='provider' AND state.participant_id=$1
			LEFT JOIN inbox_messages last_message ON last_message.sequence=conversation.last_message_sequence
			WHERE conversation.client_id=$1 AND conversation.id=ANY($2::uuid[])
		`, actorID, conversationIDs)
	} else {
		rows, err = queryer.Query(ctx, `
			SELECT conversation.id, conversation.channel, conversation.client_id,
				COALESCE(NULLIF(BTRIM(profile.business_name),''), client.full_name),
				COALESCE(handle.handle_slug,''), COALESCE(profile.avatar_url,''), conversation.preview,
				(
					SELECT COUNT(*)::int FROM inbox_messages message
					WHERE message.conversation_id=conversation.id
					  AND message.sequence > COALESCE(state.last_read_sequence,0)
					  AND message.sender_type <> 'marketplace_customer'
					),
					EXISTS (
						SELECT 1 FROM inbox_ai_conversation_controls ai_control
						WHERE ai_control.conversation_id=conversation.id AND ai_control.state='handoff'
					),
					conversation.last_message_at, last_message.sender_type,
				state.archived_at IS NOT NULL, conversation.disabled_at IS NOT NULL,
				conversation.created_at, conversation.updated_at
			FROM inbox_conversations conversation
			INNER JOIN clients client ON client.id=conversation.client_id
			LEFT JOIN client_profiles profile ON profile.client_id=conversation.client_id
			LEFT JOIN client_profile_handles handle
				ON handle.client_id=conversation.client_id AND handle.handle_slug=profile.handle_slug
			LEFT JOIN inbox_participant_states state
				ON state.conversation_id=conversation.id
				AND state.participant_type='marketplace_customer' AND state.participant_id=$1
			LEFT JOIN inbox_messages last_message ON last_message.sequence=conversation.last_message_sequence
			WHERE conversation.marketplace_customer_id=$1 AND conversation.id=ANY($2::uuid[])
		`, actorID, conversationIDs)
	}
	if err != nil {
		return nil, fmt.Errorf("load %s inbox event summaries: %w", actorType, err)
	}
	defer rows.Close()
	for rows.Next() {
		var summary InboxConversationSummary
		var conversationID, counterpartyID uuid.UUID
		var lastMessageSenderType *string
		if err := rows.Scan(
			&conversationID, &summary.Channel, &counterpartyID, &summary.Counterparty.Name,
			&summary.Counterparty.Handle, &summary.Counterparty.AvatarURL, &summary.Preview,
			&summary.UnreadCount, &summary.ProviderHandoffRequested,
			&summary.LastMessageAt, &lastMessageSenderType,
			&summary.Archived, &summary.Disabled, &summary.CreatedAt, &summary.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan inbox event summary: %w", err)
		}
		summary.ID = conversationID.String()
		summary.Counterparty.ID = counterpartyID.String()
		if actorType == "provider" {
			summary.Counterparty.Kind = "marketplace_customer"
		} else {
			summary.Counterparty.Kind = "provider"
		}
		if lastMessageSenderType != nil {
			summary.LastMessageSenderType = *lastMessageSenderType
		}
		summary.BookingContexts = make([]InboxBookingContext, 0)
		result[conversationID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox event summaries: %w", err)
	}

	bookingRows, err := queryer.Query(ctx, `
		SELECT conversation_id, id, title, status, start_at, timezone
		FROM (
			SELECT link.conversation_id, booking.id, booking.title, booking.status,
				booking.start_at, booking.timezone,
				ROW_NUMBER() OVER (
					PARTITION BY link.conversation_id ORDER BY booking.start_at DESC, booking.id DESC
				) AS position
			FROM inbox_conversation_bookings link
			INNER JOIN bookings booking ON booking.id=link.booking_id
			WHERE link.conversation_id=ANY($1::uuid[])
			  AND (($2='provider' AND booking.client_id=$3)
				OR ($2='marketplace_customer' AND booking.marketplace_customer_id=$3))
		) ranked
		WHERE position <= 10
		ORDER BY conversation_id, start_at DESC, id DESC
	`, conversationIDs, actorType, actorID)
	if err != nil {
		return nil, fmt.Errorf("load inbox event booking contexts: %w", err)
	}
	defer bookingRows.Close()
	for bookingRows.Next() {
		var conversationID, bookingID uuid.UUID
		var booking InboxBookingContext
		if err := bookingRows.Scan(
			&conversationID, &bookingID, &booking.ServiceTitle, &booking.Status,
			&booking.StartsAt, &booking.Timezone,
		); err != nil {
			return nil, fmt.Errorf("scan inbox event booking context: %w", err)
		}
		booking.ID = bookingID.String()
		summary, ok := result[conversationID]
		if ok {
			summary.BookingContexts = append(summary.BookingContexts, booking)
			result[conversationID] = summary
		}
	}
	if err := bookingRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox event booking contexts: %w", err)
	}
	return result, nil
}

func loadInboxEventMessages(
	ctx context.Context,
	queryer inboxQueryer,
	messageIDs []uuid.UUID,
) (map[uuid.UUID]InboxMessage, error) {
	result := make(map[uuid.UUID]InboxMessage, len(messageIDs))
	if len(messageIDs) == 0 {
		return result, nil
	}
	rows, err := queryer.Query(ctx, `
		SELECT id, conversation_id, sender_type, sender_id, client_message_id,
			booking_id, content, message_type, presentation, sent_at
		FROM inbox_messages WHERE id=ANY($1::uuid[])
	`, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("load inbox event messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, conversationID uuid.UUID
		var senderID, clientMessageID, bookingID uuid.NullUUID
		var message InboxMessage
		if err := rows.Scan(
			&id, &conversationID, &message.SenderType, &senderID, &clientMessageID,
			&bookingID, &message.Content, &message.MessageType, &message.Presentation, &message.SentAt,
		); err != nil {
			return nil, fmt.Errorf("scan inbox event message: %w", err)
		}
		message.ID = id.String()
		message.ConversationID = conversationID.String()
		if senderID.Valid {
			message.SenderID = senderID.UUID.String()
		}
		if clientMessageID.Valid {
			message.ClientMessageID = clientMessageID.UUID.String()
		}
		if bookingID.Valid {
			message.BookingID = bookingID.UUID.String()
		}
		result[id] = message
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox event messages: %w", err)
	}
	return result, nil
}
