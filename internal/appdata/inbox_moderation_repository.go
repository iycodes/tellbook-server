package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInboxInvalidModeration = errors.New("inbox moderation details are invalid")

type InboxModerationResult struct {
	ConversationID string
	AuditID        string
	Disabled       bool
	Changed        bool
	CreatedAt      time.Time
}

func (r *Repository) SetInboxConversationDisabled(
	ctx context.Context,
	conversationID uuid.UUID,
	disabled bool,
	reason string,
	operator string,
) (InboxModerationResult, error) {
	reason = strings.TrimSpace(reason)
	operator = strings.TrimSpace(operator)
	if conversationID == uuid.Nil || reason == "" || operator == "" ||
		utf8.RuneCountInString(reason) > 500 || utf8.RuneCountInString(operator) > 160 {
		return InboxModerationResult{}, ErrInboxInvalidModeration
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return InboxModerationResult{}, fmt.Errorf("begin inbox moderation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var clientID, marketplaceCustomerID uuid.UUID
	var currentlyDisabled bool
	if err := tx.QueryRow(ctx, `
		SELECT client_id, marketplace_customer_id, disabled_at IS NOT NULL
		FROM inbox_conversations
		WHERE id=$1
		FOR UPDATE
	`, conversationID).Scan(&clientID, &marketplaceCustomerID, &currentlyDisabled); errors.Is(err, pgx.ErrNoRows) {
		return InboxModerationResult{}, ErrNotFound
	} else if err != nil {
		return InboxModerationResult{}, fmt.Errorf("lock inbox conversation for moderation: %w", err)
	}
	if currentlyDisabled == disabled {
		if err := tx.Commit(ctx); err != nil {
			return InboxModerationResult{}, fmt.Errorf("commit unchanged inbox moderation: %w", err)
		}
		return InboxModerationResult{
			ConversationID: conversationID.String(), Disabled: disabled, Changed: false,
		}, nil
	}

	if disabled {
		_, err = tx.Exec(ctx, `
			UPDATE inbox_conversations
			SET disabled_at=NOW(), disabled_reason=$2, disabled_by=$3, updated_at=NOW()
			WHERE id=$1
		`, conversationID, reason, operator)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE inbox_conversations
			SET disabled_at=NULL, disabled_reason='', disabled_by='', updated_at=NOW()
			WHERE id=$1
		`, conversationID)
	}
	if err != nil {
		return InboxModerationResult{}, fmt.Errorf("update inbox moderation state: %w", err)
	}

	auditID := uuid.New()
	action := "enabled"
	if disabled {
		action = "disabled"
	}
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_conversation_moderation_events (
			id, conversation_id, action, reason, operator
		) VALUES ($1,$2,$3,$4,$5)
		RETURNING created_at
	`, auditID, conversationID, action, reason, operator).Scan(&createdAt); err != nil {
		return InboxModerationResult{}, fmt.Errorf("append inbox moderation audit: %w", err)
	}

	eventType := "conversation.updated"
	if disabled {
		eventType = "conversation.disabled"
	}
	var eventSequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO inbox_events (
			conversation_id, client_id, marketplace_customer_id, event_type, payload
		) VALUES (
			$1,$2,$3,$4,
			jsonb_build_object(
				'conversation_id',$1::uuid::text,
				'moderation_event_id',$5::uuid::text,
				'disabled',$6::boolean
			)
		) RETURNING sequence
	`, conversationID, clientID, marketplaceCustomerID, eventType, auditID, disabled).Scan(&eventSequence); err != nil {
		return InboxModerationResult{}, fmt.Errorf("append inbox moderation event: %w", err)
	}
	if err := notifyInboxEvent(ctx, tx, eventSequence, clientID, marketplaceCustomerID); err != nil {
		return InboxModerationResult{}, fmt.Errorf("notify inbox moderation event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxModerationResult{}, fmt.Errorf("commit inbox moderation: %w", err)
	}
	return InboxModerationResult{
		ConversationID: conversationID.String(), AuditID: auditID.String(), Disabled: disabled,
		Changed: true, CreatedAt: createdAt,
	}, nil
}
