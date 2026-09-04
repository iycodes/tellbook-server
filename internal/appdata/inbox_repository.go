package appdata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const initialInboxMessageLimit = 20

type inboxQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Repository) GetOrCreateMarketplaceProviderConversation(
	ctx context.Context,
	marketplaceCustomerID, clientID uuid.UUID,
) (GetOrCreateInboxConversationResult, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("begin provider conversation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var providerEligible bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM client_profiles
			WHERE client_id=$1 AND marketplace_enabled AND market_configured_at IS NOT NULL
		)
	`, clientID).Scan(&providerEligible); err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("authorize marketplace provider conversation: %w", err)
	}
	if !providerEligible {
		return GetOrCreateInboxConversationResult{}, ErrNotFound
	}

	conversationID := uuid.New()
	created := false
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_conversations (
			id, client_id, customer_id, marketplace_customer_id, channel, created_at, updated_at
		) VALUES ($1,$2,NULL,$3,'tellbook',NOW(),NOW())
		ON CONFLICT (client_id, marketplace_customer_id) DO NOTHING
		RETURNING id
	`, conversationID, clientID, marketplaceCustomerID).Scan(&conversationID); err == nil {
		created = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("create marketplace provider conversation: %w", err)
	}

	if !created {
		if err := tx.QueryRow(ctx, `
			SELECT id FROM inbox_conversations
			WHERE client_id=$1 AND marketplace_customer_id=$2
			FOR UPDATE
		`, clientID, marketplaceCustomerID).Scan(&conversationID); err != nil {
			return GetOrCreateInboxConversationResult{}, fmt.Errorf("load marketplace provider conversation: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_participant_states (conversation_id, participant_type, participant_id)
		VALUES ($1,'provider',$2),($1,'marketplace_customer',$3)
		ON CONFLICT DO NOTHING
	`, conversationID, clientID, marketplaceCustomerID); err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("create provider conversation participant states: %w", err)
	}

	if created {
		var eventSequence int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_events (
				conversation_id, client_id, marketplace_customer_id, event_type, payload
			) VALUES (
				$1,$2,$3,'conversation.created',
				jsonb_build_object('conversation_id',$1::uuid::text,'provider_id',$2::uuid::text)
			)
			RETURNING sequence
		`, conversationID, clientID, marketplaceCustomerID).Scan(&eventSequence); err != nil {
			return GetOrCreateInboxConversationResult{}, fmt.Errorf("append provider conversation event: %w", err)
		}
		if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
			return GetOrCreateInboxConversationResult{}, fmt.Errorf("notify provider conversation event: %w", err)
		}
	}

	detail, err := loadMarketplaceConversationDetail(
		ctx, tx, marketplaceCustomerID, conversationID, initialInboxMessageLimit,
	)
	if err != nil {
		return GetOrCreateInboxConversationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("commit provider conversation: %w", err)
	}
	return GetOrCreateInboxConversationResult{Detail: detail, Created: created}, nil
}

func (r *Repository) GetOrCreateMarketplaceBookingConversation(
	ctx context.Context,
	marketplaceCustomerID, bookingID uuid.UUID,
) (GetOrCreateInboxConversationResult, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("begin get or create inbox conversation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var clientID, customerID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT client_id, customer_id
		FROM bookings
		WHERE id = $1 AND marketplace_customer_id = $2
		FOR UPDATE
	`, bookingID, marketplaceCustomerID).Scan(&clientID, &customerID); errors.Is(err, pgx.ErrNoRows) {
		return GetOrCreateInboxConversationResult{}, ErrNotFound
	} else if err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("authorize marketplace booking conversation: %w", err)
	}

	conversationID := uuid.New()
	created := false
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_conversations (
			id, client_id, customer_id, marketplace_customer_id, channel, created_at, updated_at
		) VALUES ($1,$2,$3,$4,'tellbook',NOW(),NOW())
		ON CONFLICT (client_id, marketplace_customer_id) DO NOTHING
		RETURNING id
	`, conversationID, clientID, customerID, marketplaceCustomerID).Scan(&conversationID); err == nil {
		created = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("create marketplace booking conversation: %w", err)
	}

	if !created {
		var existingCustomerID uuid.NullUUID
		if err := tx.QueryRow(ctx, `
			SELECT id, customer_id
			FROM inbox_conversations
			WHERE client_id = $1 AND marketplace_customer_id = $2
			FOR UPDATE
		`, clientID, marketplaceCustomerID).Scan(&conversationID, &existingCustomerID); err != nil {
			return GetOrCreateInboxConversationResult{}, fmt.Errorf("load marketplace booking conversation: %w", err)
		}
		if existingCustomerID.Valid && existingCustomerID.UUID != customerID {
			return GetOrCreateInboxConversationResult{}, fmt.Errorf("marketplace customer maps to multiple provider customer records")
		}
		if !existingCustomerID.Valid {
			if _, err := tx.Exec(ctx, `
				UPDATE inbox_conversations SET customer_id=$2, updated_at=NOW()
				WHERE id=$1 AND customer_id IS NULL
			`, conversationID, customerID); err != nil {
				return GetOrCreateInboxConversationResult{}, fmt.Errorf("link provider customer to conversation: %w", err)
			}
		}
	}

	commandTag, err := tx.Exec(ctx, `
		INSERT INTO inbox_conversation_bookings (conversation_id, booking_id, linked_by_actor)
		VALUES ($1,$2,'customer')
		ON CONFLICT (conversation_id, booking_id) DO NOTHING
	`, conversationID, bookingID)
	if err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("link marketplace booking conversation: %w", err)
	}
	bookingLinked := commandTag.RowsAffected() == 1

	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_participant_states (conversation_id, participant_type, participant_id)
		VALUES ($1,'provider',$2),($1,'marketplace_customer',$3)
		ON CONFLICT DO NOTHING
	`, conversationID, clientID, marketplaceCustomerID); err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("create inbox participant states: %w", err)
	}

	if created || bookingLinked {
		eventType := "conversation.updated"
		if created {
			eventType = "conversation.created"
		}
		var eventSequence int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_events (
				conversation_id, client_id, marketplace_customer_id, event_type, payload
			) VALUES ($1,$2,$3,$4,jsonb_build_object('conversation_id',$1::uuid::text,'booking_id',$5::uuid::text))
			RETURNING sequence
		`, conversationID, clientID, marketplaceCustomerID, eventType, bookingID).Scan(&eventSequence); err != nil {
			return GetOrCreateInboxConversationResult{}, fmt.Errorf("append inbox conversation event: %w", err)
		}
		if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
			return GetOrCreateInboxConversationResult{}, fmt.Errorf("notify inbox conversation event: %w", err)
		}
	}

	detail, err := loadMarketplaceConversationDetail(
		ctx, tx, marketplaceCustomerID, conversationID, initialInboxMessageLimit,
	)
	if err != nil {
		return GetOrCreateInboxConversationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GetOrCreateInboxConversationResult{}, fmt.Errorf("commit marketplace booking conversation: %w", err)
	}
	return GetOrCreateInboxConversationResult{Detail: detail, Created: created}, nil
}

func (r *Repository) GetMarketplaceConversationDetail(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	limit int,
) (InboxConversationDetailResponse, error) {
	if limit < 1 || limit > 50 {
		limit = initialInboxMessageLimit
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("begin marketplace conversation snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	detail, err := loadMarketplaceConversationDetail(ctx, tx, marketplaceCustomerID, conversationID, limit)
	if err != nil {
		return InboxConversationDetailResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("commit marketplace conversation snapshot: %w", err)
	}
	return detail, nil
}

func loadMarketplaceConversationDetail(
	ctx context.Context,
	queryer inboxQueryer,
	marketplaceCustomerID, conversationID uuid.UUID,
	limit int,
) (InboxConversationDetailResponse, error) {
	summary, err := loadMarketplaceConversationSummary(ctx, queryer, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxConversationDetailResponse{}, err
	}

	rows, err := queryer.Query(ctx, `
		SELECT id, conversation_id, sender_type, sender_id, client_message_id, booking_id,
			content, message_type, presentation, sent_at, sequence
		FROM inbox_messages
		WHERE conversation_id = $1
		ORDER BY sequence DESC
		LIMIT $2
	`, conversationID, limit+1)
	if err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("load marketplace conversation messages: %w", err)
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
		var senderID, clientMessageID, messageBookingID uuid.NullUUID
		var sequence int64
		if err := rows.Scan(
			&id, &conversation, &item.SenderType, &senderID, &clientMessageID,
			&messageBookingID, &item.Content, &item.MessageType, &item.Presentation, &item.SentAt, &sequence,
		); err != nil {
			return InboxConversationDetailResponse{}, fmt.Errorf("scan marketplace conversation message: %w", err)
		}
		item.ID = id.String()
		item.ConversationID = conversation.String()
		if senderID.Valid {
			item.SenderID = senderID.UUID.String()
		}
		if clientMessageID.Valid {
			item.ClientMessageID = clientMessageID.UUID.String()
		}
		if messageBookingID.Valid {
			item.BookingID = messageBookingID.UUID.String()
		}
		newestFirst = append(newestFirst, sequencedMessage{message: item, sequence: sequence})
	}
	if err := rows.Err(); err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("iterate marketplace conversation messages: %w", err)
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

	var syncSequence int64
	if err := queryer.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence),0) FROM inbox_events
		WHERE marketplace_customer_id=$1
	`, marketplaceCustomerID).Scan(&syncSequence); err != nil {
		return InboxConversationDetailResponse{}, fmt.Errorf("load inbox sync cursor: %w", err)
	}
	return InboxConversationDetailResponse{
		Conversation:     summary,
		Messages:         messages,
		NextBeforeCursor: nextBefore,
		SyncCursor:       encodeInboxSequenceCursor(syncSequence),
	}, nil
}

func loadMarketplaceConversationSummary(
	ctx context.Context,
	queryer inboxQueryer,
	marketplaceCustomerID, conversationID uuid.UUID,
) (InboxConversationSummary, error) {
	var summary InboxConversationSummary
	var conversation, providerID uuid.UUID
	var lastMessageSenderType *string
	err := queryer.QueryRow(ctx, `
		SELECT conversation.id, conversation.channel, conversation.client_id,
			COALESCE(NULLIF(BTRIM(profile.business_name),''), client.full_name),
			COALESCE(handle.handle_slug,''), COALESCE(profile.avatar_url,''),
			conversation.preview,
			(
				SELECT COUNT(*)::int
				FROM inbox_messages message
				WHERE message.conversation_id = conversation.id
				  AND message.sequence > COALESCE(state.last_read_sequence,0)
				  AND message.sender_type <> 'marketplace_customer'
			),
			EXISTS (
				SELECT 1 FROM inbox_ai_conversation_controls ai_control
				WHERE ai_control.conversation_id=conversation.id AND ai_control.state='handoff'
			),
			conversation.last_message_at,
			(SELECT sender_type FROM inbox_messages WHERE sequence=conversation.last_message_sequence),
			state.archived_at IS NOT NULL, conversation.disabled_at IS NOT NULL,
			conversation.created_at, conversation.updated_at
		FROM inbox_conversations conversation
		INNER JOIN clients client ON client.id=conversation.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=conversation.client_id
		LEFT JOIN client_profile_handles handle
			ON handle.client_id=conversation.client_id AND handle.handle_slug=profile.handle_slug
		LEFT JOIN inbox_participant_states state
			ON state.conversation_id=conversation.id
			AND state.participant_type='marketplace_customer'
			AND state.participant_id=$1
		WHERE conversation.id=$2 AND conversation.marketplace_customer_id=$1
	`, marketplaceCustomerID, conversationID).Scan(
		&conversation, &summary.Channel, &providerID, &summary.Counterparty.Name,
		&summary.Counterparty.Handle, &summary.Counterparty.AvatarURL, &summary.Preview,
		&summary.UnreadCount, &summary.ProviderHandoffRequested, &summary.LastMessageAt,
		&lastMessageSenderType, &summary.Archived,
		&summary.Disabled,
		&summary.CreatedAt, &summary.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboxConversationSummary{}, ErrNotFound
	}
	if err != nil {
		return InboxConversationSummary{}, fmt.Errorf("load marketplace conversation summary: %w", err)
	}
	summary.ID = conversation.String()
	summary.Counterparty.ID = providerID.String()
	summary.Counterparty.Kind = "provider"
	if lastMessageSenderType != nil {
		summary.LastMessageSenderType = *lastMessageSenderType
	}

	rows, err := queryer.Query(ctx, `
		SELECT booking.id, booking.title, booking.status, booking.start_at, booking.timezone
		FROM inbox_conversation_bookings link
		INNER JOIN bookings booking ON booking.id=link.booking_id
		WHERE link.conversation_id=$1 AND booking.marketplace_customer_id=$2
		ORDER BY booking.start_at DESC, booking.id DESC
		LIMIT 10
	`, conversationID, marketplaceCustomerID)
	if err != nil {
		return InboxConversationSummary{}, fmt.Errorf("load marketplace conversation bookings: %w", err)
	}
	defer rows.Close()
	summary.BookingContexts = make([]InboxBookingContext, 0)
	for rows.Next() {
		var booking InboxBookingContext
		var id uuid.UUID
		if err := rows.Scan(&id, &booking.ServiceTitle, &booking.Status, &booking.StartsAt, &booking.Timezone); err != nil {
			return InboxConversationSummary{}, fmt.Errorf("scan marketplace conversation booking: %w", err)
		}
		booking.ID = id.String()
		summary.BookingContexts = append(summary.BookingContexts, booking)
	}
	if err := rows.Err(); err != nil {
		return InboxConversationSummary{}, fmt.Errorf("iterate marketplace conversation bookings: %w", err)
	}
	return summary, nil
}

func encodeInboxSequenceCursor(sequence int64) string {
	return strconv.FormatInt(sequence, 36)
}

func (r *Repository) GetClientIDByHandleSlug(ctx context.Context, slug string) (uuid.UUID, error) {
	var clientID uuid.UUID
	if err := r.db.QueryRow(ctx, `
		SELECT client_id FROM client_profile_handles WHERE handle_slug = $1
	`, strings.TrimSpace(slug)).Scan(&clientID); errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	} else if err != nil {
		return uuid.Nil, fmt.Errorf("get client by handle slug: %w", err)
	}
	return clientID, nil
}
