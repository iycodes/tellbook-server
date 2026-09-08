package appdata

import (
	"errors"
	"net/http"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/whatsapp"
)

func (h *Handler) customerContact(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	if h.notificationContacts == nil {
		writeError(w, http.StatusServiceUnavailable, "customer_contact_unavailable", "Customer contact settings are unavailable.")
		return
	}
	ctx := r.Context()
	var result any
	var err error
	switch r.Method {
	case http.MethodGet:
		result, err = h.notificationContacts.GetCustomerContact(ctx, client.ID)
	case http.MethodPatch:
		var input whatsapp.CustomerContactPatch
		input, err = decodeJSON[whatsapp.CustomerContactPatch](r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Enter valid contact settings.")
			return
		}
		result, err = h.notificationContacts.UpdateCustomerContact(ctx, client.ID, input)
	case http.MethodDelete:
		var input struct {
			Revision int64 `json:"revision"`
		}
		input, err = decodeJSON[struct {
			Revision int64 `json:"revision"`
		}](r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Provide the current settings revision.")
			return
		}
		err = h.notificationContacts.RemoveCustomerContact(ctx, client.ID, input.Revision)
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if err != nil {
		writeCustomerContactError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contact": result})
}

type customerContactNumberInput struct {
	Phone    string `json:"phone"`
	Revision int64  `json:"revision"`
}

func (h *Handler) customerContactNumber(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in to continue.")
		return
	}
	if h.notificationContacts == nil {
		writeError(w, http.StatusServiceUnavailable, "customer_contact_unavailable", "Customer contact settings are unavailable.")
		return
	}
	input, err := decodeJSON[customerContactNumberInput](r)
	if err != nil || input.Phone == "" || input.Revision < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter a phone number and the current settings revision.")
		return
	}
	if r.URL.Path == "/v1/app/profile/customer-contact/reuse-verified" {
		contact, err := h.notificationContacts.ReuseCustomerContact(r.Context(), client.ID, input.Phone, input.Revision)
		if err != nil {
			writeCustomerContactError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"contact": contact})
		return
	}
	verification, err := h.notificationContacts.StartCustomerContactVerification(r.Context(), client.ID, input.Phone, input.Revision)
	if err != nil {
		writeCustomerContactError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"verification": verification})
}

func writeCustomerContactError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, whatsapp.ErrCustomerContactNotFound):
		writeError(w, http.StatusNotFound, "profile_not_found", "Set up your business profile first.")
	case errors.Is(err, whatsapp.ErrCustomerContactConflict):
		writeError(w, http.StatusConflict, "customer_contact_changed", "Contact settings changed. Reload them before saving.")
	case errors.Is(err, whatsapp.ErrCustomerContactUnverified):
		writeError(w, http.StatusConflict, "customer_contact_unverified", "Verify this number before sharing it with customers.")
	case errors.Is(err, whatsapp.ErrProviderWhatsAppVerificationUnavailable):
		writeError(w, http.StatusServiceUnavailable, "whatsapp_verification_unavailable", "WhatsApp verification is unavailable.")
	case errors.Is(err, whatsapp.ErrProviderWhatsAppAlreadyVerified):
		writeError(w, http.StatusConflict, "customer_contact_already_verified", "This contact number is already verified.")
	case errors.Is(err, whatsapp.ErrInvalidWhatsAppDestination), errors.Is(err, whatsapp.ErrCustomerContactInvalid):
		writeError(w, http.StatusBadRequest, "invalid_customer_contact", "Enter a valid phone number or sharing setting.")
	default:
		writeError(w, http.StatusInternalServerError, "customer_contact_failed", "Could not update customer contact settings.")
	}
}
