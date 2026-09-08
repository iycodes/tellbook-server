package marketplaceauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/whatsapp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const sessionCookieName = "tellbook_marketplace_session"

type Handler struct {
	service *Service
	repo    *Repository
	cfg     config.Config
}

func NewHandler(service *Service, repo *Repository, cfg config.Config) *Handler {
	return &Handler{service: service, repo: repo, cfg: cfg}
}

func (h *Handler) NotificationCapabilities() NotificationDeliveryCapabilities {
	capabilities := NotificationDeliveryCapabilities{
		EmailReminderAvailable: h.cfg.NotificationEmailEnabled,
	}
	if !h.cfg.NotificationWhatsAppEnabled {
		return capabilities
	}
	definition, known := whatsapp.LookupTemplate(whatsapp.TemplateUserReminder)
	if !known || definition.RequiresContractHold {
		return capabilities
	}
	for _, rawKey := range h.cfg.WhatsAppEnabledTemplateKeys {
		if whatsapp.TemplateKey(strings.TrimSpace(rawKey)) == whatsapp.TemplateUserReminder {
			capabilities.WhatsAppReminderAvailable = true
			break
		}
	}
	return capabilities
}

func (h *Handler) Routes(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Get("/capabilities", h.authCapabilities)
		r.Post("/code", h.startCode)
		r.Get("/code/{challengeID}", h.codeStatus)
		r.Post("/code/resend", h.resendCode)
		r.Post("/verify", h.verifyCode)
		r.Post("/password", h.passwordLogin)
		r.Post("/password/reset/code", h.startPasswordReset)
		r.Post("/password/reset/verify", h.verifyPasswordReset)
		r.Post("/password/reset", h.completePasswordReset)
		r.Get("/session", h.session)
		r.Post("/session", h.session)
		r.Post("/logout", h.logout)
	})
	r.Group(func(r chi.Router) {
		r.Use(h.AuthMiddleware())
		r.Get("/me", h.getProfile)
		r.Patch("/me", h.updateProfile)
		r.Patch("/me/password", h.setPassword)
		r.Post("/me/identities/code", h.startIdentityLink)
		r.Get("/me/identities/code/{challengeID}", h.identityLinkStatus)
		r.Post("/me/identities/code/resend", h.resendIdentityLink)
		r.Post("/me/identities/verify", h.verifyIdentityLink)
		r.Get("/me/addresses", h.listAddresses)
		r.Post("/me/addresses/default-location", h.defaultAddressLocation)
		r.Post("/me/addresses", h.createAddress)
		r.Patch("/me/addresses/{addressID}", h.updateAddress)
		r.Delete("/me/addresses/{addressID}", h.deleteAddress)
		r.Get("/me/notification-preferences", h.getPreferences)
		r.Patch("/me/notification-preferences", h.updatePreferences)
	})
}

func (h *Handler) authCapabilities(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, h.service.AuthCapabilities())
}

type contextKey string

const customerContextKey contextKey = "marketplace.customer"

func CustomerFromContext(ctx context.Context) (Customer, bool) {
	customer, ok := ctx.Value(customerContextKey).(Customer)
	return customer, ok
}

func (h *Handler) AuthMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := h.principalFromRequest(r, sessionRequiresFreshValidation(r.Method))
			if err != nil {
				if errors.Is(err, ErrAuthUnavailable) {
					writeError(w, http.StatusServiceUnavailable, "auth_temporarily_unavailable", "Sign-in is temporarily unavailable. Please try again shortly.")
					return
				}
				writeError(w, http.StatusUnauthorized, "session_expired", "Sign in to continue.")
				return
			}
			customer := Customer{ID: principal.CustomerID}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), customerContextKey, customer)))
		})
	}
}

func sessionRequiresFreshValidation(_ string) bool {
	// PostgreSQL owns authorization. A positive cache entry cannot prove that a
	// password reset or identity change has not revoked the session since it was cached.
	return true
}

func (h *Handler) principalFromRequest(r *http.Request, forceFresh bool) (SessionPrincipal, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return SessionPrincipal{}, ErrNotFound
	}
	return h.service.AuthenticatePrincipal(r.Context(), cookie.Value, forceFresh)
}

func (h *Handler) CustomerFromRequest(r *http.Request) (Customer, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return Customer{}, ErrNotFound
	}
	return h.service.Authenticate(r.Context(), cookie.Value)
}

func (h *Handler) startCode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Identifier      string `json:"identifier"`
		DeliveryChannel string `json:"delivery_channel"`
	}
	if err := decodeJSONLimit(r, &input, 4<<10); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	result, err := h.service.StartChallenge(r.Context(), input.Identifier, input.DeliveryChannel)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) resendCode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ChallengeID string `json:"challenge_id"`
	}
	if err := decodeJSONLimit(r, &input, 4<<10); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	id, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, 400, "invalid_challenge", "Start again to request a new code.")
		return
	}
	result, err := h.service.ResendChallenge(r.Context(), id)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) codeStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "challengeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return
	}
	result, err := h.service.ChallengeStatus(r.Context(), id)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) writeChallengeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrDeliveryUnavailable):
		writeError(w, http.StatusServiceUnavailable, "delivery_channel_unavailable", "That sign-in method is not available right now.")
	case errors.Is(err, ErrChallengeTooSoon):
		w.Header().Set("Retry-After", "45")
		writeError(w, http.StatusTooManyRequests, "code_requested_too_recently", "Wait a moment before requesting another code.")
	case errors.Is(err, ErrInvalidIdentifier):
		message := strings.TrimPrefix(err.Error(), ErrInvalidIdentifier.Error()+": ")
		writeError(w, http.StatusBadRequest, "invalid_identifier", message)
	case errors.Is(err, ErrIdentityAlreadyLinked):
		writeError(w, http.StatusConflict, "identity_already_linked", "That contact is already linked to your account.")
	case errors.Is(err, ErrIdentityConflict):
		writeError(w, http.StatusConflict, "identity_unavailable", "That contact is already in use or this account already has that contact type.")
	case errors.Is(err, ErrInvalidChallenge):
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
	default:
		writeError(w, http.StatusInternalServerError, "code_delivery_failed", "Could not send the code. Try again shortly.")
	}
}

func (h *Handler) verifyCode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if err := decodeJSONLimit(r, &input, 4<<10); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	id, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, 400, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	customer, token, isNewAccount, err := h.service.VerifyChallenge(r.Context(), id, input.Code, r.UserAgent(), clientIP(r))
	if err != nil {
		if errors.Is(err, ErrInvalidChallenge) {
			writeError(w, 400, "invalid_verification_code", "Code is invalid or expired.")
			return
		}
		writeError(w, 500, "verification_failed", "Could not verify the code.")
		return
	}
	h.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"customer": customer, "is_new_account": isNewAccount, "onboarding_required": false,
	})
}

func (h *Handler) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter your contact and password.")
		return
	}
	customer, token, err := h.service.PasswordLogin(r.Context(), input.Identifier, input.Password, r.UserAgent(), clientIP(r))
	if errors.Is(err, ErrInvalidPassword) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Contact or password is incorrect.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_login_failed", "Could not sign in. Try again shortly.")
		return
	}
	h.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"customer": customer})
}

func (h *Handler) startPasswordReset(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Identifier      string `json:"identifier"`
		DeliveryChannel string `json:"delivery_channel"`
	}
	if err := decodeJSONLimit(r, &input, 4<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter a valid email address or phone number.")
		return
	}
	result, err := h.service.StartPasswordReset(r.Context(), input.Identifier, input.DeliveryChannel)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) verifyPasswordReset(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if err := decodeJSONLimit(r, &input, 4<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	challengeID, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	result, err := h.service.VerifyPasswordReset(r.Context(), challengeID, input.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) completePasswordReset(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ResetGrant  string `json:"reset_grant"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSONLimit(r, &input, 4<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_reset", "Reset request is invalid or expired.")
		return
	}
	customer, token, err := h.service.CompletePasswordReset(r.Context(), input.ResetGrant, input.NewPassword, r.UserAgent(), clientIP(r))
	if errors.Is(err, ErrInvalidChallenge) {
		writeError(w, http.StatusBadRequest, "invalid_reset", "Reset request is invalid or expired.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_reset_failed", "Could not reset the password. Try again.")
		return
	}
	h.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"customer": customer})
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	principal, err := h.principalFromRequest(r, true)
	if err != nil {
		if errors.Is(err, ErrAuthUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "auth_temporarily_unavailable", "Sign-in is temporarily unavailable. Please try again shortly.")
			return
		}
		writeError(w, 401, "session_expired", "Sign in to continue.")
		return
	}
	customer, err := h.repo.CustomerByID(r.Context(), principal.CustomerID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "session_expired", "Sign in to continue.")
			return
		}
		writeError(w, http.StatusInternalServerError, "profile_load_failed", "Could not load your profile.")
		return
	}
	writeJSON(w, 200, map[string]any{"customer": customer})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := h.service.Logout(r.Context(), cookie.Value); err != nil {
			writeError(w, http.StatusServiceUnavailable, "logout_failed", "Could not sign you out. Please try again.")
			return
		}
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	principal, _ := CustomerFromContext(r.Context())
	customer, err := h.repo.CustomerByID(r.Context(), principal.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "session_expired", "Sign in to continue.")
			return
		}
		writeError(w, http.StatusInternalServerError, "profile_load_failed", "Could not load your profile.")
		return
	}
	writeJSON(w, 200, map[string]any{"customer": customer})
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	var input ProfileInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	input.FullName = strings.TrimSpace(input.FullName)
	input.Birthday = strings.TrimSpace(input.Birthday)
	if len(input.FullName) > 160 {
		writeError(w, 400, "invalid_profile", "Full name is too long.")
		return
	}
	if input.Birthday != "" {
		if _, err := time.Parse("2006-01-02", input.Birthday); err != nil {
			writeError(w, 400, "invalid_birthday", "Birthday must use YYYY-MM-DD.")
			return
		}
	}
	updated, err := h.service.UpdateProfile(r.Context(), customer.ID, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "profile_update_failed", "Could not update your profile.")
		return
	}
	writeJSON(w, 200, map[string]any{"customer": updated})
}

func (h *Handler) setPassword(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	updated, err := h.service.SetPassword(r.Context(), customer.ID, input.CurrentPassword, input.NewPassword)
	if errors.Is(err, ErrWeakPassword) {
		writeError(w, http.StatusBadRequest, "invalid_password", "Password must be between 8 and 72 characters.")
		return
	}
	if errors.Is(err, ErrInvalidPassword) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Current password is incorrect.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_update_failed", "Could not save your password.")
		return
	}
	h.clearSessionCookie(w)
	writeJSON(w, 200, map[string]any{"message": "Password saved. Sign in again to continue.", "customer": updated, "reauthentication_required": true})
}

func (h *Handler) startIdentityLink(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	var input struct {
		Identifier      string `json:"identifier"`
		DeliveryChannel string `json:"delivery_channel"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter a contact to verify.")
		return
	}
	result, err := h.service.StartIdentityLink(r.Context(), customer.ID, input.Identifier, input.DeliveryChannel)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) resendIdentityLink(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	id, ok := challengeIDFromRequest(w, r)
	if !ok {
		return
	}
	result, err := h.service.ResendIdentityLink(r.Context(), customer.ID, id)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *Handler) identityLinkStatus(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "challengeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return
	}
	result, err := h.service.IdentityLinkStatus(r.Context(), customer.ID, id)
	if err != nil {
		h.writeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) verifyIdentityLink(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	var input struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter the verification code.")
		return
	}
	id, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	updated, err := h.service.VerifyIdentityLink(r.Context(), customer.ID, id, input.Code)
	if errors.Is(err, ErrInvalidChallenge) {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	if errors.Is(err, ErrIdentityConflict) {
		writeError(w, http.StatusConflict, "identity_unavailable", "That contact is already linked to another account.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_link_failed", "Could not link that contact.")
		return
	}
	h.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"customer": updated, "reauthentication_required": true})
}

func challengeIDFromRequest(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	var input struct {
		ChallengeID string `json:"challenge_id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Start again to request a new code.")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listAddresses(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	items, err := h.repo.ListAddresses(r.Context(), customer.ID)
	if err != nil {
		writeError(w, 500, "addresses_failed", "Could not load addresses.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (h *Handler) defaultAddressLocation(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	location, err := h.repo.DefaultAddressLocation(r.Context(), customer.ID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "default_address_not_found", "No default service address is saved.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "default_address_failed", "Could not load your default address.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"location": location})
}
func (h *Handler) createAddress(w http.ResponseWriter, r *http.Request) { h.saveAddress(w, r, nil) }
func (h *Handler) updateAddress(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "addressID"))
	if err != nil {
		writeError(w, 400, "invalid_address", "Address ID is invalid.")
		return
	}
	h.saveAddress(w, r, &id)
}
func (h *Handler) saveAddress(w http.ResponseWriter, r *http.Request, addressID *uuid.UUID) {
	customer, _ := CustomerFromContext(r.Context())
	var input AddressInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	input.Label = strings.TrimSpace(input.Label)
	input.AddressLine1 = strings.TrimSpace(input.AddressLine1)
	input.AddressLine2 = strings.TrimSpace(input.AddressLine2)
	input.Locality = strings.TrimSpace(input.Locality)
	input.StateRegionID = strings.TrimSpace(input.StateRegionID)
	input.LGARegionID = strings.TrimSpace(input.LGARegionID)
	input.CountryCode = strings.ToUpper(strings.TrimSpace(input.CountryCode))
	input.PostalCode = strings.TrimSpace(input.PostalCode)
	input.LocationToken = strings.TrimSpace(input.LocationToken)
	if input.Label == "" || input.AddressLine1 == "" {
		writeError(w, 400, "invalid_address", "Label and address line are required.")
		return
	}
	if input.CountryCode == "" {
		input.CountryCode = "NG"
	}
	if input.CountryCode != "NG" {
		writeError(w, http.StatusBadRequest, "invalid_country", "Service addresses currently support Nigeria.")
		return
	}
	if len(input.Label) > 80 || len(input.AddressLine1) > 500 || len(input.AddressLine2) > 500 || len(input.Locality) > 160 || len(input.PostalCode) > 32 {
		writeError(w, http.StatusBadRequest, "invalid_address", "One or more address fields are too long.")
		return
	}
	if (input.Latitude == nil) != (input.Longitude == nil) {
		writeError(w, 400, "invalid_coordinates", "Latitude and longitude must be supplied together.")
		return
	}
	if input.Latitude != nil && (*input.Latitude < -90 || *input.Latitude > 90 || *input.Longitude < -180 || *input.Longitude > 180) {
		writeError(w, http.StatusBadRequest, "invalid_coordinates", "Coordinates are outside their valid range.")
		return
	}
	item, err := h.repo.SaveAddress(r.Context(), customer.ID, addressID, input)
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "address_not_found", "Address was not found.")
		return
	}
	if errors.Is(err, ErrInvalidRegion) {
		writeError(w, http.StatusBadRequest, "invalid_region_selection", "Choose a valid Nigerian State and LGA combination.")
		return
	}
	if errors.Is(err, ErrInvalidLocation) {
		writeError(w, http.StatusBadRequest, "invalid_location", "Validate the address again before saving it.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "address_save_failed", "Could not save the address.")
		return
	}
	status := 200
	if addressID == nil {
		status = 201
	}
	writeJSON(w, status, map[string]any{"address": item})
}
func (h *Handler) deleteAddress(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "addressID"))
	if err != nil {
		writeError(w, 400, "invalid_address", "Address ID is invalid.")
		return
	}
	if err := h.repo.DeleteAddress(r.Context(), customer.ID, id); errors.Is(err, ErrNotFound) {
		writeError(w, 404, "address_not_found", "Address was not found.")
		return
	} else if err != nil {
		writeError(w, 500, "address_delete_failed", "Could not delete address.")
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) getPreferences(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	item, err := h.repo.GetPreferences(r.Context(), customer.ID)
	if err != nil {
		writeError(w, 500, "preferences_failed", "Could not load preferences.")
		return
	}
	capabilities := h.NotificationCapabilities()
	item.EmailAvailable = item.EmailAvailable && capabilities.EmailReminderAvailable
	item.WhatsAppAvailable = item.WhatsAppAvailable && capabilities.WhatsAppReminderAvailable
	writeJSON(w, 200, map[string]any{"preferences": item})
}
func (h *Handler) updatePreferences(w http.ResponseWriter, r *http.Request) {
	customer, _ := CustomerFromContext(r.Context())
	var input NotificationPreferencesInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	item, err := h.repo.UpdatePreferences(r.Context(), customer.ID, input)
	if err != nil {
		writeError(w, 500, "preferences_update_failed", "Could not update preferences.")
		return
	}
	capabilities := h.NotificationCapabilities()
	item.EmailAvailable = item.EmailAvailable && capabilities.EmailReminderAvailable
	item.WhatsAppAvailable = item.WhatsAppAvailable && capabilities.WhatsAppReminderAvailable
	writeJSON(w, 200, map[string]any{"preferences": item})
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", Domain: h.cfg.AuthCookieDomain, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.cfg.AuthCookieSecure, MaxAge: int(h.cfg.AuthRefreshTokenTTL.Seconds()), Expires: time.Now().UTC().Add(h.cfg.AuthRefreshTokenTTL)})
}
func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", Domain: h.cfg.AuthCookieDomain, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.cfg.AuthCookieSecure, MaxAge: -1, Expires: time.Unix(0, 0).UTC()})
}

func decodeJSON(r *http.Request, target any) error {
	return decodeJSONLimit(r, target, 1<<20)
}

func decodeJSONLimit(r *http.Request, target any, maximumBytes int64) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maximumBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
