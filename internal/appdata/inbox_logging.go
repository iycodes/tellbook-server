package appdata

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

func logInboxCommand(
	r *http.Request,
	command string,
	actorType string,
	actorID uuid.UUID,
	conversationID uuid.UUID,
	messageID string,
	replayed bool,
	duration time.Duration,
	err error,
) {
	attributes := []any{
		"command", command,
		"actor_type", actorType,
		"actor_id", actorID.String(),
		"conversation_id", conversationID.String(),
		"duration", duration,
		"request_id", chimiddleware.GetReqID(r.Context()),
	}
	if messageID != "" {
		attributes = append(attributes, "message_id", messageID)
	}
	if replayed {
		attributes = append(attributes, "idempotent_replay", true)
	}
	if err != nil {
		attributes = append(attributes, "error_code", inboxCommandErrorCode(err), "error", err)
		slog.Warn("inbox command failed", attributes...)
		return
	}
	if replayed {
		slog.Info("inbox command replayed", attributes...)
		return
	}
	slog.Debug("inbox command completed", attributes...)
}

func inboxCommandErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrInboxInvalidContent):
		return "invalid_content"
	case errors.Is(err, ErrInboxBookingContext):
		return "invalid_booking_context"
	case errors.Is(err, ErrInboxIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, ErrInboxConversationDisabled):
		return "conversation_disabled"
	case errors.Is(err, ErrInboxInvalidCursor):
		return "invalid_cursor"
	default:
		return "server_unavailable"
	}
}
