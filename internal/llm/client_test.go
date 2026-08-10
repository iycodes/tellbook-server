package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
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
