package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"booking/go-server/internal/authchallenge"
	"booking/go-server/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
	cfg     config.Config
}

type contextKey string

const userContextKey contextKey = "auth.user"

func NewHandler(service *Service, cfg config.Config) *Handler {
	return &Handler{service: service, cfg: cfg}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/capabilities", h.capabilities)
	r.Post("/code", h.startCode)
	r.Get("/code/{challengeID}", h.codeStatus)
	r.Post("/code/resend", h.resendCode)
	r.Post("/verify", h.verifyCode)
	r.Post("/password", h.login)
	r.Post("/password/reset/code", h.startPasswordReset)
	r.Post("/password/reset/verify", h.verifyPasswordReset)
	r.Post("/password/reset", h.completePasswordReset)
	r.Post("/session", h.session)
	r.Post("/logout", h.logout)
}

// ProtectedRoutes is mounted below /v1/app after the provider authentication
// middleware. Keeping these operations here prevents appdata from duplicating
// authentication ownership.
func (h *Handler) ProtectedRoutes(r chi.Router) {
	r.Patch("/me/password", h.updatePassword)
	r.Post("/me/identities/code", h.startIdentityLink)
	r.Get("/me/identities/code/{challengeID}", h.identityLinkStatus)
	r.Post("/me/identities/code/resend", h.resendIdentityLink)
	r.Post("/me/identities/verify", h.verifyIdentityLink)
}

func (h *Handler) updatePassword(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	input, err := decodeJSONLimit[struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter a valid new password.")
		return
	}
	updated, err := h.service.UpdatePassword(r.Context(), user.ID, input.CurrentPassword, input.NewPassword)
	switch {
	case errors.Is(err, ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "invalid_password", "Password must be between 8 and 72 characters.")
		return
	case errors.Is(err, ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Current password is incorrect.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "password_update_failed", "Could not save your password.")
		return
	}
	h.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": h.signUserMedia(r.Context(), updated), "reauthentication_required": true,
	})
}

func (h *Handler) capabilities(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, h.service.AuthCapabilities())
}

func (h *Handler) startCode(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSONLimit[struct {
		Identifier      string `json:"identifier"`
		DeliveryChannel string `json:"delivery_channel"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter a valid email address or phone number.")
		return
	}
	response, err := h.service.StartCodeChallenge(r.Context(), input.Identifier, input.DeliveryChannel)
	if err != nil {
		h.writeCodeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) codeStatus(w http.ResponseWriter, r *http.Request) {
	challengeID, err := uuid.Parse(chi.URLParam(r, "challengeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return
	}
	response, err := h.service.CodeChallengeStatus(r.Context(), challengeID)
	if err != nil {
		h.writeCodeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) resendCode(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSONLimit[struct {
		ChallengeID string `json:"challenge_id"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Start again to request a new code.")
		return
	}
	challengeID, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return
	}
	response, err := h.service.ResendCodeChallenge(r.Context(), challengeID)
	if err != nil {
		h.writeCodeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) verifyCode(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSONLimit[struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter the six-digit code.")
		return
	}
	challengeID, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	result, err := h.service.VerifyCodeChallenge(r.Context(), challengeID, input.Code, MetadataFromRequest(r))
	if err != nil {
		if errors.Is(err, authchallenge.ErrInvalidChallenge) {
			writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
			return
		}
		writeError(w, http.StatusInternalServerError, "verification_failed", "Could not verify the code.")
		return
	}
	result.User = h.signUserMedia(r.Context(), result.User)
	h.setSessionCookies(w, result.Pair.AccessToken, result.RefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": result.User, "is_new_account": result.IsNewAccount,
		"onboarding_required": result.OnboardingRequired,
	})
}

func (h *Handler) writeCodeChallengeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authchallenge.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "delivery_channel_unavailable", "That sign-in method is not available right now.")
	case errors.Is(err, authchallenge.ErrTooSoon):
		w.Header().Set("Retry-After", "45")
		writeError(w, http.StatusTooManyRequests, "code_requested_too_recently", "Wait a moment before requesting another code.")
	case errors.Is(err, authchallenge.ErrInvalidIdentifier):
		writeError(w, http.StatusBadRequest, "invalid_identifier", "Enter a valid email address or phone number.")
	case errors.Is(err, authchallenge.ErrInvalidChallenge), errors.Is(err, authchallenge.ErrNotFound):
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
	default:
		slog.Error("provider auth code request failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "code_delivery_unavailable", "Could not prepare the code. Try again shortly.")
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[loginInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	user, pair, refreshToken, err := h.service.Login(r.Context(), input, MetadataFromRequest(r))
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "Contact or password is incorrect.")
			return
		}
		writeError(w, http.StatusBadRequest, "login_failed", err.Error())
		return
	}

	user = h.signUserMedia(r.Context(), user)
	h.setSessionCookies(w, pair.AccessToken, refreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (h *Handler) startPasswordReset(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSONLimit[struct {
		Identifier      string `json:"identifier"`
		DeliveryChannel string `json:"delivery_channel"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter a valid email address or phone number.")
		return
	}
	response, err := h.service.StartPasswordReset(r.Context(), input.Identifier, input.DeliveryChannel)
	if err != nil {
		h.writeCodeChallengeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) verifyPasswordReset(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSONLimit[struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}](r, 4<<10)
	if err != nil {
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
	input, err := decodeJSONLimit[struct {
		ResetGrant  string `json:"reset_grant"`
		NewPassword string `json:"new_password"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_reset", "Reset request is invalid or expired.")
		return
	}
	result, err := h.service.CompletePasswordReset(r.Context(), input.ResetGrant, input.NewPassword, MetadataFromRequest(r))
	if errors.Is(err, ErrInvalidResetToken) {
		writeError(w, http.StatusBadRequest, "invalid_reset", "Reset request is invalid or expired.")
		return
	}
	if err != nil {
		slog.Error("complete provider password reset", "error", err)
		writeError(w, http.StatusInternalServerError, "password_reset_failed", "Could not reset the password. Try again.")
		return
	}
	result.User = h.signUserMedia(r.Context(), result.User)
	h.setSessionCookies(w, result.Pair.AccessToken, result.RefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"user": result.User})
}

func (h *Handler) startIdentityLink(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	input, err := decodeJSONLimit[struct {
		Identifier      string `json:"identifier"`
		DeliveryChannel string `json:"delivery_channel"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Enter a contact to verify.")
		return
	}
	response, err := h.service.StartIdentityLink(r.Context(), user.ID, input.Identifier, input.DeliveryChannel)
	if err != nil {
		h.writeIdentityLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) identityLinkStatus(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	challengeID, err := uuid.Parse(chi.URLParam(r, "challengeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return
	}
	response, err := h.service.IdentityLinkStatus(r.Context(), user.ID, challengeID)
	if err != nil {
		h.writeIdentityLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) resendIdentityLink(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	input, err := decodeJSONLimit[struct {
		ChallengeID string `json:"challenge_id"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return
	}
	challengeID, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_challenge", "Start again to request a new code.")
		return
	}
	response, err := h.service.ResendIdentityLink(r.Context(), user.ID, challengeID)
	if err != nil {
		h.writeIdentityLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) verifyIdentityLink(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	input, err := decodeJSONLimit[struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}](r, 4<<10)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	challengeID, err := uuid.Parse(input.ChallengeID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_verification_code", "Code is invalid or expired.")
		return
	}
	updated, err := h.service.VerifyIdentityLink(r.Context(), user.ID, challengeID, input.Code)
	if err != nil {
		h.writeIdentityLinkError(w, err)
		return
	}
	h.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"user": h.signUserMedia(r.Context(), updated), "reauthentication_required": true})
}

func (h *Handler) writeIdentityLinkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrIdentityAlreadyLinked):
		writeError(w, http.StatusConflict, "identity_already_linked", "That contact is already linked to your account.")
	case errors.Is(err, ErrIdentityConflict):
		writeError(w, http.StatusConflict, "identity_unavailable", "That contact is already in use or this account already has that contact type.")
	default:
		h.writeCodeChallengeError(w, err)
	}
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	if accessToken := h.extractAccessToken(r); accessToken != "" {
		user, err := h.service.AuthenticateAccessToken(r.Context(), accessToken)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"user": h.signUserMedia(r.Context(), user)})
			return
		}
	}

	refreshToken := h.extractRefreshToken(r)
	if refreshToken == "" {
		writeError(w, http.StatusUnauthorized, "session_expired", "Your session has expired.")
		return
	}

	user, pair, nextRefreshToken, err := h.service.Refresh(r.Context(), refreshToken, MetadataFromRequest(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "session_expired", "Your session has expired.")
		return
	}

	h.setSessionCookies(w, pair.AccessToken, nextRefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"user": h.signUserMedia(r.Context(), user)})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	refreshToken := h.extractRefreshToken(r)
	err := h.service.Logout(r.Context(), refreshToken)
	h.clearSessionCookies(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "logout_failed", "Could not end session.")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accessToken := h.extractAccessToken(r)
		if accessToken == "" {
			writeError(w, http.StatusUnauthorized, "missing_access_token", "Access token is required.")
			return
		}

		user, err := h.service.AuthenticateAccessToken(r.Context(), accessToken)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_access_token", "Access token is invalid or expired.")
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey).(User)
	return user, ok
}

func (h *Handler) AuthMiddleware() func(http.Handler) http.Handler {
	return h.requireAuth
}

func (h *Handler) setSessionCookies(w http.ResponseWriter, accessToken, refreshToken string) {
	h.setAuthCookie(w, h.cfg.AuthAccessCookieName, accessToken, h.cfg.AuthAccessTokenTTL)
	h.setAuthCookie(w, h.cfg.AuthRefreshCookieName, refreshToken, h.cfg.AuthRefreshTokenTTL)
}

func (h *Handler) setAuthCookie(w http.ResponseWriter, name, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		Domain:   h.cfg.AuthCookieDomain,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.AuthCookieSecure,
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().UTC().Add(ttl),
	})
}

func (h *Handler) clearSessionCookies(w http.ResponseWriter) {
	h.clearAuthCookie(w, h.cfg.AuthAccessCookieName)
	h.clearAuthCookie(w, h.cfg.AuthRefreshCookieName)
}

func (h *Handler) clearAuthCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Domain:   h.cfg.AuthCookieDomain,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.AuthCookieSecure,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
	})
}

func (h *Handler) extractAccessToken(r *http.Request) string {
	if cookie, err := r.Cookie(h.cfg.AuthAccessCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func (h *Handler) extractRefreshToken(r *http.Request) string {
	if cookie, err := r.Cookie(h.cfg.AuthRefreshCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func decodeJSON[T any](r *http.Request) (T, error) {
	return decodeJSONLimit[T](r, 1<<20)
}

func decodeJSONLimit[T any](r *http.Request, maxBytes int64) (T, error) {
	var payload T

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&payload); err != nil {
		return payload, err
	}

	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return payload, errors.New("request body must contain a single JSON object")
	}

	return payload, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
