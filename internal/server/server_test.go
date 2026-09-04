package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/observability"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type readinessCheckerFunc func(context.Context) error

func (check readinessCheckerFunc) Ping(ctx context.Context) error { return check(ctx) }

func TestMarketsEndpointIsPublic(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	httpServer := New(config.Config{}, logger, nil, nil, nil, OperationalDependencies{})

	request := httptest.NewRequest(http.MethodGet, "/v1/meta/markets", nil)
	recorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if recorder.Header().Get("ETag") == "" {
		t.Fatal("ETag header is missing")
	}
	if recorder.Header().Get("Cache-Control") == "no-store" {
		t.Fatal("markets endpoint inherited the global no-store cache policy")
	}
}

func TestMetaWhatsAppWebhookIsMountedOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	httpServer := New(
		config.Config{}, logger, nil, nil, nil,
		OperationalDependencies{MetaWhatsAppWebhook: handler},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/webhooks/meta/whatsapp", nil)
	response := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("configured webhook status = %d", response.Code)
	}

	httpServer = New(config.Config{}, logger, nil, nil, nil, OperationalDependencies{})
	request = httptest.NewRequest(http.MethodPost, "/v1/webhooks/meta/whatsapp", nil)
	response = httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured webhook status = %d", response.Code)
	}
}

func TestWebhookIngressLogContainsSafeRequestMetadata(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	httpServer := New(config.Config{}, logger, nil, nil, nil, OperationalDependencies{})

	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/webhooks/payaza",
		strings.NewReader(`{"secret_body_marker":"must-not-be-logged"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Payaza-Webhook-Test")
	request.Header.Set("CF-Ray", "test-ray")
	request.Header.Set("X-Payaza-Signature", "must-not-be-logged")
	recorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(recorder, request)

	logs := output.String()
	for _, expected := range []string{
		"provider webhook request received",
		"method=POST",
		"route=/v1/webhooks/{provider}",
		"content_type=application/json",
		"user_agent=Payaza-Webhook-Test",
		"cf_ray=test-ray",
	} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("logs do not contain %q: %s", expected, logs)
		}
	}
	for _, sensitive := range []string{"secret_body_marker", "must-not-be-logged", "X-Payaza-Signature"} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("logs contain sensitive value %q: %s", sensitive, logs)
		}
	}
}

func TestRequestLogsUseNormalizedRoutesWithoutPublicTokens(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	httpServer := New(
		config.Config{HTTPSuccessLogSampleRate: 1},
		logger,
		nil,
		nil,
		nil,
		OperationalDependencies{},
	)

	request := httptest.NewRequest(http.MethodGet, "/v1/public/bookings/private-booking-token", nil)
	recorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(recorder, request)

	logs := output.String()
	if !strings.Contains(logs, "route=/v1/*") {
		t.Fatalf("logs do not contain the normalized unmatched route: %s", logs)
	}
	if strings.Contains(logs, "private-booking-token") {
		t.Fatalf("logs contain a public token: %s", logs)
	}
}

func TestReadinessChecksDatabase(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		name       string
		check      readinessCheckerFunc
		wantStatus int
	}{
		{name: "ready", check: func(context.Context) error { return nil }, wantStatus: http.StatusOK},
		{name: "database unavailable", check: func(context.Context) error { return errors.New("offline") }, wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			httpServer := New(config.Config{}, logger, nil, nil, nil, OperationalDependencies{Readiness: test.check})
			request := httptest.NewRequest(http.MethodGet, "/v1/readyz", nil)
			recorder := httptest.NewRecorder()
			httpServer.Handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestReadinessReportsMonolithStartupState(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	operational := OperationalDependencies{
		Readiness: readinessCheckerFunc(func(context.Context) error { return nil }),
		Role:      "monolith", ConfigurationReady: true, WorkersReady: true,
		MaintenanceOwnership: "embedded_monolith",
	}
	httpServer := New(config.Config{}, logger, nil, nil, nil, operational)
	recorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	for _, want := range []string{`"role":"monolith"`, `"workers":"initialized"`, `"maintenance_ownership":"embedded_monolith"`} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("readiness body does not contain %s: %s", want, recorder.Body.String())
		}
	}
}

func TestReadinessReportsRedisDegradationWithoutRemovingTraffic(t *testing.T) {
	t.Parallel()
	httpServer := New(
		config.Config{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil, nil, nil,
		OperationalDependencies{
			Readiness: readinessCheckerFunc(func(context.Context) error { return nil }),
			RedisReadiness: readinessCheckerFunc(func(context.Context) error {
				return errors.New("Redis unavailable")
			}),
		},
	)
	recorder := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"redis":"degraded"`) {
		t.Fatalf("readiness body does not report Redis degradation: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"status":"degraded"`) {
		t.Fatalf("readiness body does not expose degraded status: %s", recorder.Body.String())
	}
}

func TestMetricsRequireConfiguredBearerToken(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.New()
	httpServer := New(
		config.Config{MetricsAuthToken: "metrics-secret"},
		logger,
		nil,
		nil,
		nil,
		OperationalDependencies{Metrics: metrics},
	)

	unauthorized := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(
		unauthorized,
		httptest.NewRequest(http.MethodGet, "/internal/metrics", nil),
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}
	wrongSchemeRequest := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	wrongSchemeRequest.Header.Set("Authorization", "metrics-secret")
	wrongScheme := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(wrongScheme, wrongSchemeRequest)
	if wrongScheme.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-scheme status = %d, want %d", wrongScheme.Code, http.StatusUnauthorized)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer metrics-secret")
	authorized := httptest.NewRecorder()
	httpServer.Handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorized.Code, http.StatusOK, authorized.Body.String())
	}
	if !strings.Contains(authorized.Body.String(), "tellbook_http_requests_total") {
		t.Fatalf("metrics response is missing Tellbook HTTP metrics: %s", authorized.Body.String())
	}
}

func TestRequestMetricsIncludeRateLimitedResponses(t *testing.T) {
	t.Parallel()

	metrics := observability.New()
	httpServer := New(
		config.Config{HTTPRateLimitPerMinute: 1, HTTPRateLimitBurst: 1},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
		nil,
		nil,
		OperationalDependencies{Metrics: metrics},
	)
	for index, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodGet, "/v1/meta/markets", nil)
		request.RemoteAddr = "192.0.2.40:4000"
		recorder := httptest.NewRecorder()
		httpServer.Handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("request %d status = %d, want %d", index+1, recorder.Code, want)
		}
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), `tellbook_http_requests_total{method="GET",route="unmatched",status="429"} 1`) {
		t.Fatalf("rate-limited response is missing from request metrics: %s", recorder.Body.String())
	}
}

func TestRequestMetricsIncludeRecoveredPanics(t *testing.T) {
	t.Parallel()

	metrics := observability.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := requestLoggingMiddleware(logger, metrics, 0, time.Second)(
		chimiddleware.Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("test panic")
		})),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	metricsRecorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsRecorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsRecorder.Body.String(), `tellbook_http_requests_total{method="GET",route="unmatched",status="500"} 1`) {
		t.Fatalf("recovered panic is missing from request metrics: %s", metricsRecorder.Body.String())
	}
}

func TestSuccessfulEventStreamsDoNotFloodRequestLogs(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	handler := requestLoggingMiddleware(logger, nil, 1, time.Nanosecond)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
		}),
	)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/events", nil))
	if strings.Contains(output.String(), "http request") {
		t.Fatalf("successful event stream was logged as a sampled or slow request: %s", output.String())
	}
}

func TestInboxEventStreamsAreRecognizedForTimeoutExemption(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/v1/app/inbox/events",
		"/v1/marketplace/conversations/events",
		"/v1/app/tessa/events",
	} {
		if !isInboxEventStream(http.MethodGet, path) {
			t.Fatalf("GET %s was not recognized as an inbox event stream", path)
		}
	}
	if isInboxEventStream(http.MethodPost, "/v1/app/inbox/events") {
		t.Fatal("non-GET inbox request bypassed the route timeout")
	}
	if isInboxEventStream(http.MethodGet, "/v1/app/inbox/events/other") {
		t.Fatal("a non-stream inbox path bypassed the route timeout")
	}
}
