package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/marketplaceauth"
)

func (h *Handler) getProviderInboxAIPolicy(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	policy, err := h.repo.GetInboxAIPolicy(r.Context(), client.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load inbox AI settings.")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (h *Handler) updateProviderInboxAIPolicy(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	input, err := decodeJSON[UpdateInboxAIPolicyInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Inbox AI settings are invalid.")
		return
	}
	policy, err := h.repo.UpdateInboxAIPolicy(r.Context(), client.ID, input)
	if !writeInboxAIStateError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (h *Handler) getProviderInboxAIControl(w http.ResponseWriter, r *http.Request) {
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
	control, err := h.repo.GetInboxAIConversationControl(r.Context(), client.ID, conversationID)
	if !writeInboxAIStateError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, control)
}

func (h *Handler) updateProviderInboxAIControl(w http.ResponseWriter, r *http.Request) {
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
	input, err := decodeJSON[UpdateInboxAIConversationControlInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation AI control is invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandAI, "provider", client.ID, conversationID) {
		return
	}
	control, err := h.repo.UpdateInboxAIConversationControl(
		r.Context(), client.ID, conversationID, input,
	)
	if !writeInboxAIStateError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, control)
}

func (h *Handler) getProviderInboxAISession(w http.ResponseWriter, r *http.Request) {
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
	session, err := h.repo.GetInboxAISession(r.Context(), client.ID, conversationID)
	if !writeInboxAIStateError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *Handler) customerInboxAIHandoff(w http.ResponseWriter, r *http.Request) {
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
	input, err := decodeJSON[CustomerInboxAIHandoffInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Handoff details are invalid.")
		return
	}
	if !h.enforceInboxCommandLimit(
		r.Context(), w, inboxCommandAI, "marketplace_customer", customer.ID, conversationID,
	) {
		return
	}
	result, err := h.repo.CustomerInboxAIHandoff(
		r.Context(), customer.ID, conversationID, input.Reason,
	)
	if !writeInboxAIStateError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeInboxAIStateError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Conversation or service was not found.")
	case errors.Is(err, ErrInboxAIRevisionConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "These AI settings changed. Refresh and try again.")
	case errors.Is(err, ErrInboxAIAutomationUnavailable):
		writeError(w, http.StatusForbidden, "automation_unavailable", "Automated inbox modes are not enabled for this provider.")
	case errors.Is(err, ErrInboxAIServiceUnavailable):
		writeError(w, http.StatusUnprocessableEntity, "service_unavailable", err.Error())
	case errors.Is(err, ErrInboxAIControlBlocked):
		writeError(w, http.StatusConflict, "automation_blocked", "This conversation is paused or under provider control.")
	case errors.Is(err, ErrInboxAIInvalidState):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not update inbox AI state.")
	}
	return false
}
