package appdata

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type inboxConversationCursor struct {
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ID            uuid.UUID  `json:"id"`
}

func encodeInboxConversationCursor(item InboxConversationSummary) string {
	payload, _ := json.Marshal(inboxConversationCursor{
		LastMessageAt: item.LastMessageAt,
		CreatedAt:     item.CreatedAt,
		ID:            uuid.MustParse(item.ID),
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeInboxConversationCursor(raw string) (*inboxConversationCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > 512 {
		return nil, ErrInboxInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInboxInvalidCursor
	}
	var cursor inboxConversationCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.ID == uuid.Nil || cursor.CreatedAt.IsZero() {
		return nil, ErrInboxInvalidCursor
	}
	return &cursor, nil
}

func appendInboxListCursorClause(args *[]any, cursor *inboxConversationCursor) string {
	if cursor == nil {
		return ""
	}
	if cursor.LastMessageAt == nil {
		createdPlaceholder := len(*args) + 1
		idPlaceholder := createdPlaceholder + 1
		*args = append(*args, cursor.CreatedAt, cursor.ID)
		return fmt.Sprintf(`AND conversation.last_message_at IS NULL AND (
			conversation.created_at < $%d
			OR (conversation.created_at = $%d AND conversation.id < $%d)
		)`, createdPlaceholder, createdPlaceholder, idPlaceholder)
	}
	messagePlaceholder := len(*args) + 1
	createdPlaceholder := messagePlaceholder + 1
	idPlaceholder := messagePlaceholder + 2
	*args = append(*args, *cursor.LastMessageAt, cursor.CreatedAt, cursor.ID)
	return fmt.Sprintf(`AND (
		conversation.last_message_at < $%d
		OR conversation.last_message_at IS NULL
		OR (conversation.last_message_at = $%d AND (
			conversation.created_at < $%d
			OR (conversation.created_at = $%d AND conversation.id < $%d)
		))
	)`, messagePlaceholder, messagePlaceholder, createdPlaceholder, createdPlaceholder, idPlaceholder)
}

func decodeInboxSequenceCursor(raw string) (int64, error) {
	if len(raw) > 32 {
		return 0, ErrInboxInvalidCursor
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 36, 64)
	if err != nil || value < 1 {
		return 0, ErrInboxInvalidCursor
	}
	return value, nil
}

func (r *Repository) ListMarketplaceConversationMessages(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	before string,
	limit int,
) (InboxMessagePageResponse, error) {
	return r.listInboxConversationMessages(
		ctx, "marketplace_customer", marketplaceCustomerID, conversationID, before, limit,
	)
}

func (r *Repository) ListProviderConversationMessages(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	before string,
	limit int,
) (InboxMessagePageResponse, error) {
	return r.listInboxConversationMessages(ctx, "provider", clientID, conversationID, before, limit)
}

func (r *Repository) listInboxConversationMessages(
	ctx context.Context,
	actorType string,
	actorID, conversationID uuid.UUID,
	before string,
	limit int,
) (InboxMessagePageResponse, error) {
	if limit < 1 || limit > 50 {
		limit = initialInboxMessageLimit
	}
	beforeSequence, err := decodeInboxSequenceCursor(before)
	if err != nil {
		return InboxMessagePageResponse{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return InboxMessagePageResponse{}, fmt.Errorf("begin inbox message page snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var authorized bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM inbox_conversations
			WHERE id=$1 AND (($2='provider' AND client_id=$3)
				OR ($2='marketplace_customer' AND marketplace_customer_id=$3))
		)
	`, conversationID, actorType, actorID).Scan(&authorized); err != nil {
		return InboxMessagePageResponse{}, fmt.Errorf("authorize inbox message page: %w", err)
	}
	if !authorized {
		return InboxMessagePageResponse{}, ErrNotFound
	}
	items, nextBefore, err := loadInboxConversationMessages(
		ctx, tx, conversationID, limit, &beforeSequence,
	)
	if err != nil {
		return InboxMessagePageResponse{}, err
	}
	syncSequence, err := loadInboxActorSyncSequence(ctx, tx, actorType, actorID)
	if err != nil {
		return InboxMessagePageResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxMessagePageResponse{}, fmt.Errorf("commit inbox message page snapshot: %w", err)
	}
	return InboxMessagePageResponse{
		Items: items, NextBeforeCursor: nextBefore, SyncCursor: encodeInboxSequenceCursor(syncSequence),
	}, nil
}

func (r *Repository) MarkMarketplaceConversationRead(
	ctx context.Context,
	marketplaceCustomerID, conversationID, messageID uuid.UUID,
) (InboxParticipantState, error) {
	return r.markInboxConversationRead(
		ctx, "marketplace_customer", marketplaceCustomerID, conversationID, messageID,
	)
}

func (r *Repository) MarkProviderConversationRead(
	ctx context.Context,
	clientID, conversationID, messageID uuid.UUID,
) (InboxParticipantState, error) {
	return r.markInboxConversationRead(ctx, "provider", clientID, conversationID, messageID)
}

func (r *Repository) markInboxConversationRead(
	ctx context.Context,
	actorType string,
	actorID, conversationID, messageID uuid.UUID,
) (InboxParticipantState, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return InboxParticipantState{}, fmt.Errorf("begin inbox read command: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var clientID, marketplaceCustomerID uuid.UUID
	var messageSequence int64
	if err := tx.QueryRow(ctx, `
		SELECT conversation.client_id, conversation.marketplace_customer_id, message.sequence
		FROM inbox_conversations conversation
		INNER JOIN inbox_messages message
			ON message.conversation_id=conversation.id AND message.id=$4
		WHERE conversation.id=$1
		  AND (($2='provider' AND conversation.client_id=$3)
			OR ($2='marketplace_customer' AND conversation.marketplace_customer_id=$3))
		FOR UPDATE OF conversation
	`, conversationID, actorType, actorID, messageID).Scan(
		&clientID, &marketplaceCustomerID, &messageSequence,
	); errors.Is(err, pgx.ErrNoRows) {
		return InboxParticipantState{}, ErrNotFound
	} else if err != nil {
		return InboxParticipantState{}, fmt.Errorf("authorize inbox read command: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO inbox_participant_states (
			conversation_id, participant_type, participant_id, last_read_sequence, last_read_at
		) VALUES ($1,$2,$3,$4,NOW())
		ON CONFLICT (conversation_id, participant_type, participant_id) DO UPDATE SET
			last_read_sequence=EXCLUDED.last_read_sequence,
			last_read_at=EXCLUDED.last_read_at
		WHERE inbox_participant_states.last_read_sequence < EXCLUDED.last_read_sequence
	`, conversationID, actorType, actorID, messageSequence)
	if err != nil {
		return InboxParticipantState{}, fmt.Errorf("advance inbox read cursor: %w", err)
	}
	if tag.RowsAffected() > 0 {
		var eventSequence int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_events (
				conversation_id, client_id, marketplace_customer_id, event_type, payload
			) VALUES (
				$1,$2,$3,'read.updated',
				jsonb_build_object('conversation_id',$1::uuid::text,'participant_type',$4::text,'message_id',$5::uuid::text)
			) RETURNING sequence
		`, conversationID, clientID, marketplaceCustomerID, actorType, messageID).Scan(&eventSequence); err != nil {
			return InboxParticipantState{}, fmt.Errorf("append inbox read event: %w", err)
		}
		if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
			return InboxParticipantState{}, fmt.Errorf("notify inbox read event: %w", err)
		}
	}
	state, err := loadInboxParticipantState(ctx, tx, actorType, actorID, conversationID)
	if err != nil {
		return InboxParticipantState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxParticipantState{}, fmt.Errorf("commit inbox read command: %w", err)
	}
	return state, nil
}

func (r *Repository) SetProviderConversationArchived(
	ctx context.Context,
	clientID, conversationID uuid.UUID,
	archived bool,
) (InboxParticipantState, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return InboxParticipantState{}, fmt.Errorf("begin inbox archive command: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var marketplaceCustomerID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT marketplace_customer_id FROM inbox_conversations
		WHERE id=$1 AND client_id=$2 FOR UPDATE
	`, conversationID, clientID).Scan(&marketplaceCustomerID); errors.Is(err, pgx.ErrNoRows) {
		return InboxParticipantState{}, ErrNotFound
	} else if err != nil {
		return InboxParticipantState{}, fmt.Errorf("authorize inbox archive command: %w", err)
	}

	var tag pgconn.CommandTag
	if archived {
		tag, err = tx.Exec(ctx, `
			INSERT INTO inbox_participant_states (
				conversation_id, participant_type, participant_id, archived_at
			) VALUES ($1,'provider',$2,NOW())
			ON CONFLICT (conversation_id, participant_type, participant_id) DO UPDATE SET archived_at=NOW()
			WHERE inbox_participant_states.archived_at IS NULL
		`, conversationID, clientID)
	} else {
		tag, err = tx.Exec(ctx, `
			UPDATE inbox_participant_states SET archived_at=NULL
			WHERE conversation_id=$1 AND participant_type='provider' AND participant_id=$2
			  AND archived_at IS NOT NULL
		`, conversationID, clientID)
	}
	if err != nil {
		return InboxParticipantState{}, fmt.Errorf("update inbox archive state: %w", err)
	}
	if tag.RowsAffected() > 0 {
		var eventSequence int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_events (
				conversation_id, client_id, marketplace_customer_id, event_type, payload
			) VALUES (
				$1,$2,$3,'participant.archive_updated',
				jsonb_build_object('conversation_id',$1::uuid::text,'participant_type','provider','archived',$4::boolean)
			) RETURNING sequence
		`, conversationID, clientID, marketplaceCustomerID, archived).Scan(&eventSequence); err != nil {
			return InboxParticipantState{}, fmt.Errorf("append inbox archive event: %w", err)
		}
		if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
			return InboxParticipantState{}, fmt.Errorf("notify inbox archive event: %w", err)
		}
	}
	state, err := loadInboxParticipantState(ctx, tx, "provider", clientID, conversationID)
	if err != nil {
		return InboxParticipantState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxParticipantState{}, fmt.Errorf("commit inbox archive command: %w", err)
	}
	return state, nil
}

func (r *Repository) GetMarketplaceInboxUnreadCount(
	ctx context.Context,
	marketplaceCustomerID uuid.UUID,
) (InboxUnreadCountResponse, error) {
	return r.getInboxUnreadCount(ctx, "marketplace_customer", marketplaceCustomerID)
}

func (r *Repository) GetProviderInboxUnreadCount(
	ctx context.Context,
	clientID uuid.UUID,
) (InboxUnreadCountResponse, error) {
	return r.getInboxUnreadCount(ctx, "provider", clientID)
}

func (r *Repository) getInboxUnreadCount(
	ctx context.Context,
	actorType string,
	actorID uuid.UUID,
) (InboxUnreadCountResponse, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return InboxUnreadCountResponse{}, fmt.Errorf("begin inbox unread snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	unread, syncSequence, err := loadInboxActorCounts(ctx, tx, actorType, actorID)
	if err != nil {
		return InboxUnreadCountResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxUnreadCountResponse{}, fmt.Errorf("commit inbox unread snapshot: %w", err)
	}
	return InboxUnreadCountResponse{
		UnreadTotal: unread,
		SyncCursor:  encodeInboxSequenceCursor(syncSequence),
	}, nil
}

func loadInboxParticipantState(
	ctx context.Context,
	queryer inboxQueryer,
	actorType string,
	actorID, conversationID uuid.UUID,
) (InboxParticipantState, error) {
	var lastReadSequence int64
	var state InboxParticipantState
	if err := queryer.QueryRow(ctx, `
		SELECT state.last_read_sequence, state.last_read_at, state.archived_at IS NOT NULL,
			(
				SELECT COUNT(*)::int FROM inbox_messages message
				WHERE message.conversation_id=$1
				  AND message.sequence > state.last_read_sequence
				  AND message.sender_type <> $2
			)
		FROM inbox_participant_states state
		WHERE state.conversation_id=$1 AND state.participant_type=$2 AND state.participant_id=$3
	`, conversationID, actorType, actorID).Scan(
		&lastReadSequence, &state.LastReadAt, &state.Archived, &state.ConversationUnreadCount,
	); err != nil {
		return InboxParticipantState{}, fmt.Errorf("load inbox participant state: %w", err)
	}
	state.ConversationID = conversationID.String()
	state.ParticipantType = actorType
	state.ParticipantID = actorID.String()
	state.LastReadCursor = encodeInboxSequenceCursor(lastReadSequence)
	var syncSequence int64
	unreadTotal, syncSequence, err := loadInboxActorCounts(ctx, queryer, actorType, actorID)
	if err != nil {
		return InboxParticipantState{}, err
	}
	state.UnreadTotal = unreadTotal
	state.SyncCursor = encodeInboxSequenceCursor(syncSequence)
	return state, nil
}

func loadInboxActorCounts(
	ctx context.Context,
	queryer inboxQueryer,
	actorType string,
	actorID uuid.UUID,
) (int, int64, error) {
	var unread int
	if actorType == "provider" {
		if err := queryer.QueryRow(ctx, `
			SELECT COUNT(*)::int
			FROM inbox_messages message
			INNER JOIN inbox_conversations conversation ON conversation.id=message.conversation_id
			LEFT JOIN inbox_participant_states state
				ON state.conversation_id=conversation.id
				AND state.participant_type='provider' AND state.participant_id=$1
			WHERE conversation.client_id=$1
			  AND message.sequence > COALESCE(state.last_read_sequence,0)
			  AND message.sender_type NOT IN ('provider','ai')
		`, actorID).Scan(&unread); err != nil {
			return 0, 0, fmt.Errorf("load provider inbox unread count: %w", err)
		}
	} else {
		if err := queryer.QueryRow(ctx, `
			SELECT COUNT(*)::int
			FROM inbox_messages message
			INNER JOIN inbox_conversations conversation ON conversation.id=message.conversation_id
			LEFT JOIN inbox_participant_states state
				ON state.conversation_id=conversation.id
				AND state.participant_type='marketplace_customer' AND state.participant_id=$1
			WHERE conversation.marketplace_customer_id=$1
			  AND message.sequence > COALESCE(state.last_read_sequence,0)
			  AND message.sender_type <> 'marketplace_customer'
		`, actorID).Scan(&unread); err != nil {
			return 0, 0, fmt.Errorf("load marketplace inbox unread count: %w", err)
		}
	}
	syncSequence, err := loadInboxActorSyncSequence(ctx, queryer, actorType, actorID)
	if err != nil {
		return 0, 0, err
	}
	return unread, syncSequence, nil
}

func loadInboxActorSyncSequence(
	ctx context.Context,
	queryer inboxQueryer,
	actorType string,
	actorID uuid.UUID,
) (int64, error) {
	var syncSequence int64
	query := `SELECT COALESCE(MAX(sequence),0) FROM inbox_events WHERE marketplace_customer_id=$1`
	if actorType == "provider" {
		query = `SELECT COALESCE(MAX(sequence),0) FROM inbox_events WHERE client_id=$1`
	}
	if err := queryer.QueryRow(ctx, query, actorID).Scan(&syncSequence); err != nil {
		return 0, fmt.Errorf("load %s inbox sync cursor: %w", actorType, err)
	}
	return syncSequence, nil
}
