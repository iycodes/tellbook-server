package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"

	"github.com/go-chi/chi/v5"
)

func TestSessionCookiesUseConfiguredParentDomain(t *testing.T) {
	handler := &Handler{cfg: config.Config{
		AuthAccessCookieName:  "tellbook_access",
		AuthRefreshCookieName: "tellbook_refresh",
		AuthCookieDomain:      "tellbook.app",
		AuthCookieSecure:      true,
		AuthAccessTokenTTL:    15 * time.Minute,
		AuthRefreshTokenTTL:   30 * 24 * time.Hour,
	}}
	recorder := httptest.NewRecorder()

	handler.setSessionCookies(recorder, "access-token", "refresh-token")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookie count = %d, want 2", len(cookies))
	}
	for _, cookie := range cookies {
		if cookie.Domain != "tellbook.app" {
			t.Fatalf("cookie %q domain = %q", cookie.Name, cookie.Domain)
		}
		if !cookie.Secure || !cookie.HttpOnly {
			t.Fatalf("cookie %q must be Secure and HttpOnly", cookie.Name)
		}
	}
}

func TestLegacyProviderAuthRoutesAreNotRegistered(t *testing.T) {
	handler := &Handler{}
	router := chi.NewRouter()
	router.Route("/v1/auth", handler.Routes)

	for _, path := range []string{
		"/v1/auth/register",
		"/v1/auth/register/verify",
		"/v1/auth/register/resend",
		"/v1/auth/login",
		"/v1/auth/password/forgot",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want 404", path, recorder.Code)
		}
	}
}
