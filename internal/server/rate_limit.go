package server

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/config"
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
}

func newRequestLimiter(perMinute, burst int) *requestLimiter {
	return &requestLimiter{
		buckets:     make(map[string]*tokenBucket),
		perMinute:   perMinute,
		burst:       burst,
		lastCleanup: time.Now(),
	}
}

func (l *requestLimiter) allow(key string, now time.Time) (bool, time.Duration) {
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

	bucket := l.buckets[key]
	if bucket == nil {
		bucket = &tokenBucket{tokens: float64(l.burst), lastRefill: now}
		l.buckets[key] = bucket
	}

	refillPerSecond := float64(l.perMinute) / 60
	bucket.tokens = math.Min(float64(l.burst), bucket.tokens+now.Sub(bucket.lastRefill).Seconds()*refillPerSecond)
	bucket.lastRefill = now
	bucket.lastSeen = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}

	wait := time.Duration(math.Ceil((1-bucket.tokens)/refillPerSecond)) * time.Second
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

func rateLimitMiddleware(cfg config.Config) func(http.Handler) http.Handler {
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

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rateLimitExempt(r) {
				next.ServeHTTP(w, r)
				return
			}

			limiter := general
			class := "general"
			if isAIRoute(r.Method, r.URL.Path) {
				limiter = ai
				class = "ai"
			} else if r.URL.Path == "/v1/public/locations/resolve" {
				limiter = location
				class = "location"
			}

			allowed, retryAfter := limiter.allow(class+":"+requestClientIP(r), time.Now())
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
	return r.URL.Path == "/v1/app/inbox/ws" ||
		(strings.HasPrefix(r.URL.Path, "/v1/app/inbox/conversations/") && strings.HasSuffix(r.URL.Path, "/ws")) ||
		(strings.HasPrefix(r.URL.Path, "/v1/public/payments/") && strings.HasSuffix(r.URL.Path, "/events"))
}

func isAIRoute(method, path string) bool {
	return strings.HasPrefix(path, "/v1/app/ai/") ||
		(method == http.MethodPost && strings.HasPrefix(path, "/v1/app/agreement-templates/generation-jobs")) ||
		(strings.HasPrefix(path, "/v1/app/inbox/conversations/") && strings.HasSuffix(path, "/suggest-reply"))
}
