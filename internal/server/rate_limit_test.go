package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"booking/go-server/internal/config"
)

func TestRequestLimiterRefillsTokens(t *testing.T) {
	limiter := newRequestLimiter(60, 1)
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	if allowed, _ := limiter.allow("client", now); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _ := limiter.allow("client", now); allowed {
		t.Fatal("burst should be exhausted")
	}
	if allowed, _ := limiter.allow("client", now.Add(time.Second)); !allowed {
		t.Fatal("one token should refill after one second")
	}
}

func TestRateLimitMiddlewareUsesStricterLocationBucket(t *testing.T) {
	cfg := config.Config{
		HTTPRateLimitPerMinute:     600,
		HTTPRateLimitBurst:         10,
		AIRateLimitPerMinute:       60,
		AIRateLimitBurst:           2,
		LocationRateLimitPerMinute: 60,
		LocationRateLimitBurst:     1,
	}
	handler := rateLimitMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodPost, "/v1/public/locations/resolve", nil)
	firstRequest.RemoteAddr = "192.0.2.10:4000"
	handler.ServeHTTP(first, firstRequest)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusNoContent)
	}

	second := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodPost, "/v1/public/locations/resolve", nil)
	secondRequest.RemoteAddr = "192.0.2.10:4001"
	handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header is required")
	}
}
