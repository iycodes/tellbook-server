package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/aierror"
)

func TestGenerateJSONDoesNotExposeErrorResponseBody(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"private customer address"}}`))
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, path: "/", model: "test", httpClient: &http.Client{Timeout: time.Second}}
	err := client.GenerateJSON(context.Background(), "system", "private prompt", &struct{}{})
	if err == nil || strings.Contains(err.Error(), "private customer address") {
		t.Fatalf("GenerateJSON() leaked response body: %v", err)
	}
}

func TestGenerateJSONPropagatesCancellationWhileReadingResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{
		baseURL: "https://models.example.com", path: "/v1/chat/completions", model: "test",
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       &cancelOnReadBody{cancel: cancel},
				Request:    request,
			}, nil
		})},
	}
	err := client.GenerateJSON(ctx, "system", "user", &struct{}{})
	if !errors.Is(err, context.Canceled) || aierror.IsRetryable(err) {
		t.Fatalf("response-read cancellation classification = %v", err)
	}
}

func TestGenerateJSONParsesFencedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "test-model" {
			t.Fatalf("unexpected model: %#v", payload["model"])
		}
		if payload["max_tokens"] != float64(256) {
			t.Fatalf("unexpected max_tokens: %#v", payload["max_tokens"])
		}
		responseFormat, ok := payload["response_format"].(map[string]any)
		if !ok || responseFormat["type"] != "json_object" {
			t.Fatalf("unexpected response_format: %#v", payload["response_format"])
		}
		chatTemplateKwargs, ok := payload["chat_template_kwargs"].(map[string]any)
		if !ok || chatTemplateKwargs["enable_thinking"] != false {
			t.Fatalf("thinking was not disabled in the chat template: %#v", payload["chat_template_kwargs"])
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"finish_reason": "stop",
					"message": map[string]any{
						"content": "```json\n{\"message\":\"hello\"}\n```",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{
		baseURL:         server.URL,
		path:            "/v1/chat/completions",
		model:           "test-model",
		temperature:     0.2,
		maxOutputTokens: 256,
		httpClient:      &http.Client{Timeout: time.Second},
	}

	var response struct {
		Message string `json:"message"`
	}
	if err := client.GenerateJSON(context.Background(), "system", "user", &response); err != nil {
		t.Fatalf("GenerateJSON returned error: %v", err)
	}
	if response.Message != "hello" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestGenerateJSONSchemaSendsStrictApplicationSchema(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		responseFormat, ok := payload["response_format"].(map[string]any)
		if !ok || responseFormat["type"] != "json_schema" {
			t.Fatalf("unexpected response_format: %#v", payload["response_format"])
		}
		envelope, ok := responseFormat["json_schema"].(map[string]any)
		if !ok || envelope["name"] != "semi_pilot_turn" || envelope["strict"] != true {
			t.Fatalf("unexpected schema envelope: %#v", responseFormat["json_schema"])
		}
		schema, ok := envelope["schema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("unexpected schema: %#v", envelope["schema"])
		}
		topLevelSchema, ok := payload["json_schema"].(map[string]any)
		if !ok || topLevelSchema["type"] != "object" {
			t.Fatalf("unexpected top-level llama schema: %#v", payload["json_schema"])
		}
		chatTemplateKwargs, ok := payload["chat_template_kwargs"].(map[string]any)
		if !ok || chatTemplateKwargs["enable_thinking"] != false || payload["enable_thinking"] != false {
			t.Fatalf("schema generation requested model thinking: %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"content": `{"protocol_version":1}`},
			}},
		})
	}))
	defer server.Close()

	client := &Client{
		baseURL: server.URL, path: "/", model: "test-model",
		enableThinking: true, maxOutputTokens: 256, httpClient: &http.Client{Timeout: time.Second},
	}
	payload, err := client.GenerateJSONSchema(
		context.Background(), "system", "user", "semi_pilot_turn",
		json.RawMessage(`{"type":"object"}`),
	)
	if err != nil {
		t.Fatalf("GenerateJSONSchema() error = %v", err)
	}
	if string(payload) != `{"protocol_version":1}` {
		t.Fatalf("unexpected payload: %s", payload)
	}
}

func TestExtractJSONObjectHandlesPrefixedText(t *testing.T) {
	t.Parallel()

	got, err := extractJSONObject("Here is the JSON:\n{\"safe_to_send\":true}")
	if err != nil {
		t.Fatalf("extractJSONObject returned error: %v", err)
	}
	if got != "{\"safe_to_send\":true}" {
		t.Fatalf("unexpected payload: %s", got)
	}
}
