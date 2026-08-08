package appdata

import "net/http"

func (h *Handler) resolvePublicLocation(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[ResolvePublicLocationInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	response, err := h.repo.ResolvePublicLocation(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "location_resolution_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, response)
}
