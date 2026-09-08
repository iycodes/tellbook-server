package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/redisstore"
)

type sharedLimitCall struct {
	scope, identity  string
	perMinute, burst int
}

type fakeSharedLimiter struct {
	allowed bool
	err     error
	calls   []sharedLimitCall
}

func (limiter *fakeSharedLimiter) AllowTokenBuckets(
	_ context.Context, requests ...redisstore.TokenBucketRequest,
) (bool, time.Duration, error) {
	for _, request := range requests {
		limiter.calls = append(limiter.calls, sharedLimitCall{
			scope: request.Scope, identity: request.Identity,
			perMinute: request.PerMinute, burst: request.Burst,
		})
	}
	return limiter.allowed, time.Second, limiter.err
}

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

func TestRequestLimiterBoundsFallbackIdentityMemory(t *testing.T) {
	limiter := newRequestLimiter(60, 1)
	limiter.maxBuckets = 1
	now := time.Now()
	if allowed, _ := limiter.allow("first", now); !allowed {
		t.Fatal("first identity should fit")
	}
	if allowed, retryAfter := limiter.allow("second", now); allowed || retryAfter != time.Minute {
		t.Fatalf("overflow identity = %v, %v", allowed, retryAfter)
	}
}

func TestRequestLimiterDoesNotPartiallyConsumePairedBuckets(t *testing.T) {
	limiter := newRequestLimiter(60, 1)
	now := time.Now()
	primary := localBucketRequest{key: "primary", perMinute: 1, burst: 1}
	secondary := localBucketRequest{key: "secondary", perMinute: 1, burst: 1}
	if allowed, _ := limiter.allowMany([]localBucketRequest{secondary}, now); !allowed {
		t.Fatal("secondary setup was denied")
	}
	if allowed, _ := limiter.allowMany([]localBucketRequest{primary, secondary}, now); allowed {
		t.Fatal("paired request should be denied by the exhausted secondary bucket")
	}
	if allowed, _ := limiter.allowMany([]localBucketRequest{primary}, now); !allowed {
		t.Fatal("denied pair partially consumed the primary bucket")
	}
}

func TestProviderInboxAIDraftUsesAIRouteControls(t *testing.T) {
	path := "/v1/app/inbox/conversations/00000000-0000-4000-8000-000000000001/ai-drafts"
	if !isAIRoute(http.MethodPost, path) {
		t.Fatal("provider inbox AI draft route was not classified as AI")
	}
	if isAIRoute(http.MethodGet, path) {
		t.Fatal("non-POST inbox route was classified as AI")
	}
}

func TestTessaMessageUsesAICommandRateLimitWithoutLongRequestTimeout(t *testing.T) {
	path := "/v1/app/tessa/threads/00000000-0000-4000-8000-000000000001/messages"
	if !isAICommandRoute(http.MethodPost, path) {
		t.Fatal("Tessa message command was not classified for the AI rate limit")
	}
	if isAIRoute(http.MethodPost, path) {
		t.Fatal("Tessa enqueue command incorrectly inherited the synchronous AI timeout")
	}
	if isAICommandRoute(http.MethodGet, path) {
		t.Fatal("non-POST Tessa route was classified as an AI command")
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

func TestRateLimitMiddlewareUsesMarketplaceAuthBucket(t *testing.T) {
	cfg := config.Config{
		HTTPRateLimitPerMinute:            600,
		HTTPRateLimitBurst:                10,
		MarketplaceAuthRateLimitPerMinute: 60,
		MarketplaceAuthRateLimitBurst:     1,
	}
	handler := rateLimitMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for index, expected := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/marketplace/auth/password", nil)
		request.Header.Set("X-Forwarded-For", "198.51.100.20")
		request.RemoteAddr = "127.0.0.1:4000"
		handler.ServeHTTP(recorder, request)
		if recorder.Code != expected {
			t.Fatalf("request %d status = %d, want %d", index+1, recorder.Code, expected)
		}
	}
}

func TestSharedAuthLimitUsesNormalizedIdentifierAndSecondaryIP(t *testing.T) {
	cfg := config.Config{
		AuthAccessCookieName:   "booking_access",
		HTTPRateLimitPerMinute: 600, HTTPRateLimitBurst: 10,
		MarketplaceAuthRateLimitPerMinute: 20, MarketplaceAuthRateLimitBurst: 6,
		RateLimitIPCeilingMultiplier: 8,
	}
	shared := &fakeSharedLimiter{allowed: true}
	handler := rateLimitMiddleware(cfg, shared)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil || string(payload) != `{"identifier":"0802 123 4567","password":"secret-password"}` {
			t.Fatalf("request body was not preserved: %q, %v", payload, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/marketplace/auth/password",
		strings.NewReader(`{"identifier":"0802 123 4567","password":"secret-password"}`),
	)
	request.Header.Set("Authorization", "Bearer attacker-controlled-bypass")
	request.RemoteAddr = "192.0.2.12:4000"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if len(shared.calls) != 2 {
		t.Fatalf("shared calls = %d, want actor and IP", len(shared.calls))
	}
	if shared.calls[0].scope != "route_auth" ||
		shared.calls[0].identity != "identifier:+2348021234567" {
		t.Fatalf("unexpected primary limit: %+v", shared.calls[0])
	}
	if shared.calls[1].scope != "route_auth_ip" ||
		shared.calls[1].identity != "192.0.2.12" ||
		shared.calls[1].perMinute != 160 || shared.calls[1].burst != 48 {
		t.Fatalf("unexpected IP ceiling: %+v", shared.calls[1])
	}
}

func TestRateLimitIdentifierRejectsUnboundedInput(t *testing.T) {
	if normalized := normalizeRateLimitIdentifier(strings.Repeat("x", 321)); normalized != "" {
		t.Fatalf("oversized identifier normalized to %q", normalized)
	}
}

func TestA5AuthMutationsUseAuthLimiter(t *testing.T) {
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/auth/password"},
		{http.MethodPost, "/v1/auth/password/reset/code"},
		{http.MethodPost, "/v1/auth/password/reset/verify"},
		{http.MethodPost, "/v1/auth/password/reset"},
		{http.MethodPost, "/v1/app/me/identities/code"},
		{http.MethodPatch, "/v1/app/me/password"},
		{http.MethodPost, "/v1/marketplace/auth/password/reset/code"},
		{http.MethodPost, "/v1/marketplace/auth/password/reset/verify"},
		{http.MethodPost, "/v1/marketplace/auth/password/reset"},
		{http.MethodPatch, "/v1/marketplace/me/password"},
	} {
		if !isAuthMutationRoute(route.method, route.path) {
			t.Fatalf("%s %s was not classified as auth", route.method, route.path)
		}
	}
}

func TestSharedAuthLimitFailsClosedButGeneralRouteDegradesLocally(t *testing.T) {
	cfg := config.Config{
		HTTPRateLimitPerMinute: 60, HTTPRateLimitBurst: 1,
		MarketplaceAuthRateLimitPerMinute: 20, MarketplaceAuthRateLimitBurst: 6,
	}
	shared := &fakeSharedLimiter{allowed: false, err: errors.New("Redis unavailable")}
	handler := rateLimitMiddleware(cfg, shared)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	authRecorder := httptest.NewRecorder()
	authRequest := httptest.NewRequest(http.MethodPost, "/v1/marketplace/auth/code", strings.NewReader(`{"identifier":"person@example.com"}`))
	authRequest.RemoteAddr = "192.0.2.20:4000"
	handler.ServeHTTP(authRecorder, authRequest)
	if authRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth status = %d, want 503", authRecorder.Code)
	}

	for index, expected := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/public/marketplace/home", nil)
		request.RemoteAddr = "192.0.2.21:4000"
		handler.ServeHTTP(recorder, request)
		if recorder.Code != expected {
			t.Fatalf("general request %d status = %d, want %d", index+1, recorder.Code, expected)
		}
	}
}
