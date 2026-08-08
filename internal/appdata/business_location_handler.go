package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/auth"
)

func (h *Handler) listBusinessLocations(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	items, err := h.repo.ListBusinessLocations(r.Context(), client.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "business_locations_failed", "Could not load business locations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createBusinessLocation(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	input, err := decodeJSON[UpsertBusinessLocationInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.repo.CreateBusinessLocation(r.Context(), client.ID, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_business_location_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateBusinessLocation(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	locationID, err := uuidFromURLParam("locationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_location_id", "Location ID is invalid.")
		return
	}
	input, err := decodeJSON[UpsertBusinessLocationInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.repo.UpdateBusinessLocation(r.Context(), client.ID, locationID, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "business_location_not_found", "Location was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "update_business_location_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) archiveBusinessLocation(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	locationID, err := uuidFromURLParam("locationID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_location_id", "Location ID is invalid.")
		return
	}
	if err := h.repo.ArchiveBusinessLocation(r.Context(), client.ID, locationID); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, "business_location_not_found", "Location was not found.")
		case errors.Is(err, ErrLocationInUse):
			writeError(w, http.StatusConflict, "business_location_in_use", "Pause or move published services before archiving this location.")
		default:
			writeError(w, http.StatusInternalServerError, "archive_business_location_failed", "Could not archive location.")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
