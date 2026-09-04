package appdata

import (
	"errors"
	"net/http"
	"strings"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/whatsapp"
)

func (h *Handler) getProviderNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok || h.notificationContacts == nil {
		writeError(w, http.StatusServiceUnavailable, "notification_preferences_unavailable", "Notification preferences are unavailable.")
		return
	}
	preferences, err := h.notificationContacts.GetProviderPreferences(r.Context(), client.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "notification_preferences_failed", "Could not load notification preferences.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": preferences})
}

func (h *Handler) updateProviderNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok || h.notificationContacts == nil {
		writeError(w, http.StatusServiceUnavailable, "notification_preferences_unavailable", "Notification preferences are unavailable.")
		return
	}
	input, err := decodeJSON[whatsapp.ProviderNotificationPreferencesPatch](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.BookingEmail == nil && input.BookingWhatsApp == nil &&
		input.AppointmentReminderEnabled == nil && input.AppointmentReminderMinutes == nil {
		writeError(w, http.StatusBadRequest, "empty_preference_update", "Choose at least one preference to update.")
		return
	}
	preferences, err := h.notificationContacts.UpdateProviderPreferences(r.Context(), client.ID, input)
	switch {
	case errors.Is(err, whatsapp.ErrProviderWhatsAppUnverified):
		writeError(w, http.StatusConflict, "whatsapp_not_verified", "Verify this WhatsApp number before enabling notifications.")
	case errors.Is(err, whatsapp.ErrUnsupportedProviderReminderOffset):
		writeError(w, http.StatusUnprocessableEntity, "unsupported_reminder_offset", "Appointment reminders currently support the 24-hour schedule only.")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "notification_preferences_update_failed", "Could not update notification preferences.")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"preferences": preferences})
	}
}

func (h *Handler) startProviderWhatsAppVerification(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok || h.notificationContacts == nil {
		writeError(w, http.StatusServiceUnavailable, "whatsapp_verification_unavailable", "WhatsApp verification is unavailable.")
		return
	}
	input, err := decodeJSON[struct {
		Phone string `json:"phone"`
	}](r)
	if err != nil || strings.TrimSpace(input.Phone) == "" {
		writeError(w, http.StatusBadRequest, "invalid_whatsapp_number", "Enter a valid WhatsApp number.")
		return
	}
	verification, err := h.notificationContacts.StartProviderWhatsAppVerification(
		r.Context(), client.ID, input.Phone,
	)
	switch {
	case errors.Is(err, whatsapp.ErrProviderWhatsAppVerificationUnavailable):
		writeError(w, http.StatusServiceUnavailable, "whatsapp_verification_unavailable", "WhatsApp verification is unavailable until the inbound webhook is configured.")
	case errors.Is(err, whatsapp.ErrProviderWhatsAppAlreadyVerified):
		writeError(w, http.StatusConflict, "whatsapp_already_verified", "This WhatsApp number is already verified.")
	case errors.Is(err, whatsapp.ErrInvalidWhatsAppDestination):
		writeError(w, http.StatusUnprocessableEntity, "invalid_whatsapp_number", "Enter a valid WhatsApp number for your business country.")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "whatsapp_verification_failed", "Could not start WhatsApp verification.")
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"verification": verification})
	}
}
