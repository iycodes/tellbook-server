package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"booking/go-server/internal/aierror"
	"booking/go-server/internal/config"
)

type compatibleFixture struct {
	Message string `json:"message"`
}

func TestOpenAICompatibleClientUsesIsolatedChatCompletionsContract(t *testing.T) {
	var temperature = 0.0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/v1/chat/completions" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer external-key" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, forbidden := range []string{
			"max_completion_tokens", "top_p", "top_k", "min_p", "repeat_penalty",
			"enable_thinking", "chat_template_kwargs", "json_schema", "instructions", "input", "store",
		} {
			if _, exists := payload[forbidden]; exists {
				t.Fatalf("request included forbidden field %q: %#v", forbidden, payload)
			}
		}
		if payload["model"] != "external-model" || payload["max_tokens"] != float64(321) ||
			payload["temperature"] != float64(0) || payload["stream"] != false {
			t.Fatalf("request settings = %#v", payload)
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
		format, ok := payload["response_format"].(map[string]any)
		if !ok || format["type"] != "json_schema" {
			t.Fatalf("response_format = %#v", payload["response_format"])
		}
		envelope, ok := format["json_schema"].(map[string]any)
		if !ok || envelope["name"] != "compatibleFixture" || envelope["strict"] != true {
			t.Fatalf("json_schema envelope = %#v", format["json_schema"])
		}
		schema, ok := envelope["schema"].(map[string]any)
		if !ok || schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("schema = %#v", envelope["schema"])
		}
		writeCompatibleSuccess(t, w, `{"message":"ready"}`)
	}))
	defer server.Close()

	client := newTestOpenAICompatibleClient(server.URL+"/gateway", config.OpenAICompatTokenFieldMaxTokens)
	client.temperature = &temperature
	var result compatibleFixture
	if err := client.GenerateJSON(context.Background(), "system", "user", &result); err != nil {
		t.Fatalf("GenerateJSON() error = %v", err)
	}
	if result.Message != "ready" {
		t.Fatalf("result = %#v", result)
	}
}

func TestOpenAICompatibleClientSelectsOneTokenFieldAndOmitsSampling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["max_completion_tokens"] != float64(321) {
			t.Fatalf("max_completion_tokens = %#v", payload["max_completion_tokens"])
		}
		for _, absent := range []string{"max_tokens", "temperature", "top_p"} {
			if _, exists := payload[absent]; exists {
				t.Fatalf("request unexpectedly included %q", absent)
			}
		}
		writeCompatibleSuccess(t, w, `{"value":"ok"}`)
	}))
	defer server.Close()

	client := newTestOpenAICompatibleClient(server.URL, config.OpenAICompatTokenFieldMaxCompletionTokens)
	payload, err := client.GenerateJSONSchema(
		context.Background(), "system", "user", "result",
		json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
	)
	if err != nil || string(payload) != `{"value":"ok"}` {
		t.Fatalf("GenerateJSONSchema() payload=%s error=%v", payload, err)
	}
}

func TestOpenAICompatibleClientValidatesOutputLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeCompatibleSuccess(t, w, `{"message":7}`)
	}))
	defer server.Close()
	client := newTestOpenAICompatibleClient(server.URL, config.OpenAICompatTokenFieldMaxTokens)

	var result compatibleFixture
	err := client.GenerateJSON(context.Background(), "system", "user", &result)
	if err == nil || !aierror.IsRepairable(err) || aierror.IsRetryable(err) {
		t.Fatalf("schema mismatch classification = %v", err)
	}
}

func TestOpenAICompatibleClientClassifiesStatusWithoutRetryOrBodyLeak(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{status: http.StatusUnauthorized},
		{status: http.StatusForbidden},
		{status: http.StatusBadRequest},
		{status: http.StatusTooManyRequests, retryable: true},
		{status: http.StatusInternalServerError, retryable: true},
		{status: http.StatusNotImplemented},
		{status: http.StatusBadGateway, retryable: true},
		{status: http.StatusServiceUnavailable, retryable: true},
		{status: http.StatusGatewayTimeout, retryable: true},
		{status: http.StatusHTTPVersionNotSupported},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":{"message":"private customer address"}}`))
			}))
			defer server.Close()
			client := newTestOpenAICompatibleClient(server.URL, config.OpenAICompatTokenFieldMaxTokens)

			var result compatibleFixture
			err := client.GenerateJSON(context.Background(), "system", "user", &result)
			if err == nil || aierror.IsRetryable(err) != test.retryable {
				t.Fatalf("status %d classification = %v", test.status, err)
			}
			if strings.Contains(err.Error(), "private customer address") || requests.Load() != 1 {
				t.Fatalf("status %d leaked or retried: requests=%d error=%v", test.status, requests.Load(), err)
			}
		})
	}
}

func TestOpenAICompatibleClientRejectsTerminalFinishReasons(t *testing.T) {
	tests := []struct {
		name         string
		finishReason string
		refusal      string
	}{
		{name: "truncated", finishReason: "length"},
		{name: "filtered", finishReason: "content_filter"},
		{name: "refused", finishReason: "stop", refusal: "cannot comply"},
		{name: "unsupported", finishReason: "tool_calls"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "chat_test", "model": "external-model",
					"choices": []map[string]any{{
						"finish_reason": test.finishReason,
						"message":       map[string]any{"role": "assistant", "content": "", "refusal": test.refusal},
					}},
				})
			}))
			defer server.Close()
			client := newTestOpenAICompatibleClient(server.URL, config.OpenAICompatTokenFieldMaxTokens)
			var result compatibleFixture
			err := client.GenerateJSON(context.Background(), "system", "user", &result)
			if err == nil || aierror.IsRetryable(err) || aierror.IsRepairable(err) {
				t.Fatalf("finish reason classification = %v", err)
			}
		})
	}
}

func TestOpenAICompatibleClientPropagatesCancellationAndBoundsResponses(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			writeCompatibleSuccess(t, w, `{"message":"unexpected"}`)
		}))
		defer server.Close()
		client := newTestOpenAICompatibleClient(server.URL, config.OpenAICompatTokenFieldMaxTokens)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var result compatibleFixture
		err := client.GenerateJSON(ctx, "system", "user", &result)
		if !errors.Is(err, context.Canceled) || requests.Load() != 0 {
			t.Fatalf("cancellation error=%v requests=%d", err, requests.Load())
		}
	})

	t.Run("cancellation while reading response", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		client := newTestOpenAICompatibleClient("https://models.example.com", config.OpenAICompatTokenFieldMaxTokens)
		client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       &cancelOnReadBody{cancel: cancel},
				Request:    request,
			}, nil
		})
		var result compatibleFixture
		err := client.GenerateJSON(ctx, "system", "user", &result)
		if !errors.Is(err, context.Canceled) || aierror.IsRetryable(err) {
			t.Fatalf("response-read cancellation classification = %v", err)
		}
	})

	t.Run("response size", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", openAICompatibleMaxResponseBytes+1)))
		}))
		defer server.Close()
		client := newTestOpenAICompatibleClient(server.URL, config.OpenAICompatTokenFieldMaxTokens)
		var result compatibleFixture
		err := client.GenerateJSON(context.Background(), "system", "user", &result)
		if err == nil || aierror.IsRetryable(err) {
			t.Fatalf("oversized response classification = %v", err)
		}
	})

	t.Run("client timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(40 * time.Millisecond)
			writeCompatibleSuccess(t, w, `{"message":"late"}`)
		}))
		defer server.Close()
		client := newTestOpenAICompatibleClient(server.URL, config.OpenAICompatTokenFieldMaxTokens)
		client.httpClient.Timeout = 5 * time.Millisecond
		var result compatibleFixture
		err := client.GenerateJSON(context.Background(), "system", "user", &result)
		if err == nil || !aierror.IsRetryable(err) {
			t.Fatalf("timeout classification = %v", err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type cancelOnReadBody struct {
	cancel context.CancelFunc
}

func (body *cancelOnReadBody) Read([]byte) (int, error) {
	body.cancel()
	return 0, errors.New("response stream closed")
}

func (*cancelOnReadBody) Close() error {
	return nil
}

func newTestOpenAICompatibleClient(baseURL, tokenLimitField string) *OpenAICompatibleClient {
	return &OpenAICompatibleClient{
		baseURL: baseURL, path: "/v1/chat/completions", model: "external-model",
		apiKey: "external-key", maxOutputTokens: 321, tokenLimitField: tokenLimitField,
		httpClient: &http.Client{Timeout: time.Second},
	}
}

func writeCompatibleSuccess(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"id": "chat_test", "model": "external-model",
		"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
