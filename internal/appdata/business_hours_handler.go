package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/auth"
)

func (h *Handler) getBusinessHours(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	response, err := h.repo.GetBusinessHours(r.Context(), client.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "business_hours_failed", "Could not load business hours.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) updateBusinessHours(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[UpdateBusinessHoursInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	response, err := h.repo.UpdateBusinessHours(r.Context(), client.ID, input)
	if err != nil {
		if errors.Is(err, ErrBusinessHoursRequired) ||
			errors.Is(err, ErrInvalidBusinessHours) ||
			errors.Is(err, ErrDuplicateBusinessDay) {
			writeError(w, http.StatusBadRequest, "invalid_business_hours", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "business_hours_update_failed", "Could not update business hours.")
		return
	}

	writeJSON(w, http.StatusOK, response)
}
