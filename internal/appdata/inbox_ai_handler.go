package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/auth"

	"github.com/google/uuid"
)

func (h *Handler) generateProviderInboxAIDraft(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	if !h.inboxAIDraftsAvailable(client.ID) {
		writeError(w, http.StatusNotFound, "not_available", "AI reply drafts are not enabled.")
		return
	}
	conversationID, err := uuidFromURLParam("conversationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Conversation ID is invalid.")
		return
	}
	input, err := decodeJSON[GenerateInboxAIDraftInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "AI draft request is invalid.")
		return
	}
	requestID, err := uuid.Parse(input.ClientRequestID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "AI draft request ID is invalid.")
		return
	}

	existing, err := h.repo.FindProviderInboxAIDraftRunByRequest(r.Context(), client.ID, conversationID, requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load the AI reply draft request.")
		return
	}
	if existing != nil {
		switch {
		case existing.Status == "completed" && existing.ProviderOutcome == "pending":
			writeJSON(w, http.StatusOK, inboxAIDraftResponseFromRun(existing, true))
			return
		case existing.Status == "queued" || existing.Status == "processing" || existing.Status == "running":
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusAccepted, inboxAIDraftResponseFromRun(existing, true))
			return
		case existing.Status == "completed":
			writeError(w, http.StatusConflict, "ai_draft_closed", "This AI draft has already been used or discarded.")
			return
		}
	}
	if !h.enforceInboxCommandLimit(r.Context(), w, inboxCommandAI, "provider", client.ID, conversationID) {
		return
	}

	snapshot, err := h.repo.LoadProviderInboxAIDraftContext(r.Context(), client.ID, conversationID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Conversation was not found.")
		return
	}
	if errors.Is(err, ErrInboxConversationDisabled) {
		writeError(w, http.StatusConflict, "conversation_disabled", "Messaging is unavailable for this conversation.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not prepare an AI reply draft.")
		return
	}

	runID, err := h.repo.StartProviderInboxAIDraftRun(
		r.Context(), client.ID, conversationID, requestID,
		h.inboxAIModelProvider, h.inboxAIModelName, h.inboxAIModelConfigHash, snapshot,
	)
	if errors.Is(err, ErrInboxAIDraftInProgress) {
		existing, loadErr := h.repo.FindProviderInboxAIDraftRunByRequest(r.Context(), client.ID, conversationID, requestID)
		if loadErr != nil || existing == nil {
			writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load the AI reply draft request.")
			return
		}
		if existing.Status == "completed" && existing.ProviderOutcome == "pending" {
			writeJSON(w, http.StatusOK, inboxAIDraftResponseFromRun(existing, true))
			return
		}
		if existing.Status == "completed" {
			writeError(w, http.StatusConflict, "ai_draft_closed", "This AI draft has already been used or discarded.")
			return
		}
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusAccepted, inboxAIDraftResponseFromRun(existing, true))
		return
	}
	if errors.Is(err, ErrInboxAIServiceUnavailable) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "ai_busy", "AI is busy. Please try again shortly.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not start an AI reply draft.")
		return
	}

	run, err := h.repo.GetProviderInboxAIDraftRun(r.Context(), client.ID, conversationID, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load the queued AI reply draft.")
		return
	}
	w.Header().Set("Retry-After", "2")
	writeJSON(w, http.StatusAccepted, inboxAIDraftResponseFromRun(run, false))
}

func (h *Handler) getProviderInboxAIDraft(w http.ResponseWriter, r *http.Request) {
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
	runID, err := uuidFromURLParam("runID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "AI draft run ID is invalid.")
		return
	}
	run, err := h.repo.GetProviderInboxAIDraftRun(r.Context(), client.ID, conversationID, runID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "AI reply draft was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not load the AI reply draft.")
		return
	}
	writeJSON(w, http.StatusOK, inboxAIDraftResponseFromRun(run, false))
}

func (h *Handler) discardProviderInboxAIDraft(w http.ResponseWriter, r *http.Request) {
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
	runID, err := uuidFromURLParam("runID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "AI draft run ID is invalid.")
		return
	}
	input, err := decodeJSON[DiscardInboxAIDraftInput](r)
	if err != nil || (input.Reason != "discarded" && input.Reason != "stale") {
		writeError(w, http.StatusBadRequest, "invalid_request", "AI draft discard reason is invalid.")
		return
	}
	err = h.repo.DiscardProviderInboxAIDraftRun(r.Context(), client.ID, conversationID, runID, input.Reason)
	if errors.Is(err, ErrInboxAIDraftInvalid) {
		writeError(w, http.StatusConflict, "ai_draft_closed", "This AI draft is no longer active.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_unavailable", "Could not update the AI draft.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func inboxAIDraftResponseFromRun(run *ProviderInboxAIDraftRun, replayed bool) InboxAIDraftResponse {
	return InboxAIDraftResponse{
		RunID: run.ID.String(), Status: run.Status, Draft: run.Draft, NeedsProviderInput: run.NeedsProviderInput,
		Warnings: run.Warnings, SourceMessageID: nullableUUIDString(run.LatestMessageID),
		Replayed: replayed, CreatedAt: run.CreatedAt, ErrorCode: run.ErrorCode,
	}
}

func nullableUUIDString(id uuid.NullUUID) string {
	if id.Valid {
		return id.UUID.String()
	}
	return ""
}
