package auth

import (
	"net/http/httptest"
	"testing"
	"time"

	"booking/go-server/internal/config"
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
