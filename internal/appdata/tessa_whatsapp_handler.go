package appdata

import (
	"booking/go-server/internal/auth"
	"booking/go-server/internal/whatsapp"
	"errors"
	"net/http"

	"github.com/google/uuid"
)

func (h *Handler) ConfigureTessaWhatsApp(repo *whatsapp.TessaLinkRepository) { h.tessaWhatsApp = repo }

// Reading and revoking an existing grant must remain possible when AI access is disabled.
func tessaWhatsAppAccount(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return uuid.Nil, false
	}
	return client.ID, true
}
func (h *Handler) getTessaWhatsApp(w http.ResponseWriter, r *http.Request) {
	id, ok := tessaWhatsAppAccount(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.tessaWhatsApp == nil {
		writeError(w, 503, "tessa_whatsapp_unavailable", "Tessa WhatsApp is unavailable.")
		return
	}
	state, err := h.tessaWhatsApp.State(r.Context(), id)
	if err != nil {
		writeError(w, 500, "tessa_whatsapp_state_failed", "Could not load the WhatsApp connection.")
		return
	}
	writeJSON(w, 200, state)
}
func (h *Handler) startTessaWhatsAppLink(w http.ResponseWriter, r *http.Request) {
	id, ok := h.tessaClient(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.tessaWhatsApp == nil {
		writeError(w, 503, "tessa_whatsapp_unavailable", "Tessa WhatsApp is unavailable.")
		return
	}
	input, err := decodeJSON[struct {
		Destination    string `json:"destination"`
		NoticeRevision string `json:"notice_revision"`
	}](r)
	if err != nil {
		writeError(w, 400, "invalid_request", "Enter your international WhatsApp number and accept the notice.")
		return
	}
	result, err := h.tessaWhatsApp.Start(r.Context(), id, input.Destination, input.NoticeRevision)
	switch {
	case errors.Is(err, whatsapp.ErrTessaLinkUnavailable):
		writeError(w, 503, "tessa_whatsapp_unavailable", "Tessa WhatsApp linking is unavailable.")
	case errors.Is(err, whatsapp.ErrTessaLinkRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "rate_limited", "Wait a minute before requesting another link.")
	case errors.Is(err, whatsapp.ErrTessaLinkNotice):
		writeError(w, 409, "notice_changed", "Review the current WhatsApp notice.")
	case errors.Is(err, whatsapp.ErrInvalidWhatsAppDestination):
		writeError(w, 400, "invalid_destination", "Enter a WhatsApp number including its country code, for example +234….")
	case err != nil:
		writeError(w, 500, "tessa_whatsapp_link_failed", "Could not create the linking request.")
	default:
		writeJSON(w, 200, result)
	}
}
func (h *Handler) disconnectTessaWhatsApp(w http.ResponseWriter, r *http.Request) {
	id, ok := tessaWhatsAppAccount(w, r)
	if !ok {
		return
	}
	if h.tessaWhatsApp == nil {
		writeError(w, 503, "tessa_whatsapp_unavailable", "Tessa WhatsApp is unavailable.")
		return
	}
	if err := h.tessaWhatsApp.Disconnect(r.Context(), id); err != nil {
		writeError(w, 500, "tessa_whatsapp_disconnect_failed", "Could not disconnect WhatsApp.")
		return
	}
	h.getTessaWhatsApp(w, r)
}
