package appdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"booking/go-server/internal/auth"
)

func (h *Handler) prepareServiceWizardDraftResponse(r *http.Request, draft ServiceWizardDraft) (ServiceWizardDraft, error) {
	var payload struct {
		ImageURL string `json:"imageUrl"`
	}
	if err := json.Unmarshal(draft.Payload, &payload); err != nil {
		return ServiceWizardDraft{}, fmt.Errorf("decode service wizard image URL: %w", err)
	}
	if strings.TrimSpace(payload.ImageURL) == "" {
		return draft, nil
	}

	if h.storage == nil {
		draft.ImagePreviewURL = payload.ImageURL
		return draft, nil
	}
	previewURL, err := h.storage.ResolveBrowserURL(r.Context(), payload.ImageURL, signedMediaURLTTL)
	if err != nil {
		return ServiceWizardDraft{}, fmt.Errorf("sign service wizard image URL: %w", err)
	}
	draft.ImagePreviewURL = previewURL
	return draft, nil
}

func (h *Handler) createServiceWizardDraft(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	input, err := decodeJSON[CreateServiceWizardDraftInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	draft, err := h.repo.CreateServiceWizardDraft(r.Context(), client.ID, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_not_found", "Service was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "create_service_wizard_draft_failed", err.Error())
		return
	}
	draft, err = h.prepareServiceWizardDraftResponse(r, draft)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "service_wizard_draft_failed", "Could not prepare the service draft.")
		return
	}
	writeJSON(w, http.StatusCreated, draft)
}

func (h *Handler) listNewServiceWizardDrafts(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	drafts, err := h.repo.ListNewServiceWizardDrafts(r.Context(), client.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "service_wizard_drafts_failed", "Could not load service drafts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": drafts})
}

func (h *Handler) getServiceWizardDraft(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	draftID, err := uuidFromURLParam("draftID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_wizard_draft_id", "Service draft ID is invalid.")
		return
	}
	draft, err := h.repo.GetServiceWizardDraft(r.Context(), client.ID, draftID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_wizard_draft_not_found", "Service draft was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "service_wizard_draft_failed", "Could not load the service draft.")
		return
	}
	draft, err = h.prepareServiceWizardDraftResponse(r, draft)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "service_wizard_draft_failed", "Could not prepare the service draft.")
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (h *Handler) updateServiceWizardDraft(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	draftID, err := uuidFromURLParam("draftID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_wizard_draft_id", "Service draft ID is invalid.")
		return
	}
	input, err := decodeJSON[UpdateServiceWizardDraftInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	draft, err := h.repo.UpdateServiceWizardDraft(r.Context(), client.ID, draftID, input)
	if err != nil {
		switch {
		case errors.Is(err, ErrServiceWizardDraftConflict):
			writeError(w, http.StatusConflict, "service_wizard_draft_conflict", "This service draft changed elsewhere. Reload it before continuing.")
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, "service_wizard_draft_not_found", "Service draft was not found.")
		default:
			writeError(w, http.StatusBadRequest, "update_service_wizard_draft_failed", err.Error())
		}
		return
	}
	draft, err = h.prepareServiceWizardDraftResponse(r, draft)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "service_wizard_draft_failed", "Could not prepare the service draft.")
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (h *Handler) deleteServiceWizardDraft(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	draftID, err := uuidFromURLParam("draftID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_wizard_draft_id", "Service draft ID is invalid.")
		return
	}
	if err := h.repo.DeleteServiceWizardDraft(r.Context(), client.ID, draftID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "service_wizard_draft_not_found", "Service draft was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete_service_wizard_draft_failed", "Could not delete the service draft.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
