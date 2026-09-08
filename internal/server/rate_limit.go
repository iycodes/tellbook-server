package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/redisstore"
)

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

type requestLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*tokenBucket
	perMinute   int
	burst       int
	lastCleanup time.Time
	maxBuckets  int
}

type localBucketRequest struct {
	key              string
	perMinute, burst int
}

func newRequestLimiter(perMinute, burst int) *requestLimiter {
	return &requestLimiter{
		buckets:     make(map[string]*tokenBucket),
		perMinute:   perMinute,
		burst:       burst,
		lastCleanup: time.Now(),
		maxBuckets:  100_000,
	}
}

func (l *requestLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	return l.allowMany([]localBucketRequest{{key: key, perMinute: l.perMinute, burst: l.burst}}, now)
}

func (l *requestLimiter) allowMany(requests []localBucketRequest, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastCleanup) >= time.Minute {
		for bucketKey, bucket := range l.buckets {
			if now.Sub(bucket.lastSeen) > 10*time.Minute {
				delete(l.buckets, bucketKey)
			}
		}
		l.lastCleanup = now
	}

	missing := 0
	for _, request := range requests {
		if l.buckets[request.key] == nil {
			missing++
		}
	}
	if len(l.buckets)+missing > l.maxBuckets {
		return false, time.Minute
	}
	buckets := make([]*tokenBucket, 0, len(requests))
	retryAfter := time.Duration(0)
	for _, request := range requests {
		bucket := l.buckets[request.key]
		if bucket == nil {
			bucket = &tokenBucket{tokens: float64(request.burst), lastRefill: now}
			l.buckets[request.key] = bucket
		}
		refillPerSecond := float64(request.perMinute) / 60
		bucket.tokens = math.Min(float64(request.burst), bucket.tokens+now.Sub(bucket.lastRefill).Seconds()*refillPerSecond)
		bucket.lastRefill = now
		bucket.lastSeen = now
		buckets = append(buckets, bucket)
		if bucket.tokens < 1 {
			wait := time.Duration(math.Ceil((1-bucket.tokens)/refillPerSecond)) * time.Second
			if wait < time.Second {
				wait = time.Second
			}
			retryAfter = max(retryAfter, wait)
		}
	}
	if retryAfter > 0 {
		return false, retryAfter
	}
	for _, bucket := range buckets {
		bucket.tokens--
	}
	return true, 0
}

func rateLimitMiddleware(cfg config.Config, sharedLimiters ...SharedRateLimiter) func(http.Handler) http.Handler {
	general := newRequestLimiter(
		positiveOr(cfg.HTTPRateLimitPerMinute, 300),
		positiveOr(cfg.HTTPRateLimitBurst, 100),
	)
	ai := newRequestLimiter(
		positiveOr(cfg.AIRateLimitPerMinute, 12),
		positiveOr(cfg.AIRateLimitBurst, 4),
	)
	location := newRequestLimiter(
		positiveOr(cfg.LocationRateLimitPerMinute, 20),
		positiveOr(cfg.LocationRateLimitBurst, 5),
	)
	authLimiter := newRequestLimiter(
		positiveOr(cfg.MarketplaceAuthRateLimitPerMinute, 20),
		positiveOr(cfg.MarketplaceAuthRateLimitBurst, 6),
	)
	var shared SharedRateLimiter
	if len(sharedLimiters) > 0 {
		shared = sharedLimiters[0]
	}
	ipMultiplier := positiveOr(cfg.RateLimitIPCeilingMultiplier, 8)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rateLimitExempt(r) {
				next.ServeHTTP(w, r)
				return
			}

			limiter := general
			class := "general"
			if isAICommandRoute(r.Method, r.URL.Path) {
				limiter = ai
				class = "ai"
			} else if r.URL.Path == "/v1/public/locations/resolve" {
				limiter = location
				class = "location"
			} else if isAuthMutationRoute(r.Method, r.URL.Path) {
				limiter = authLimiter
				class = "auth"
			}

			clientIP := requestClientIP(r)
			identity := rateLimitIdentity(r, cfg)
			if identity == "" {
				identity = "ip:" + clientIP
			}
			allowed, retryAfter, limitErr := allowRequest(
				r.Context(), shared, limiter, class, identity, clientIP,
				limiter.perMinute, limiter.burst, ipMultiplier,
			)
			if limitErr != nil && class == "auth" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code":    "auth_temporarily_unavailable",
					"message": "Sign-in is temporarily unavailable. Please try again shortly.",
				})
				return
			}
			if !allowed {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code":    "rate_limited",
					"message": "Too many requests. Please wait and try again.",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func allowRequest(
	ctx context.Context,
	shared SharedRateLimiter,
	local *requestLimiter,
	class, identity, clientIP string,
	perMinute, burst, ipMultiplier int,
) (bool, time.Duration, error) {
	requests := []redisstore.TokenBucketRequest{{
		Scope:    "route_" + strings.ReplaceAll(class, "-", "_"),
		Identity: identity, PerMinute: perMinute, Burst: burst,
	}}
	localRequests := []localBucketRequest{{
		key: localRateLimitKey(class, identity), perMinute: perMinute, burst: burst,
	}}
	if identity != "ip:"+clientIP {
		requests = append(requests, redisstore.TokenBucketRequest{
			Scope:    "route_" + strings.ReplaceAll(class, "-", "_") + "_ip",
			Identity: clientIP, PerMinute: perMinute * ipMultiplier, Burst: burst * ipMultiplier,
		})
		localRequests = append(localRequests, localBucketRequest{
			key:       localRateLimitKey(class+":ip", clientIP),
			perMinute: perMinute * ipMultiplier, burst: burst * ipMultiplier,
		})
	}
	if shared == nil {
		allowed, retryAfter := local.allowMany(localRequests, time.Now())
		return allowed, retryAfter, nil
	}
	allowed, retryAfter, err := shared.AllowTokenBuckets(ctx, requests...)
	if err != nil {
		fallbackAllowed, fallbackRetry := local.allowMany(localRequests, time.Now())
		return fallbackAllowed, fallbackRetry, err
	}
	return allowed, retryAfter, nil
}

func localRateLimitKey(class, identity string) string {
	digest := sha256.Sum256([]byte(identity))
	return class + ":" + hex.EncodeToString(digest[:16])
}

func rateLimitIdentity(r *http.Request, cfg config.Config) string {
	if isAuthMutationRoute(r.Method, r.URL.Path) {
		if identity := marketplaceAuthRateLimitIdentity(r); identity != "" {
			return identity
		}
	}
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" && len(authorization) <= 4096 {
		return "credential:" + authorization
	}
	for _, cookieName := range []string{cfg.AuthAccessCookieName, "tellbook_marketplace_session"} {
		if cookieName == "" {
			continue
		}
		if cookie, err := r.Cookie(cookieName); err == nil && cookie.Value != "" && len(cookie.Value) <= 4096 {
			return "credential:" + cookie.Value
		}
	}
	return ""
}

func marketplaceAuthRateLimitIdentity(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	const maximumIdentityBody = 32 * 1024
	originalBody := r.Body
	payload, err := io.ReadAll(io.LimitReader(originalBody, maximumIdentityBody+1))
	if err != nil {
		r.Body = &multiReadCloser{Reader: io.MultiReader(bytes.NewReader(payload), originalBody), Closer: originalBody}
		return ""
	}
	if len(payload) > maximumIdentityBody {
		r.Body = &multiReadCloser{Reader: io.MultiReader(bytes.NewReader(payload), originalBody), Closer: originalBody}
		return ""
	}
	_ = originalBody.Close()
	r.Body = io.NopCloser(bytes.NewReader(payload))
	var input struct {
		Identifier  string `json:"identifier"`
		ChallengeID string `json:"challenge_id"`
	}
	if json.Unmarshal(payload, &input) != nil {
		return ""
	}
	if identifier := normalizeRateLimitIdentifier(input.Identifier); identifier != "" {
		return "identifier:" + identifier
	}
	if challengeID := strings.ToLower(strings.TrimSpace(input.ChallengeID)); challengeID != "" && len(challengeID) <= 128 {
		return "challenge:" + challengeID
	}
	return ""
}

type multiReadCloser struct {
	io.Reader
	io.Closer
}

func normalizeRateLimitIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 320 {
		return ""
	}
	if strings.Contains(value, "@") {
		return strings.ToLower(value)
	}
	digits := strings.Builder{}
	for _, character := range value {
		if character >= '0' && character <= '9' {
			digits.WriteRune(character)
		}
	}
	phone := digits.String()
	phone = strings.TrimPrefix(phone, "00")
	if strings.HasPrefix(phone, "0") {
		phone = "234" + strings.TrimPrefix(phone, "0")
	}
	if len(phone) >= 10 && len(phone) <= 15 {
		return "+" + phone
	}
	return strings.ToLower(value)
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func requestClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if value := strings.TrimSpace(r.RemoteAddr); value != "" {
		return value
	}
	return "unknown"
}

func rateLimitExempt(r *http.Request) bool {
	if r.Method == http.MethodOptions || r.URL.Path == "/v1/healthz" ||
		strings.HasPrefix(r.URL.Path, "/v1/webhooks/") {
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/v1/public/payments/") &&
		strings.HasSuffix(r.URL.Path, "/events")
}

func isAIRoute(method, path string) bool {
	return strings.HasPrefix(path, "/v1/app/ai/") ||
		(method == http.MethodPost && strings.HasPrefix(path, "/v1/app/inbox/conversations/") && strings.HasSuffix(path, "/ai-drafts")) ||
		(method == http.MethodPost && strings.HasPrefix(path, "/v1/app/agreement-templates/generation-jobs"))
}

func isAICommandRoute(method, path string) bool {
	if isAIRoute(method, path) {
		return true
	}
	return method == http.MethodPost && strings.HasPrefix(path, "/v1/app/tessa/threads/") &&
		strings.HasSuffix(path, "/messages")
}

func isAuthMutationRoute(method, path string) bool {
	if method != http.MethodPost && method != http.MethodPatch {
		return false
	}
	return path == "/v1/auth/code" ||
		path == "/v1/app/profile/customer-contact/verification" ||
		path == "/v1/auth/code/resend" ||
		path == "/v1/auth/verify" ||
		path == "/v1/auth/password" ||
		strings.HasPrefix(path, "/v1/auth/password/reset") ||
		strings.HasPrefix(path, "/v1/app/me/identities/") ||
		path == "/v1/app/me/password" ||
		path == "/v1/marketplace/auth/code" ||
		path == "/v1/marketplace/auth/code/resend" ||
		path == "/v1/marketplace/auth/verify" ||
		path == "/v1/marketplace/auth/password" ||
		strings.HasPrefix(path, "/v1/marketplace/auth/password/reset") ||
		path == "/v1/marketplace/me/password" ||
		strings.HasPrefix(path, "/v1/marketplace/me/identities/")
}
