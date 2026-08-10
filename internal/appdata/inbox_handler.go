package appdata

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"booking/go-server/internal/auth"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/gorilla/websocket"
)

var inboxWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (h *Handler) listInboxConversations(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListInboxConversations(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "inbox_conversations_failed", "Could not load inbox conversations.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) streamInboxListWebSocket(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	conn, err := inboxWebsocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetReadLimit(1 << 20)
	_ = conn.WriteJSON(InboxConversationListStreamEvent{Type: "connected"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var lastSnapshot string

	for {
		items, err := h.repo.ListInboxConversations(r.Context(), authedClient.ID)
		if err != nil {
			_ = conn.WriteJSON(InboxConversationListStreamEvent{
				Type: "error",
				Error: &APIError{
					Code:    "inbox_conversations_failed",
					Message: "Could not load inbox conversations.",
				},
			})
			return
		}

		snapshotBytes, err := json.Marshal(items)
		if err != nil {
			return
		}
		snapshot := string(snapshotBytes)
		if snapshot != lastSnapshot {
			lastSnapshot = snapshot
			if err := conn.WriteJSON(InboxConversationListStreamEvent{
				Type:  "snapshot",
				Items: items,
			}); err != nil {
				return
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) getInboxConversationDetails(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_conversation_id", "Conversation ID is invalid.")
		return
	}

	details, err := h.repo.GetInboxConversationDetails(r.Context(), authedClient.ID, conversationID)
	if err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "conversation_details_failed", "Could not load conversation details.")
		return
	}

	writeJSON(w, http.StatusOK, details)
}

func (h *Handler) streamInboxConversationWebSocket(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_conversation_id", "Conversation ID is invalid.")
		return
	}

	if _, err := h.repo.GetInboxConversationDetails(r.Context(), authedClient.ID, conversationID); err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "conversation_details_failed", "Could not load conversation details.")
		return
	}

	conn, err := inboxWebsocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetReadLimit(1 << 20)
	_ = conn.WriteJSON(InboxConversationStreamEvent{Type: "connected"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastSnapshot string

	for {
		details, err := h.repo.GetInboxConversationDetails(r.Context(), authedClient.ID, conversationID)
		if err != nil {
			if err == ErrNotFound {
				_ = conn.WriteJSON(InboxConversationStreamEvent{
					Type: "error",
					Error: &APIError{
						Code:    "conversation_not_found",
						Message: "Conversation was not found.",
					},
				})
				return
			}
			_ = conn.WriteJSON(InboxConversationStreamEvent{
				Type: "error",
				Error: &APIError{
					Code:    "conversation_stream_failed",
					Message: "Could not refresh conversation details.",
				},
			})
			return
		}

		snapshotBytes, err := json.Marshal(details)
		if err != nil {
			return
		}
		snapshot := string(snapshotBytes)
		if snapshot != lastSnapshot {
			lastSnapshot = snapshot
			if err := conn.WriteJSON(InboxConversationStreamEvent{
				Type:    "snapshot",
				Details: &details,
			}); err != nil {
				return
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) updateInboxConversationControls(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_conversation_id", "Conversation ID is invalid.")
		return
	}

	input, err := decodeJSON[UpdateInboxConversationControlsInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if strings.TrimSpace(input.AutopilotMode) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Autopilot mode is required.")
		return
	}

	item, err := h.repo.UpdateInboxConversationControls(
		r.Context(),
		authedClient.ID,
		conversationID,
		input.AutopilotMode,
		input.HumanTakeover,
	)
	if err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "conversation_controls_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) updateInboxConversationComposeState(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_conversation_id", "Conversation ID is invalid.")
		return
	}

	input, err := decodeJSON[UpdateInboxComposeStateInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	item, err := h.repo.UpdateInboxConversationComposeState(r.Context(), authedClient.ID, conversationID, input.IsComposing)
	if err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "conversation_compose_state_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) sendInboxConversationMessage(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_conversation_id", "Conversation ID is invalid.")
		return
	}

	input, err := decodeJSON[SendInboxMessageInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	message, conversation, err := h.repo.SendInboxConversationMessage(
		r.Context(),
		authedClient.ID,
		conversationID,
		input.Content,
	)
	if err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "conversation_message_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"conversation": conversation,
		"message":      message,
	})
}

func (h *Handler) suggestInboxConversationReply(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if h.ai == nil || !h.ai.Available() {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI generation is not available right now.")
		return
	}

	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_conversation_id", "Conversation ID is invalid.")
		return
	}

	agentContext, err := h.repo.BuildInboxAgentContext(r.Context(), authedClient.ID, conversationID)
	if err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "conversation_not_found", "Conversation was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "conversation_context_failed", "Could not build reply context.")
		return
	}

	latestCustomerMessage := latestInboxCustomerMessage(agentContext.RecentTurns)
	if strings.TrimSpace(latestCustomerMessage) == "" {
		writeError(w, http.StatusBadRequest, "reply_context_missing", "There is no customer message to reply to yet.")
		return
	}

	response, err := h.ai.SuggestReply(r.Context(), aiapi.SuggestReplyRequest{
		ThreadID:              agentContext.Conversation.ID,
		BusinessName:          agentContext.BusinessName,
		CustomerName:          firstNonEmptyInboxValue(agentContext.Conversation.CustomerName, agentContext.Conversation.LeadName),
		LatestCustomerMessage: latestCustomerMessage,
		Goal:                  "Draft a concise, helpful reply the provider can review and send.",
		Conversation:          agentContext.RecentTurns,
		Context:               agentContext.ContextItems,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "reply_suggestion_failed", "Could not generate a reply suggestion.")
		return
	}

	writeJSON(w, http.StatusOK, SuggestInboxReplyResponse{
		Reply:            response.Reply,
		SafeToSend:       response.SafeToSend,
		NeedsHumanReview: response.NeedsHumanReview,
		Warnings:         response.Warnings,
	})
}

func firstNonEmptyInboxValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func latestInboxCustomerMessage(turns []aiapi.MessageTurn) string {
	for index := len(turns) - 1; index >= 0; index-- {
		if strings.TrimSpace(turns[index].Role) == "customer" && strings.TrimSpace(turns[index].Content) != "" {
			return strings.TrimSpace(turns[index].Content)
		}
	}
	return ""
}
