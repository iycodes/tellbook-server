package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"booking/go-server/internal/config"

	"github.com/go-chi/chi/v5"
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
	r.Post("/register", h.register)
	r.Post("/register/verify", h.verifyRegistration)
	r.Post("/register/resend", h.resendRegistrationVerification)
	r.Post("/login", h.login)
	r.Post("/session", h.session)
	r.Post("/logout", h.logout)
	r.Post("/password/forgot", h.forgotPassword)
	r.Post("/password/reset", h.resetPassword)

}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSONLimit[registerInput](r, 8<<20)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	err = h.service.StartRegistration(r.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, ErrEmailTaken):
			writeError(w, http.StatusConflict, "email_taken", "An account with that email already exists.")
		default:
			writeError(w, http.StatusBadRequest, "register_failed", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": "Verification code has been sent.",
		"email":   normalizeEmail(input.Email),
	})
}

func (h *Handler) verifyRegistration(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[verifyRegistrationInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	user, pair, refreshToken, err := h.service.CompleteRegistration(
		r.Context(),
		input.Email,
		input.Token,
		MetadataFromRequest(r),
	)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidRegistrationToken):
			writeError(w, http.StatusBadRequest, "invalid_verification_token", "Verification code is invalid or expired.")
		case errors.Is(err, ErrEmailTaken):
			writeError(w, http.StatusConflict, "email_taken", "An account with that email already exists.")
		default:
			writeError(w, http.StatusBadRequest, "register_failed", err.Error())
		}
		return
	}

	user = h.signUserMedia(r.Context(), user)
	h.setSessionCookies(w, pair.AccessToken, refreshToken)
	writeJSON(w, http.StatusCreated, map[string]any{"user": user})
}

func (h *Handler) resendRegistrationVerification(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[resendRegistrationInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.service.ResendRegistrationVerification(r.Context(), input.Email); err != nil {
		if errors.Is(err, ErrInvalidRegistrationToken) {
			writeError(w, http.StatusBadRequest, "registration_not_pending", "Start signup again before requesting another code.")
			return
		}
		slog.Error("resend registration verification code failed", "error", err, "email", normalizeEmail(input.Email))
		writeError(w, http.StatusInternalServerError, "verification_failed", "Could not resend verification code.")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"message": "Verification code has been sent."})
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
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
			return
		}
		writeError(w, http.StatusBadRequest, "login_failed", err.Error())
		return
	}

	user = h.signUserMedia(r.Context(), user)
	h.setSessionCookies(w, pair.AccessToken, refreshToken)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
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

func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[forgotPasswordInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.service.SendPasswordReset(r.Context(), input.Email); err != nil {
		slog.Error("send password reset code failed", "error", err, "email", input.Email)
		writeError(w, http.StatusInternalServerError, "password_reset_failed", "Could not issue password reset code.")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": "If that email exists, a password reset code has been sent.",
	})
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[resetPasswordInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.service.ResetPassword(r.Context(), input.Token, input.NewPassword); err != nil {
		if errors.Is(err, ErrInvalidResetToken) {
			writeError(w, http.StatusBadRequest, "invalid_reset_token", "Reset token is invalid or expired.")
			return
		}
		writeError(w, http.StatusBadRequest, "password_reset_failed", err.Error())
		return
	}

	h.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"message": "Password updated successfully."})
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
		SameSite: http.SameSiteNoneMode,
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
