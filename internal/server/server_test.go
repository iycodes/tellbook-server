package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"booking/go-server/internal/config"
)

func TestMarketsEndpointIsPublic(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	httpServer := New(config.Config{}, logger, nil, nil, nil)

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

func TestWebhookIngressLogContainsSafeRequestMetadata(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	httpServer := New(config.Config{}, logger, nil, nil, nil)

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
		"path=/v1/webhooks/payaza",
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
