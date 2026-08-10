package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	aiapi "booking/go-server/shared/ai_api"
)

type schemaFixture struct {
	Name  string              `json:"name"`
	Note  *string             `json:"note"`
	Items []schemaFixtureItem `json:"items"`
}

func TestOpenAIClientReportsIncompleteResponseBeforeDecoding(t *testing.T) {
	responseLogFile := filepath.Join(t.TempDir(), "responses.jsonl")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                 "resp_incomplete",
			"object":             "response",
			"created_at":         1,
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
			"model":              "gpt-test",
			"output": []map[string]any{{
				"id":     "msg_incomplete",
				"type":   "message",
				"status": "incomplete",
				"role":   "assistant",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        `{"name":"unfinished`,
					"annotations": []any{},
				}},
			}},
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 1000,
				"total_tokens":  1010,
				"output_tokens_details": map[string]any{
					"reasoning_tokens": 700,
				},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIBaseURL:         server.URL + "/v1",
		OpenAIModel:           "gpt-test",
		OpenAIAPIKey:          "test-key",
		OpenAIReasoningEffort: "low",
		OpenAITimeout:         time.Second,
		OpenAIMaxOutputTokens: 1000,
		OpenAIResponseLogFile: responseLogFile,
	})
	var response schemaFixture
	err := client.GenerateJSON(context.Background(), "system", "user", &response)
	if err == nil || !strings.Contains(err.Error(), "output token limit") {
		t.Fatalf("GenerateJSON error = %v", err)
	}
	logged, readErr := os.ReadFile(responseLogFile)
	if readErr != nil {
		t.Fatalf("read response log: %v", readErr)
	}
	var entry openAIResponseLogEntry
	if err := json.Unmarshal(logged, &entry); err != nil {
		t.Fatalf("decode response log: %v", err)
	}
	if entry.Status != "incomplete" || entry.IncompleteReason != "max_output_tokens" || entry.Output != `{"name":"unfinished` {
		t.Fatalf("response log entry = %#v", entry)
	}
}

type schemaFixtureItem struct {
	Value string `json:"value"`
}

func TestOpenAIClientUsesResponsesStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header was not set")
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "gpt-test" {
			t.Fatalf("model = %#v", payload["model"])
		}
		if payload["store"] != false {
			t.Fatalf("store = %#v", payload["store"])
		}
		reasoning := payload["reasoning"].(map[string]any)
		if reasoning["effort"] != "medium" {
			t.Fatalf("reasoning effort = %#v", reasoning["effort"])
		}
		textConfig := payload["text"].(map[string]any)
		format := textConfig["format"].(map[string]any)
		if format["type"] != "json_schema" || format["strict"] != true {
			t.Fatalf("response format = %#v", format)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         "resp_test",
			"object":     "response",
			"created_at": 1,
			"status":     "completed",
			"model":      "gpt-test",
			"output": []map[string]any{{
				"id":     "msg_test",
				"type":   "message",
				"status": "completed",
				"role":   "assistant",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        `{"name":"TellBook","note":null,"items":[{"value":"ready"}]}`,
					"annotations": []any{},
				}},
			}},
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 5,
				"total_tokens":  15,
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIBaseURL:         server.URL + "/v1",
		OpenAIModel:           "gpt-test",
		OpenAIAPIKey:          "test-key",
		OpenAIReasoningEffort: "medium",
		OpenAITimeout:         time.Second,
		OpenAIMaxOutputTokens: 1000,
	})
	var response schemaFixture
	if err := client.GenerateJSON(context.Background(), "system", "user", &response); err != nil {
		t.Fatalf("GenerateJSON returned error: %v", err)
	}
	if response.Name != "TellBook" || len(response.Items) != 1 || response.Items[0].Value != "ready" {
		t.Fatalf("decoded response = %#v", response)
	}
}

func TestResponseSchemaIsStrictAtEveryObjectLevel(t *testing.T) {
	name, schema, err := responseSchema(&schemaFixture{})
	if err != nil {
		t.Fatalf("responseSchema returned error: %v", err)
	}
	if name != "schemaFixture" {
		t.Fatalf("schema name = %q", name)
	}
	assertStrictObject(t, schema, []string{"items", "name", "note"})

	properties := schema["properties"].(map[string]any)
	items := properties["items"].(map[string]any)
	itemSchema := items["items"].(map[string]any)
	assertStrictObject(t, itemSchema, []string{"value"})
}

func TestResponseSchemaRejectsNonStructDestination(t *testing.T) {
	_, _, err := responseSchema(new(string))
	if err == nil {
		t.Fatal("expected a schema error")
	}
}

func TestResponseSchemaUsesAgreementDocumentUnion(t *testing.T) {
	name, schema, err := responseSchema(&aiapi.GenerateAgreementDocumentResponse{})
	if err != nil {
		t.Fatalf("responseSchema() error = %v", err)
	}
	if name != "GenerateAgreementDocumentResponse" {
		t.Fatalf("name = %q", name)
	}
	properties := schema["properties"].(map[string]any)
	document := properties["document_schema"].(map[string]any)
	if _, ok := document["anyOf"].([]any); !ok {
		t.Fatalf("document_schema = %#v", document)
	}
}

func assertStrictObject(t *testing.T, schema map[string]any, expectedRequired []string) {
	t.Helper()
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("additionalProperties = %#v", schema["additionalProperties"])
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required = %#v", schema["required"])
	}
	if len(required) != len(expectedRequired) {
		t.Fatalf("required = %#v", required)
	}
	for index, key := range expectedRequired {
		if required[index] != key {
			t.Fatalf("required[%d] = %#v, want %q", index, required[index], key)
		}
	}
}
