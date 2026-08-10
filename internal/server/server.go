package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/config"
	"booking/go-server/internal/markets"
	"booking/go-server/internal/payments"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func New(cfg config.Config, logger *slog.Logger, authHandler *auth.Handler, providerWebhookHandler *payments.ProviderWebhookHandler, appdataHandler *appdata.Handler) *http.Server {
	router := chi.NewRouter()
	writeTimeout := cfg.WriteTimeout
	aiRouteTimeout := cfg.SynchronousAIRouteTimeout()
	if aiRouteTimeout >= writeTimeout {
		writeTimeout = aiRouteTimeout + (5 * time.Second)
	}

	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.RealIP)
	router.Use(chimiddleware.Recoverer)
	router.Use(rateLimitMiddleware(cfg))
	router.Use(routeTimeoutMiddleware(cfg))
	router.Use(corsMiddleware(cfg.CORSOrigins))
	router.Use(requestLoggingMiddleware(logger))

	router.Route("/v1", func(r chi.Router) {
		r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "ok",
				"time":   time.Now().UTC(),
			})
		})
		r.Get("/meta/markets", markets.Handler(markets.DefaultCatalog()))

		if authHandler != nil {
			r.Route("/auth", authHandler.Routes)
		}
		if providerWebhookHandler != nil {
			r.Route("/webhooks", providerWebhookHandler.Routes)
		}
		if appdataHandler != nil {
			appdataHandler.Routes(r)
		}
	})

	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return context.Background()
		},
	}
}

func requestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			startedAt := time.Now()
			requestID := chimiddleware.GetReqID(r.Context())

			if strings.HasPrefix(r.URL.Path, "/v1/webhooks") {
				logger.Info(
					"provider webhook request received",
					"method", r.Method,
					"path", r.URL.Path,
					"content_type", truncatedLogValue(r.Header.Get("Content-Type"), 128),
					"content_length", r.ContentLength,
					"user_agent", truncatedLogValue(r.UserAgent(), 256),
					"cf_ray", truncatedLogValue(r.Header.Get("CF-Ray"), 128),
					"request_id", requestID,
				)
			}

			next.ServeHTTP(ww, r)

			logger.Info(
				"http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration", time.Since(startedAt),
				"request_id", requestID,
			)
		})
	}
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
			if r.URL.Path == "/v1/app/inbox/ws" ||
				(strings.HasPrefix(r.URL.Path, "/v1/app/inbox/conversations/") && strings.HasSuffix(r.URL.Path, "/ws")) ||
				(strings.HasPrefix(r.URL.Path, "/v1/public/payments/") && strings.HasSuffix(r.URL.Path, "/events")) {
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
