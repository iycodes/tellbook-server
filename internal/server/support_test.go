package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"
)

type supportSender struct{ sends int }

func (s *supportSender) Enabled() bool                                  { return true }
func (s *supportSender) Send(_ context.Context, _ mailer.Message) error { s.sends++; return nil }

func TestPublicSupportIgnoresSessionAndLimitsTrustedClientIP(t *testing.T) {
	var logs strings.Builder
	sender := &supportSender{}
	httpServer := New(config.Config{
		SupportEmail: "support@example.com", SMTPUsername: "hello@example.com",
		ClientPublicBaseURL: "https://app.tellbook.test", TrustedProxyCIDRs: []string{"127.0.0.1/32"},
	}, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, nil, OperationalDependencies{SupportSender: sender})
	for index := range 4 {
		request := httptest.NewRequest(http.MethodPost, "/v1/support/requests", strings.NewReader(`{"name":"Sensitive Name","email":"pii@example.com","topic":"privacy","subject":"My information","message":"Sensitive private support request text."}`))
		request.RemoteAddr = "127.0.0.1:1234"
		request.Header.Set("X-Forwarded-For", "192.0.2.10")
		request.Header.Set("Origin", "https://app.tellbook.test")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", strings.Repeat("changed credential ", index+1))
		response := httptest.NewRecorder()
		httpServer.Handler.ServeHTTP(response, request)
		if index < 3 && response.Code != http.StatusOK {
			t.Fatalf("anonymous request=%d status=%d body=%s", index, response.Code, response.Body.String())
		}
		if index == 3 && (response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "") {
			t.Fatalf("limit status=%d", response.Code)
		}
	}
	if sender.sends != 3 {
		t.Fatalf("sends=%d", sender.sends)
	}
	for _, pii := range []string{"Sensitive Name", "pii@example.com", "Sensitive private"} {
		if strings.Contains(logs.String(), pii) {
			t.Fatal("PII in request logs")
		}
	}
}

func TestUnconfiguredSupportStillHasPublicAvailabilityEndpoint(t *testing.T) {
	httpServer := New(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil, OperationalDependencies{})
	response := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/support", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"available":false`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSupportSharedLimiterFailureDoesNotSend(t *testing.T) {
	sender := &supportSender{}
	shared := &fakeSharedLimiter{allowed: true, err: errors.New("Redis unavailable")}
	httpServer := New(config.Config{
		SupportEmail: "support@example.com", SMTPFromEmail: "hello@example.com", ClientPublicBaseURL: "https://app.tellbook.test",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil, OperationalDependencies{SupportSender: sender, SharedRateLimiter: shared})
	request := httptest.NewRequest(http.MethodPost, "/v1/support/requests", strings.NewReader(`{"name":"Ada","email":"ada@example.com","topic":"other","subject":"Question","message":"Please help with this booking."}`))
	request.Header.Set("Origin", "https://app.tellbook.test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || sender.sends != 0 {
		t.Fatalf("status=%d sends=%d", response.Code, sender.sends)
	}
	if len(shared.calls) != 1 || shared.calls[0].scope != "route_support" || !strings.HasPrefix(shared.calls[0].identity, "ip:") {
		t.Fatalf("shared limits=%+v", shared.calls)
	}
}

func TestSupportCannotSpoofIPFromUntrustedPeer(t *testing.T) {
	sender := &supportSender{}
	httpServer := New(config.Config{
		SupportEmail: "support@example.com", SMTPFromEmail: "hello@example.com", ClientPublicBaseURL: "https://app.tellbook.test", TrustedProxyCIDRs: []string{"127.0.0.1/32"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil, OperationalDependencies{SupportSender: sender})
	for index := range 4 {
		request := httptest.NewRequest(http.MethodPost, "/v1/support/requests", strings.NewReader(`{"name":"Ada","email":"ada@example.com","topic":"other","subject":"Question","message":"Please help with this booking."}`))
		request.RemoteAddr = "198.51.100.10:1234"
		request.Header.Set("X-Forwarded-For", strings.Repeat("192.0.2.1,", index+1))
		request.Header.Set("Origin", "https://app.tellbook.test")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		httpServer.Handler.ServeHTTP(response, request)
		if index == 3 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("spoof bypassed limit status=%d", response.Code)
		}
	}
	if sender.sends != 3 {
		t.Fatalf("sends=%d", sender.sends)
	}
}
