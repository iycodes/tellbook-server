package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/config"
	"booking/go-server/internal/markets"
	"booking/go-server/internal/observability"
	"booking/go-server/internal/payments"
	"booking/go-server/internal/redisstore"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type ReadinessChecker interface {
	Ping(context.Context) error
}

type SharedRateLimiter interface {
	AllowTokenBuckets(context.Context, ...redisstore.TokenBucketRequest) (bool, time.Duration, error)
}

type OperationalDependencies struct {
	Readiness            ReadinessChecker
	RedisReadiness       ReadinessChecker
	SharedRateLimiter    SharedRateLimiter
	Metrics              *observability.Metrics
	Role                 string
	ConfigurationReady   bool
	WorkersReady         bool
	MaintenanceOwnership string
	MetaWhatsAppWebhook  http.Handler
}

func New(
	cfg config.Config,
	logger *slog.Logger,
	authHandler *auth.Handler,
	providerWebhookHandler *payments.ProviderWebhookHandler,
	appdataHandler *appdata.Handler,
	operational OperationalDependencies,
) *http.Server {
	router := chi.NewRouter()
	writeTimeout := cfg.WriteTimeout
	aiRouteTimeout := cfg.SynchronousAIRouteTimeout()
	if aiRouteTimeout >= writeTimeout {
		writeTimeout = aiRouteTimeout + (5 * time.Second)
	}

	router.Use(chimiddleware.RequestID)
	router.Use(trustedRealIPMiddleware(cfg.TrustedProxyCIDRs))
	router.Use(requestLoggingMiddleware(
		logger,
		operational.Metrics,
		cfg.HTTPSuccessLogSampleRate,
		cfg.HTTPSlowRequestThreshold,
	))
	router.Use(chimiddleware.Recoverer)
	router.Use(rateLimitMiddleware(cfg, operational.SharedRateLimiter))
	router.Use(routeTimeoutMiddleware(cfg))
	router.Use(corsMiddleware(cfg.CORSOrigins))

	if operational.Metrics != nil {
		router.Handle(
			"/internal/metrics",
			metricsAccessMiddleware(cfg.MetricsAuthToken, operational.Metrics.Handler()),
		)
	}

	router.Route("/v1", func(r chi.Router) {
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})
		r.Get("/readyz", readinessHandler(operational))
		r.Get("/meta/markets", markets.Handler(markets.DefaultCatalog()))

		if authHandler != nil {
			r.Route("/auth", authHandler.Routes)
		}
		if providerWebhookHandler != nil {
			r.Route("/webhooks", providerWebhookHandler.Routes)
		}
		if operational.MetaWhatsAppWebhook != nil {
			r.Handle("/webhooks/meta/whatsapp", operational.MetaWhatsAppWebhook)
		}
		if appdataHandler != nil {
			appdataHandler.Routes(r)
		}
	})

	baseContext, cancelBaseContext := context.WithCancel(context.Background())
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return baseContext
		},
	}
	httpServer.RegisterOnShutdown(cancelBaseContext)
	return httpServer
}

func requestLoggingMiddleware(
	logger *slog.Logger,
	metrics *observability.Metrics,
	successSampleRate float64,
	slowRequestThreshold time.Duration,
) func(http.Handler) http.Handler {
	if successSampleRate < 0 || successSampleRate > 1 {
		successSampleRate = 0.1
	}
	if slowRequestThreshold <= 0 {
		slowRequestThreshold = 750 * time.Millisecond
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			startedAt := time.Now()
			requestID := chimiddleware.GetReqID(r.Context())

			if strings.HasPrefix(r.URL.Path, "/v1/webhooks") {
				webhookRoute := "/v1/webhooks/{provider}"
				if r.URL.Path == "/v1/webhooks/meta/whatsapp" {
					webhookRoute = "/v1/webhooks/meta/whatsapp"
				}
				logger.Info(
					"provider webhook request received",
					"method", r.Method,
					"route", webhookRoute,
					"content_type", truncatedLogValue(r.Header.Get("Content-Type"), 128),
					"content_length", r.ContentLength,
					"user_agent", truncatedLogValue(r.UserAgent(), 256),
					"cf_ray", truncatedLogValue(r.Header.Get("CF-Ray"), 128),
					"request_id", requestID,
				)
			}

			next.ServeHTTP(ww, r)

			duration := time.Since(startedAt)
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			route := normalizedRoute(r)
			if metrics != nil {
				metrics.ObserveHTTPRequest(route, r.Method, status, ww.BytesWritten(), duration)
			}
			eventStream := strings.HasPrefix(
				strings.ToLower(strings.TrimSpace(ww.Header().Get("Content-Type"))),
				"text/event-stream",
			)
			shouldLog := status >= http.StatusBadRequest ||
				(!eventStream && (duration >= slowRequestThreshold || rand.Float64() < successSampleRate))
			if shouldLog {
				logger.Info(
					"http request",
					"method", r.Method,
					"route", route,
					"status", status,
					"bytes", ww.BytesWritten(),
					"duration", duration,
					"request_id", requestID,
				)
			}
		})
	}
}

func normalizedRoute(r *http.Request) string {
	route := strings.TrimSpace(chi.RouteContext(r.Context()).RoutePattern())
	if route == "" {
		return "unmatched"
	}
	return route
}

func readinessHandler(operational OperationalDependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{}
		degraded := false
		if operational.Readiness == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready",
				"checks": map[string]string{"database": "not_configured"},
			})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := operational.Readiness.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready",
				"checks": map[string]string{"database": "unavailable"},
			})
			return
		}
		checks["database"] = "ok"
		if operational.RedisReadiness != nil {
			if err := operational.RedisReadiness.Ping(ctx); err != nil {
				checks["redis"] = "degraded"
				degraded = true
			} else {
				checks["redis"] = "ok"
			}
		} else {
			checks["redis"] = "disabled"
		}
		if operational.Role != "" {
			checks["role"] = operational.Role
			if !operational.ConfigurationReady {
				checks["configuration"] = "not_ready"
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks})
				return
			}
			checks["configuration"] = "ok"
			if !operational.WorkersReady {
				checks["workers"] = "not_ready"
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks})
				return
			}
			checks["workers"] = "initialized"
			checks["maintenance_ownership"] = operational.MaintenanceOwnership
		}
		status := "ready"
		if degraded {
			status = "degraded"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": status,
			"checks": checks,
		})
	}
}

func metricsAccessMiddleware(token string, next http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.NotFound(w, r)
			return
		}
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		provided := ""
		if strings.HasPrefix(authorization, "Bearer ") {
			provided = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		}
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tellbook-metrics"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"code":    "metrics_unauthorized",
				"message": "A valid metrics token is required.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func truncatedLogValue(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength]
}

func routeTimeoutMiddleware(cfg config.Config) func(http.Handler) http.Handler {
	defaultTimeout := chimiddleware.Timeout(30 * time.Second)
	aiRouteTimeout := cfg.SynchronousAIRouteTimeout()
	aiTimeout := chimiddleware.Timeout(aiRouteTimeout)

	return func(next http.Handler) http.Handler {
		defaultHandler := defaultTimeout(next)
		aiHandler := aiTimeout(next)

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v1/public/payments/") && strings.HasSuffix(r.URL.Path, "/events") {
				next.ServeHTTP(w, r)
				return
			}
			if isInboxEventStream(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if isAIRoute(r.Method, r.URL.Path) {
				aiHandler.ServeHTTP(w, r)
				return
			}
			defaultHandler.ServeHTTP(w, r)
		})
	}
}

func isInboxEventStream(method string, path string) bool {
	if method != http.MethodGet {
		return false
	}
	return path == "/v1/app/inbox/events" ||
		path == "/v1/app/bookings/events" ||
		path == "/v1/marketplace/conversations/events" ||
		path == "/v1/app/tessa/events"
}

func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := len(allowedOrigins) == 0 || (len(allowedOrigins) == 1 && allowedOrigins[0] == "*")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" && (allowAll || slices.Contains(allowedOrigins, origin)) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}

			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
