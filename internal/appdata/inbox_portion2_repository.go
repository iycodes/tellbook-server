package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const initialInboxConversationLimit = 20

func (r *Repository) ListMarketplaceConversations(
	ctx context.Context,
	marketplaceCustomerID uuid.UUID,
	limit int,
	searchQuery string,
	cursor string,
) (InboxConversationListResponse, error) {
	return r.listInboxConversations(
		ctx, "marketplace_customer", marketplaceCustomerID, limit, searchQuery, "all", cursor,
	)
}

func (r *Repository) ListProviderConversations(
	ctx context.Context,
	clientID uuid.UUID,
	limit int,
	searchQuery string,
	state string,
	cursor string,
) (InboxConversationListResponse, error) {
	return r.listInboxConversations(ctx, "provider", clientID, limit, searchQuery, state, cursor)
}

func (r *Repository) listInboxConversations(
	ctx context.Context,
	actorType string,
	actorID uuid.UUID,
	limit int,
	searchQuery string,
	state string,
	cursorRaw string,
) (InboxConversationListResponse, error) {
	if limit < 1 || limit > 50 {
		limit = initialInboxConversationLimit
	}
	cursor, err := decodeInboxConversationCursor(cursorRaw)
	if err != nil {
		return InboxConversationListResponse{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return InboxConversationListResponse{}, fmt.Errorf("begin inbox list snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	normalizedSearch := strings.ToLower(strings.TrimSpace(searchQuery))
	var rows pgx.Rows
	if actorType == "provider" {
		searchClause := ""
		queryArgs := []any{actorID, limit + 1}
		if normalizedSearch != "" {
			searchClause = fmt.Sprintf(
				"AND lower(customer.full_name) LIKE '%%' || $%d || '%%'", len(queryArgs)+1,
			)
			queryArgs = append(queryArgs, normalizedSearch)
		}
		cursorClause := appendInboxListCursorClause(&queryArgs, cursor)
		stateClause := "AND state.archived_at IS NULL"
		switch state {
		case "unread":
			stateClause = `AND state.archived_at IS NULL AND EXISTS (
				SELECT 1 FROM inbox_messages unread_message
				WHERE unread_message.conversation_id=conversation.id
				  AND unread_message.sequence > COALESCE(state.last_read_sequence,0)
				  AND unread_message.sender_type NOT IN ('provider','ai')
			)`
		case "archived":
			stateClause = "AND state.archived_at IS NOT NULL"
		}
		rows, err = tx.Query(ctx, fmt.Sprintf(`
			SELECT conversation.id, conversation.channel, conversation.marketplace_customer_id,
				COALESCE(
					NULLIF(BTRIM(customer.full_name),''), NULLIF(customer.email,''),
					NULLIF(customer.phone_e164,''),
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
			WHERE conversation.client_id=$1
			  %s %s %s
			ORDER BY conversation.last_message_at DESC NULLS LAST, conversation.created_at DESC, conversation.id DESC
			LIMIT $2
		`, stateClause, searchClause, cursorClause), queryArgs...)
	} else {
		searchClause := ""
		queryArgs := []any{actorID, limit + 1}
		if normalizedSearch != "" {
			placeholder := len(queryArgs) + 1
			searchClause = fmt.Sprintf(`AND (
				lower(profile.business_name) LIKE '%%' || $%d || '%%'
				OR lower(client.full_name) LIKE '%%' || $%d || '%%'
			)`, placeholder, placeholder)
			queryArgs = append(queryArgs, normalizedSearch)
		}
		cursorClause := appendInboxListCursorClause(&queryArgs, cursor)
		rows, err = tx.Query(ctx, fmt.Sprintf(`
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
			WHERE conversation.marketplace_customer_id=$1 AND state.archived_at IS NULL
			  %s %s
			ORDER BY conversation.last_message_at DESC NULLS LAST, conversation.created_at DESC, conversation.id DESC
			LIMIT $2
		`, searchClause, cursorClause), queryArgs...)
	}
	if err != nil {
		return InboxConversationListResponse{}, fmt.Errorf("list %s inbox conversations: %w", actorType, err)
	}
	defer rows.Close()

	items := make([]InboxConversationSummary, 0, limit+1)
	conversationIDs := make([]uuid.UUID, 0, limit+1)
	for rows.Next() {
		var item InboxConversationSummary
		var conversationID, counterpartyID uuid.UUID
		var lastMessageSenderType *string
		if err := rows.Scan(
			&conversationID, &item.Channel, &counterpartyID, &item.Counterparty.Name,
			&item.Counterparty.Handle, &item.Counterparty.AvatarURL, &item.Preview,
			&item.UnreadCount, &item.ProviderHandoffRequested, &item.LastMessageAt,
			&lastMessageSenderType, &item.Archived, &item.Disabled,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return InboxConversationListResponse{}, fmt.Errorf("scan %s inbox conversation: %w", actorType, err)
		}
		item.ID = conversationID.String()
		item.Counterparty.ID = counterpartyID.String()
		if actorType == "provider" {
			item.Counterparty.Kind = "marketplace_customer"
		} else {
			item.Counterparty.Kind = "provider"
		}
		if lastMessageSenderType != nil {
			item.LastMessageSenderType = *lastMessageSenderType
		}
		item.BookingContexts = make([]InboxBookingContext, 0)
		items = append(items, item)
		conversationIDs = append(conversationIDs, conversationID)
	}
	if err := rows.Err(); err != nil {
		return InboxConversationListResponse{}, fmt.Errorf("iterate %s inbox conversations: %w", actorType, err)
	}
	nextCursor := ""
	if len(items) > limit {
		nextCursor = encodeInboxConversationCursor(items[limit-1])
		items = items[:limit]
		conversationIDs = conversationIDs[:limit]
	}

	if len(conversationIDs) > 0 {
		bookingRows, bookingErr := tx.Query(ctx, `
			SELECT conversation_id, id, title, status, start_at, timezone
			FROM (
				SELECT link.conversation_id, booking.id, booking.title, booking.status,
					booking.start_at, booking.timezone,
					ROW_NUMBER() OVER (
						PARTITION BY link.conversation_id
						ORDER BY booking.start_at DESC, booking.id DESC
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
		if bookingErr != nil {
			return InboxConversationListResponse{}, fmt.Errorf("load inbox list booking contexts: %w", bookingErr)
		}
		defer bookingRows.Close()
		itemIndexes := make(map[uuid.UUID]int, len(conversationIDs))
		for index, conversationID := range conversationIDs {
			itemIndexes[conversationID] = index
		}
		for bookingRows.Next() {
			var conversationID, bookingID uuid.UUID
			var booking InboxBookingContext
			if err := bookingRows.Scan(
				&conversationID, &bookingID, &booking.ServiceTitle, &booking.Status,
				&booking.StartsAt, &booking.Timezone,
			); err != nil {
				return InboxConversationListResponse{}, fmt.Errorf("scan inbox list booking context: %w", err)
			}
			booking.ID = bookingID.String()
			index := itemIndexes[conversationID]
			if len(items[index].BookingContexts) < 10 {
				items[index].BookingContexts = append(items[index].BookingContexts, booking)
			}
		}
		if err := bookingRows.Err(); err != nil {
			return InboxConversationListResponse{}, fmt.Errorf("iterate inbox list booking contexts: %w", err)
		}
	}

	var unreadTotal int
	var syncSequence int64
	if actorType == "provider" {
		err = tx.QueryRow(ctx, `
			SELECT COUNT(*)::int
			FROM inbox_messages message
			INNER JOIN inbox_conversations conversation ON conversation.id=message.conversation_id
			LEFT JOIN inbox_participant_states state
				ON state.conversation_id=conversation.id
				AND state.participant_type='provider' AND state.participant_id=$1
			WHERE conversation.client_id=$1
			  AND message.sequence > COALESCE(state.last_read_sequence,0)
			  AND message.sender_type NOT IN ('provider','ai')
		`, actorID).Scan(&unreadTotal)
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM inbox_events WHERE client_id=$1`, actorID).Scan(&syncSequence)
		}
	} else {
		err = tx.QueryRow(ctx, `
			SELECT COUNT(*)::int
			FROM inbox_messages message
			INNER JOIN inbox_conversations conversation ON conversation.id=message.conversation_id
			LEFT JOIN inbox_participant_states state
				ON state.conversation_id=conversation.id
				AND state.participant_type='marketplace_customer' AND state.participant_id=$1
			WHERE conversation.marketplace_customer_id=$1
			  AND message.sequence > COALESCE(state.last_read_sequence,0)
			  AND message.sender_type <> 'marketplace_customer'
		`, actorID).Scan(&unreadTotal)
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM inbox_events WHERE marketplace_customer_id=$1`, actorID).Scan(&syncSequence)
		}
	}
	if err != nil {
		return InboxConversationListResponse{}, fmt.Errorf("load %s inbox list cursors: %w", actorType, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxConversationListResponse{}, fmt.Errorf("commit inbox list snapshot: %w", err)
	}
	return InboxConversationListResponse{
		Items: items, UnreadTotal: unreadTotal, NextCursor: nextCursor,
		SyncCursor: encodeInboxSequenceCursor(syncSequence),
	}, nil
}

func (r *Repository) GetProviderConversationDetail(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	limit int,
) (InboxConversationDetailResponse, error) {
	if limit < 1 || limit > 50 {
		limit = initialInboxMessageLimit
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("begin provider conversation snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	summary, err := loadProviderConversationSummary(ctx, tx, clientID, conversationID)
	if err != nil {
		return InboxConversationDetailResponse{}, err
	}
	messages, nextBefore, err := loadInboxConversationMessages(ctx, tx, conversationID, limit, nil)
	if err != nil {
		return InboxConversationDetailResponse{}, err
	}
	var syncSequence int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM inbox_events WHERE client_id=$1`, clientID).Scan(&syncSequence); err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("load provider inbox sync cursor: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("commit provider conversation snapshot: %w", err)
	}
	return InboxConversationDetailResponse{
		Conversation: summary, Messages: messages, NextBeforeCursor: nextBefore,
		SyncCursor: encodeInboxSequenceCursor(syncSequence),
	}, nil
}

func loadProviderConversationSummary(
	ctx context.Context,
	queryer inboxQueryer,
	clientID, conversationID uuid.UUID,
) (InboxConversationSummary, error) {
	var summary InboxConversationSummary
	var conversation, customerID uuid.UUID
	var lastMessageSenderType *string
	err := queryer.QueryRow(ctx, `
		SELECT conversation.id, conversation.channel, conversation.marketplace_customer_id,
			COALESCE(
				NULLIF(BTRIM(customer.full_name),''), NULLIF(customer.email,''),
				NULLIF(customer.phone_e164,''),
				'Tellbook customer'
			), conversation.preview,
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
		WHERE conversation.id=$2 AND conversation.client_id=$1
	`, clientID, conversationID).Scan(
		&conversation, &summary.Channel, &customerID, &summary.Counterparty.Name,
		&summary.Preview, &summary.UnreadCount, &summary.ProviderHandoffRequested,
		&summary.LastMessageAt, &lastMessageSenderType,
		&summary.Archived, &summary.Disabled, &summary.CreatedAt, &summary.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboxConversationSummary{}, ErrNotFound
	}
	if err != nil {
		return InboxConversationSummary{}, fmt.Errorf("load provider conversation summary: %w", err)
	}
	summary.ID = conversation.String()
	summary.Counterparty.ID = customerID.String()
	summary.Counterparty.Kind = "marketplace_customer"
	if lastMessageSenderType != nil {
		summary.LastMessageSenderType = *lastMessageSenderType
	}

	rows, err := queryer.Query(ctx, `
		SELECT booking.id, booking.title, booking.status, booking.start_at, booking.timezone
		FROM inbox_conversation_bookings link
		INNER JOIN bookings booking ON booking.id=link.booking_id
		WHERE link.conversation_id=$1 AND booking.client_id=$2
		ORDER BY booking.start_at DESC, booking.id DESC LIMIT 10
	`, conversationID, clientID)
	if err != nil {
		return InboxConversationSummary{}, fmt.Errorf("load provider conversation bookings: %w", err)
	}
	defer rows.Close()
	summary.BookingContexts = make([]InboxBookingContext, 0)
	for rows.Next() {
		var booking InboxBookingContext
		var bookingID uuid.UUID
		if err := rows.Scan(&bookingID, &booking.ServiceTitle, &booking.Status, &booking.StartsAt, &booking.Timezone); err != nil {
			return InboxConversationSummary{}, fmt.Errorf("scan provider conversation booking: %w", err)
		}
		booking.ID = bookingID.String()
		summary.BookingContexts = append(summary.BookingContexts, booking)
	}
	if err := rows.Err(); err != nil {
		return InboxConversationSummary{}, fmt.Errorf("iterate provider conversation bookings: %w", err)
	}
	return summary, nil
}

func loadInboxConversationMessages(
	ctx context.Context,
	queryer inboxQueryer,
	conversationID uuid.UUID,
	limit int,
	beforeSequence *int64,
) ([]InboxMessage, string, error) {
	query := `
		SELECT id, conversation_id, sender_type, sender_id, client_message_id, booking_id,
			content, message_type, presentation, sent_at, sequence
		FROM inbox_messages WHERE conversation_id=$1
		ORDER BY sequence DESC LIMIT $2
	`
	args := []any{conversationID, limit + 1}
	if beforeSequence != nil {
		query = `
			SELECT id, conversation_id, sender_type, sender_id, client_message_id, booking_id,
				content, message_type, presentation, sent_at, sequence
			FROM inbox_messages WHERE conversation_id=$1 AND sequence < $2
			ORDER BY sequence DESC LIMIT $3
		`
		args = []any{conversationID, *beforeSequence, limit + 1}
	}
	rows, err := queryer.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("load inbox conversation messages: %w", err)
	}
	defer rows.Close()
	type sequencedMessage struct {
		message  InboxMessage
		sequence int64
	}
	newestFirst := make([]sequencedMessage, 0, limit+1)
	for rows.Next() {
		var item InboxMessage
		var id, conversation uuid.UUID
		var senderID, clientMessageID, bookingID uuid.NullUUID
		var sequence int64
		if err := rows.Scan(
			&id, &conversation, &item.SenderType, &senderID, &clientMessageID, &bookingID,
			&item.Content, &item.MessageType, &item.Presentation, &item.SentAt, &sequence,
		); err != nil {
			return nil, "", fmt.Errorf("scan inbox conversation message: %w", err)
		}
		item.ID = id.String()
		item.ConversationID = conversation.String()
		if senderID.Valid {
			item.SenderID = senderID.UUID.String()
		}
		if clientMessageID.Valid {
			item.ClientMessageID = clientMessageID.UUID.String()
		}
		if bookingID.Valid {
			item.BookingID = bookingID.UUID.String()
		}
		newestFirst = append(newestFirst, sequencedMessage{message: item, sequence: sequence})
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate inbox conversation messages: %w", err)
	}
	nextBefore := ""
	if len(newestFirst) > limit {
		nextBefore = encodeInboxSequenceCursor(newestFirst[limit-1].sequence)
		newestFirst = newestFirst[:limit]
	}
	messages := make([]InboxMessage, len(newestFirst))
	for index := range newestFirst {
		messages[len(newestFirst)-1-index] = newestFirst[index].message
	}
	return messages, nextBefore, nil
}

func (r *Repository) SendMarketplaceMessage(
	ctx context.Context,
	marketplaceCustomerID, conversationID, clientMessageID uuid.UUID,
	content string,
	bookingID uuid.NullUUID,
) (SendInboxMessageResponse, error) {
	return r.sendInboxMessage(
		ctx, "marketplace_customer", marketplaceCustomerID, conversationID,
		clientMessageID, content, bookingID, uuid.NullUUID{}, nil,
	)
}

func (r *Repository) SendProviderMessage(
	ctx context.Context,
	clientID, conversationID, clientMessageID uuid.UUID,
	content string,
	bookingID uuid.NullUUID,
	aiRunIDs ...uuid.NullUUID,
) (SendInboxMessageResponse, error) {
	aiRunID := uuid.NullUUID{}
	if len(aiRunIDs) > 0 {
		aiRunID = aiRunIDs[0]
	}
	return r.sendInboxMessage(ctx, "provider", clientID, conversationID, clientMessageID, content, bookingID, aiRunID, nil)
}

func (r *Repository) SendAIInboxMessage(
	ctx context.Context,
	clientID, conversationID, clientMessageID uuid.UUID,
	content string,
	bookingID uuid.NullUUID,
	presentation InboxMessagePresentation,
) (SendInboxMessageResponse, error) {
	if err := validateInboxMessagePresentation(&presentation); err != nil {
		return SendInboxMessageResponse{}, err
	}
	return r.sendInboxMessage(
		ctx, "ai", clientID, conversationID, clientMessageID, content,
		bookingID, uuid.NullUUID{}, &presentation,
	)
}

func (r *Repository) sendInboxMessage(
	ctx context.Context,
	actorType string,
	actorID, conversationID, clientMessageID uuid.UUID,
	content string,
	bookingID uuid.NullUUID,
	aiRunID uuid.NullUUID,
	presentation *InboxMessagePresentation,
) (SendInboxMessageResponse, error) {
	normalizedContent := normalizeInboxMessageContent(content)
	if normalizedContent == "" || utf8.RuneCountInString(normalizedContent) > 4000 {
		return SendInboxMessageResponse{}, ErrInboxInvalidContent
	}
	if actorType != "ai" && presentation != nil {
		return SendInboxMessageResponse{}, ErrInboxInvalidPresentation
	}
	fingerprint, err := inboxMessageFingerprintWithPresentation(
		normalizedContent, bookingID, aiRunID, presentation,
	)
	if err != nil {
		return SendInboxMessageResponse{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("begin inbox message command: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var clientID, marketplaceCustomerID uuid.UUID
	var latestMessageSequence int64
	var disabled bool
	if err := tx.QueryRow(ctx, `
		SELECT client_id, marketplace_customer_id, disabled_at IS NOT NULL,
			COALESCE(last_message_sequence,0)
		FROM inbox_conversations
		WHERE id=$1
		  AND (($2 IN ('provider','ai') AND client_id=$3)
			OR ($2='marketplace_customer' AND marketplace_customer_id=$3))
		FOR UPDATE
	`, conversationID, actorType, actorID).Scan(&clientID, &marketplaceCustomerID, &disabled, &latestMessageSequence); errors.Is(err, pgx.ErrNoRows) {
		return SendInboxMessageResponse{}, ErrNotFound
	} else if err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("authorize inbox message command: %w", err)
	}
	if disabled {
		return SendInboxMessageResponse{}, ErrInboxConversationDisabled
	}
	if aiRunID.Valid && actorType != "provider" {
		return SendInboxMessageResponse{}, ErrInboxAIDraftInvalid
	}

	var existing InboxMessage
	var existingID, existingConversationID uuid.UUID
	var existingSenderID, existingClientMessageID, existingBookingID uuid.NullUUID
	var existingFingerprint string
	err = tx.QueryRow(ctx, `
		SELECT id, conversation_id, sender_type, sender_id, client_message_id, booking_id,
			content, message_type, presentation, sent_at, request_fingerprint
		FROM inbox_messages
		WHERE conversation_id=$1 AND sender_type=$2 AND sender_id=$3 AND client_message_id=$4
	`, conversationID, actorType, actorID, clientMessageID).Scan(
		&existingID, &existingConversationID, &existing.SenderType, &existingSenderID,
		&existingClientMessageID, &existingBookingID, &existing.Content, &existing.MessageType, &existing.Presentation,
		&existing.SentAt, &existingFingerprint,
	)
	if err == nil {
		if existingFingerprint != fingerprint {
			return SendInboxMessageResponse{}, ErrInboxIdempotencyConflict
		}
		if aiRunID.Valid {
			var linkedMessageID uuid.NullUUID
			if err := tx.QueryRow(ctx, `
				SELECT provider_message_id FROM inbox_ai_runs
				WHERE id=$1 AND client_id=$2 AND conversation_id=$3
			`, aiRunID.UUID, actorID, conversationID).Scan(&linkedMessageID); err != nil ||
				!linkedMessageID.Valid || linkedMessageID.UUID != existingID {
				return SendInboxMessageResponse{}, ErrInboxAIDraftInvalid
			}
		}
		existing.ID = existingID.String()
		existing.ConversationID = existingConversationID.String()
		if existingSenderID.Valid {
			existing.SenderID = existingSenderID.UUID.String()
		}
		if existingClientMessageID.Valid {
			existing.ClientMessageID = existingClientMessageID.UUID.String()
		}
		if existingBookingID.Valid {
			existing.BookingID = existingBookingID.UUID.String()
		}
		summary, summaryErr := loadInboxSummaryForActor(ctx, tx, actorType, actorID, conversationID)
		if summaryErr != nil {
			return SendInboxMessageResponse{}, summaryErr
		}
		if err := tx.Commit(ctx); err != nil {
			return SendInboxMessageResponse{}, fmt.Errorf("commit idempotent inbox message replay: %w", err)
		}
		return SendInboxMessageResponse{
			Message: existing, Conversation: summary, Replayed: true,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SendInboxMessageResponse{}, fmt.Errorf("load idempotent inbox message: %w", err)
	}

	if bookingID.Valid {
		var linked bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM inbox_conversation_bookings
				WHERE conversation_id=$1 AND booking_id=$2
			)
		`, conversationID, bookingID.UUID).Scan(&linked); err != nil {
			return SendInboxMessageResponse{}, fmt.Errorf("validate inbox message booking context: %w", err)
		}
		if !linked {
			return SendInboxMessageResponse{}, ErrInboxBookingContext
		}
	}

	messageID := uuid.New()
	var message InboxMessage
	var sequence int64
	err = tx.QueryRow(ctx, `
		INSERT INTO inbox_messages (
			id, conversation_id, sender_type, sender_id, client_message_id,
			request_fingerprint, booking_id, content, message_type, presentation
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'text',$9)
		RETURNING sent_at, sequence
	`, messageID, conversationID, actorType, actorID, clientMessageID,
		fingerprint, nullableInboxUUID(bookingID), normalizedContent, presentation).Scan(&message.SentAt, &sequence)
	if err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("insert inbox message: %w", err)
	}
	message.ID = messageID.String()
	message.ConversationID = conversationID.String()
	message.SenderType = actorType
	message.SenderID = actorID.String()
	message.ClientMessageID = clientMessageID.String()
	if bookingID.Valid {
		message.BookingID = bookingID.UUID.String()
	}
	message.Content = normalizedContent
	message.MessageType = "text"
	message.Presentation = presentation

	if aiRunID.Valid {
		outcome := "sent_edited"
		var generatedDraft string
		if err := tx.QueryRow(ctx, `
			SELECT output_draft
			FROM inbox_ai_runs
			WHERE id=$1 AND client_id=$2 AND conversation_id=$3
			  AND requested_by=$2 AND status='completed' AND provider_outcome='pending'
			  AND latest_message_sequence=$4
			FOR UPDATE
		`, aiRunID.UUID, actorID, conversationID, latestMessageSequence).Scan(&generatedDraft); errors.Is(err, pgx.ErrNoRows) {
			return SendInboxMessageResponse{}, ErrInboxAIDraftStale
		} else if err != nil {
			return SendInboxMessageResponse{}, fmt.Errorf("validate inbox ai draft lineage: %w", err)
		}
		if normalizeInboxMessageContent(generatedDraft) == normalizedContent {
			outcome = "sent_unchanged"
		}
		finalHash := sha256.Sum256([]byte(normalizedContent))
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_runs
			SET provider_outcome=$2, provider_message_id=$3,
				provider_final_content_hash=$4, provider_outcome_at=NOW()
			WHERE id=$1
		`, aiRunID.UUID, outcome, messageID, hex.EncodeToString(finalHash[:])); err != nil {
			return SendInboxMessageResponse{}, fmt.Errorf("link inbox ai draft to provider message: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_conversations
		SET preview=$2, last_message_sequence=$3, last_message_at=$4, updated_at=NOW()
		WHERE id=$1 AND (last_message_sequence IS NULL OR last_message_sequence < $3)
	`, conversationID, inboxMessagePreview(normalizedContent), sequence, message.SentAt); err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("update inbox conversation summary: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_participant_states (
			conversation_id, participant_type, participant_id, last_read_sequence, last_read_at
		) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (conversation_id, participant_type, participant_id) DO UPDATE SET
			last_read_sequence=GREATEST(
				inbox_participant_states.last_read_sequence,
				EXCLUDED.last_read_sequence
			),
			last_read_at=CASE
				WHEN EXCLUDED.last_read_sequence > inbox_participant_states.last_read_sequence
				THEN EXCLUDED.last_read_at
				ELSE inbox_participant_states.last_read_at
			END
	`, conversationID, inboxParticipantTypeForSender(actorType), actorID, sequence, message.SentAt); err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("advance inbox message sender read cursor: %w", err)
	}
	recipientType := "provider"
	recipientID := clientID
	if actorType == "provider" || actorType == "ai" {
		recipientType = "marketplace_customer"
		recipientID = marketplaceCustomerID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_participant_states SET archived_at=NULL
		WHERE conversation_id=$1 AND participant_type=$2 AND participant_id=$3
	`, conversationID, recipientType, recipientID); err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("unarchive inbox message recipient: %w", err)
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
		return SendInboxMessageResponse{}, fmt.Errorf("append inbox message event: %w", err)
	}
	if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("notify inbox message event: %w", err)
	}
	if actorType == "marketplace_customer" {
		if err := r.enqueueInboxAISemiPilotTurn(
			ctx, tx, conversationID, clientID, marketplaceCustomerID, messageID, sequence,
		); err != nil {
			return SendInboxMessageResponse{}, err
		}
	} else if actorType == "provider" {
		if err := r.fenceInboxAISemiPilotForProviderMessage(
			ctx, tx, conversationID, clientID, marketplaceCustomerID,
		); err != nil {
			return SendInboxMessageResponse{}, err
		}
	}
	summary, err := loadInboxSummaryForActor(ctx, tx, actorType, actorID, conversationID)
	if err != nil {
		return SendInboxMessageResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SendInboxMessageResponse{}, fmt.Errorf("commit inbox message command: %w", err)
	}
	return SendInboxMessageResponse{
		Message: message, Conversation: summary, Replayed: false,
	}, nil
}

func loadInboxSummaryForActor(
	ctx context.Context,
	queryer inboxQueryer,
	actorType string,
	actorID, conversationID uuid.UUID,
) (InboxConversationSummary, error) {
	if actorType == "provider" || actorType == "ai" {
		return loadProviderConversationSummary(ctx, queryer, actorID, conversationID)
	}
	return loadMarketplaceConversationSummary(ctx, queryer, actorID, conversationID)
}

func normalizeInboxMessageContent(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.TrimSpace(content)
}

func inboxMessageFingerprint(content string, bookingID uuid.NullUUID, aiRunIDs ...uuid.NullUUID) string {
	bookingValue := ""
	if bookingID.Valid {
		bookingValue = bookingID.UUID.String()
	}
	aiRunValue := ""
	aiRunID := uuid.NullUUID{}
	if len(aiRunIDs) > 0 {
		aiRunID = aiRunIDs[0]
	}
	if aiRunID.Valid {
		aiRunValue = aiRunID.UUID.String()
	}
	sum := sha256.Sum256([]byte(content + "\x00" + bookingValue + "\x00" + aiRunValue))
	return hex.EncodeToString(sum[:])
}

func inboxMessageFingerprintWithPresentation(
	content string,
	bookingID uuid.NullUUID,
	aiRunID uuid.NullUUID,
	presentation *InboxMessagePresentation,
) (string, error) {
	base := inboxMessageFingerprint(content, bookingID, aiRunID)
	if presentation == nil {
		return base, nil
	}
	encoded, err := json.Marshal(presentation)
	if err != nil {
		return "", ErrInboxInvalidPresentation
	}
	sum := sha256.Sum256(append([]byte(base+"\x00"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

func inboxParticipantTypeForSender(senderType string) string {
	if senderType == "ai" {
		return "provider"
	}
	return senderType
}

func inboxMessagePreview(content string) string {
	runes := []rune(strings.Join(strings.Fields(content), " "))
	if len(runes) > 240 {
		runes = runes[:240]
	}
	return string(runes)
}

func nullableInboxUUID(value uuid.NullUUID) any {
	if value.Valid {
		return value.UUID
	}
	return nil
}
