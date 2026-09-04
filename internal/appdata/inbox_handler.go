package appdata

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/marketplaceauth"

	"github.com/google/uuid"
)

func (h *Handler) listMarketplaceConversations(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	limit, ok := marketplaceIntQuery(
		w, strings.TrimSpace(r.URL.Query().Get("limit")), "limit", initialInboxConversationLimit, 1, 50,
	)
	if !ok {
		return
	}
	query, ok := parseInboxSearchQuery(w, r)
	if !ok {
		return
	}
	result, err := h.repo.ListMarketplaceConversations(
		r.Context(), customer.ID, limit, query, strings.TrimSpace(r.URL.Query().Get("cursor")),
	)
	if errors.Is(err, ErrInboxInvalidCursor) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Conversation cursor is invalid.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load conversations.")
		return
	}
	for index := range result.Items {
		result.Items[index].Counterparty.AvatarURL = h.signedMediaURL(
			r.Context(), result.Items[index].Counterparty.AvatarURL,
		)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getOrCreateMarketplaceBookingConversation(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	bookingID, ok := marketplaceBookingIDParam(w, r)
	if !ok {
		return
	}
	if !h.enforceInboxCommandLimit(
		r.Context(), w, inboxCommandCreate, "marketplace_customer", customer.ID, bookingID,
	) {
		return
	}
	startedAt := time.Now()
	result, err := h.repo.GetOrCreateMarketplaceBookingConversation(r.Context(), customer.ID, bookingID)
	conversationID := uuid.Nil
	if err == nil {
		conversationID, _ = uuid.Parse(result.Detail.Conversation.ID)
	}
	logInboxCommand(
		r, "conversation.get_or_create", "marketplace_customer", customer.ID,
		conversationID, "", false, time.Since(startedAt), err,
	)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Booking was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not open this conversation.")
		return
	}
	result.Detail.Conversation.Counterparty.AvatarURL = h.signedMediaURL(
		r.Context(), result.Detail.Conversation.Counterparty.AvatarURL,
	)
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result.Detail)
}

func (h *Handler) getOrCreateMarketplaceProviderConversation(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	providerID, err := uuidFromURLParam("providerID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Provider ID is invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(
		r.Context(), w, inboxCommandCreate, "marketplace_customer", customer.ID, providerID,
	) {
		return
	}
	startedAt := time.Now()
	result, err := h.repo.GetOrCreateMarketplaceProviderConversation(r.Context(), customer.ID, providerID)
	conversationID := uuid.Nil
	if err == nil {
		conversationID, _ = uuid.Parse(result.Detail.Conversation.ID)
	}
	logInboxCommand(
		r, "provider_conversation.get_or_create", "marketplace_customer", customer.ID,
		conversationID, "", false, time.Since(startedAt), err,
	)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Provider was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not open this conversation.")
		return
	}
	result.Detail.Conversation.Counterparty.AvatarURL = h.signedMediaURL(
		r.Context(), result.Detail.Conversation.Counterparty.AvatarURL,
	)
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result.Detail)
}

func (h *Handler) getMarketplaceConversation(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation ID is invalid.")
		return
	}
	limit, ok := marketplaceIntQuery(
		w, strings.TrimSpace(r.URL.Query().Get("limit")), "limit", initialInboxMessageLimit, 1, 50,
	)
	if !ok {
		return
	}
	detail, err := h.repo.GetMarketplaceConversationDetail(r.Context(), customer.ID, conversationID, limit)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Conversation was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load this conversation.")
		return
	}
	detail.Conversation.Counterparty.AvatarURL = h.signedMediaURL(
		r.Context(), detail.Conversation.Counterparty.AvatarURL,
	)
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) sendMarketplaceMessage(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation ID is invalid.")
		return
	}
	input, err := decodeJSON[SendInboxMessageInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Message details are invalid.")
		return
	}
	clientMessageID, bookingID, aiRunID, ok := parseInboxSendIdentifiers(w, input)
	if !ok {
		return
	}
	if aiRunID.Valid {
		writeError(w, http.StatusBadRequest, "invalid_request", "AI draft lineage is only valid for provider replies.")
		return
	}
	if !h.enforceInboxCommandLimit(
		r.Context(), w, inboxCommandSend, "marketplace_customer", customer.ID, conversationID,
	) {
		return
	}
	startedAt := time.Now()
	result, err := h.repo.SendMarketplaceMessage(
		r.Context(), customer.ID, conversationID, clientMessageID, input.Content, bookingID,
	)
	h.inboxMetrics.ObserveSend(time.Since(startedAt), err, result.Replayed)
	logInboxCommand(
		r, "message.send", "marketplace_customer", customer.ID, conversationID,
		result.Message.ID, result.Replayed, time.Since(startedAt), err,
	)
	if !writeInboxSendError(w, err) {
		return
	}
	result.Conversation.Counterparty.AvatarURL = h.signedMediaURL(
		r.Context(), result.Conversation.Counterparty.AvatarURL,
	)
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (h *Handler) listProviderConversations(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	limit, ok := marketplaceIntQuery(
		w, strings.TrimSpace(r.URL.Query().Get("limit")), "limit", initialInboxConversationLimit, 1, 50,
	)
	if !ok {
		return
	}
	query, ok := parseInboxSearchQuery(w, r)
	if !ok {
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		state = "all"
	}
	if state != "all" && state != "unread" && state != "archived" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Inbox state is invalid.")
		return
	}
	result, err := h.repo.ListProviderConversations(
		r.Context(), client.ID, limit, query, state, strings.TrimSpace(r.URL.Query().Get("cursor")),
	)
	if errors.Is(err, ErrInboxInvalidCursor) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Conversation cursor is invalid.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load conversations.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parseInboxSearchQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if utf8.RuneCountInString(query) > 160 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Search query is too long.")
		return "", false
	}
	return query, true
}

func (h *Handler) getProviderConversation(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation ID is invalid.")
		return
	}
	limit, ok := marketplaceIntQuery(
		w, strings.TrimSpace(r.URL.Query().Get("limit")), "limit", initialInboxMessageLimit, 1, 50,
	)
	if !ok {
		return
	}
	detail, err := h.repo.GetProviderConversationDetail(r.Context(), client.ID, conversationID, limit)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Conversation was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load this conversation.")
		return
	}
	detail.AIDraftsAvailable = h.inboxAIDraftsAvailable(client.ID)
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) listOlderMarketplaceMessages(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	conversationID, ok := inboxConversationIDParam(w, r)
	if !ok {
		return
	}
	limit, ok := inboxPageLimit(w, r)
	if !ok {
		return
	}
	result, err := h.repo.ListMarketplaceConversationMessages(
		r.Context(), customer.ID, conversationID, strings.TrimSpace(r.URL.Query().Get("before")), limit,
	)
	if !writeInboxPageError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listOlderProviderMessages(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	conversationID, ok := inboxConversationIDParam(w, r)
	if !ok {
		return
	}
	limit, ok := inboxPageLimit(w, r)
	if !ok {
		return
	}
	result, err := h.repo.ListProviderConversationMessages(
		r.Context(), client.ID, conversationID, strings.TrimSpace(r.URL.Query().Get("before")), limit,
	)
	if !writeInboxPageError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) markMarketplaceConversationRead(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	conversationID, ok := inboxConversationIDParam(w, r)
	if !ok {
		return
	}
	messageID, ok := inboxReadMessageID(w, r)
	if !ok {
		return
	}
	if !h.enforceInboxCommandLimit(
		r.Context(), w, inboxCommandRead, "marketplace_customer", customer.ID, conversationID,
	) {
		return
	}
	startedAt := time.Now()
	result, err := h.repo.MarkMarketplaceConversationRead(
		r.Context(), customer.ID, conversationID, messageID,
	)
	logInboxCommand(
		r, "conversation.read", "marketplace_customer", customer.ID, conversationID,
		messageID.String(), false, time.Since(startedAt), err,
	)
	if !writeInboxPageError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) markProviderConversationRead(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	conversationID, ok := inboxConversationIDParam(w, r)
	if !ok {
		return
	}
	messageID, ok := inboxReadMessageID(w, r)
	if !ok {
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandRead, "provider", client.ID, conversationID) {
		return
	}
	startedAt := time.Now()
	result, err := h.repo.MarkProviderConversationRead(r.Context(), client.ID, conversationID, messageID)
	logInboxCommand(
		r, "conversation.read", "provider", client.ID, conversationID,
		messageID.String(), false, time.Since(startedAt), err,
	)
	if !writeInboxPageError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) archiveProviderConversation(w http.ResponseWriter, r *http.Request) {
	h.setProviderConversationArchived(w, r, true)
}

func (h *Handler) unarchiveProviderConversation(w http.ResponseWriter, r *http.Request) {
	h.setProviderConversationArchived(w, r, false)
}

func (h *Handler) setProviderConversationArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	conversationID, ok := inboxConversationIDParam(w, r)
	if !ok {
		return
	}
	if !h.enforceInboxCommandLimit(
		r.Context(), w, inboxCommandArchive, "provider", client.ID, conversationID,
	) {
		return
	}
	startedAt := time.Now()
	result, err := h.repo.SetProviderConversationArchived(r.Context(), client.ID, conversationID, archived)
	logInboxCommand(
		r, "conversation.archive", "provider", client.ID, conversationID,
		"", false, time.Since(startedAt), err,
	)
	if !writeInboxPageError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getMarketplaceInboxUnreadCount(w http.ResponseWriter, r *http.Request) {
	customer, ok := marketplaceauth.CustomerFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	startedAt := time.Now()
	result, err := h.repo.GetMarketplaceInboxUnreadCount(r.Context(), customer.ID)
	h.inboxMetrics.ObserveUnread(time.Since(startedAt), err)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load unread messages.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getProviderInboxUnreadCount(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	startedAt := time.Now()
	result, err := h.repo.GetProviderInboxUnreadCount(r.Context(), client.ID)
	h.inboxMetrics.ObserveUnread(time.Since(startedAt), err)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load unread messages.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func inboxConversationIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation ID is invalid.")
		return uuid.Nil, false
	}
	return conversationID, true
}

func inboxPageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	return marketplaceIntQuery(
		w, strings.TrimSpace(r.URL.Query().Get("limit")), "limit", initialInboxMessageLimit, 1, 50,
	)
}

func inboxReadMessageID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	input, err := decodeJSON[MarkInboxReadInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Read details are invalid.")
		return uuid.Nil, false
	}
	messageID, err := uuid.Parse(strings.TrimSpace(input.MessageID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Message ID is invalid.")
		return uuid.Nil, false
	}
	return messageID, true
}

func writeInboxPageError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrInboxInvalidCursor):
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Message cursor is invalid.")
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Conversation or message was not found.")
	default:
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not update this conversation.")
	}
	return false
}

func (h *Handler) sendProviderMessage(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation ID is invalid.")
		return
	}
	input, err := decodeJSON[SendInboxMessageInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Message details are invalid.")
		return
	}
	clientMessageID, bookingID, aiRunID, ok := parseInboxSendIdentifiers(w, input)
	if !ok {
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandSend, "provider", client.ID, conversationID) {
		return
	}
	startedAt := time.Now()
	result, err := h.repo.SendProviderMessage(
		r.Context(), client.ID, conversationID, clientMessageID, input.Content, bookingID, aiRunID,
	)
	h.inboxMetrics.ObserveSend(time.Since(startedAt), err, result.Replayed)
	logInboxCommand(
		r, "message.send", "provider", client.ID, conversationID,
		result.Message.ID, result.Replayed, time.Since(startedAt), err,
	)
	if !writeInboxSendError(w, err) {
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func parseInboxSendIdentifiers(
	w http.ResponseWriter,
	input SendInboxMessageInput,
) (uuid.UUID, uuid.NullUUID, uuid.NullUUID, bool) {
	clientMessageID, err := uuid.Parse(strings.TrimSpace(input.ClientMessageID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Client message ID is invalid.")
		return uuid.Nil, uuid.NullUUID{}, uuid.NullUUID{}, false
	}
	bookingID := uuid.NullUUID{}
	if rawBookingID := strings.TrimSpace(input.BookingID); rawBookingID != "" {
		parsedBookingID, err := uuid.Parse(rawBookingID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Booking ID is invalid.")
			return uuid.Nil, uuid.NullUUID{}, uuid.NullUUID{}, false
		}
		bookingID = uuid.NullUUID{UUID: parsedBookingID, Valid: true}
	}
	aiRunID := uuid.NullUUID{}
	if rawRunID := strings.TrimSpace(input.AIRunID); rawRunID != "" {
		parsedRunID, err := uuid.Parse(rawRunID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "AI draft run ID is invalid.")
			return uuid.Nil, uuid.NullUUID{}, uuid.NullUUID{}, false
		}
		aiRunID = uuid.NullUUID{UUID: parsedRunID, Valid: true}
	}
	return clientMessageID, bookingID, aiRunID, true
}

func writeInboxSendError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Conversation was not found.")
	case errors.Is(err, ErrInboxInvalidContent):
		writeError(w, http.StatusBadRequest, "invalid_content", "Enter a message of up to 4,000 characters.")
	case errors.Is(err, ErrInboxBookingContext):
		writeError(w, http.StatusBadRequest, "invalid_booking_context", "That booking is not linked to this conversation.")
	case errors.Is(err, ErrInboxIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "That message retry key was already used for different content.")
	case errors.Is(err, ErrInboxConversationDisabled):
		writeError(w, http.StatusConflict, "conversation_disabled", "Messaging is unavailable for this conversation.")
	case errors.Is(err, ErrInboxAIDraftStale):
		writeError(w, http.StatusConflict, "ai_draft_stale", "A newer message arrived. Generate a fresh AI draft before sending.")
	case errors.Is(err, ErrInboxAIDraftInvalid):
		writeError(w, http.StatusConflict, "ai_draft_closed", "This AI draft has already been used or discarded.")
	default:
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not send this message.")
	}
	return false
}
