package llm

import (
	"booking/go-server/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalApplicationSchemaPreservesDiscriminatorOrder(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"argument":{"type":"string"}},"required":["name","argument"],"additionalProperties":false}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Both the native and OpenAI-compatible schema envelopes must preserve
		// the application-defined order, not alphabetically sorted map keys.
		if bytes.Count(body, []byte(`"properties":{"name":`)) != 2 {
			t.Errorf("schema order changed: %s", body)
		}
		io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"name\":\"test\",\"argument\":\"value\"}"}}]}`)
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, path: "/", httpClient: &http.Client{Timeout: time.Second}}
	if _, err := client.GenerateJSONSchema(context.Background(), "system", "user", "fixture", schema); err != nil {
		t.Fatal(err)
	}
}

func TestHostedApplicationSchemaIsNotReplacedWithReflection(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"type":"string","enum":["application_contract"]}}}`)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Text struct {
				Format struct {
					Schema json.RawMessage `json:"schema"`
					Name   string          `json:"name"`
					Strict bool            `json:"strict"`
				} `json:"format"`
			} `json:"text"`
			Store bool `json:"store"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Text.Format.Name != "fixture" || !request.Text.Format.Strict || request.Store || !strings.Contains(string(request.Text.Format.Schema), "application_contract") {
			t.Error("application schema was not sent")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp_fixture","object":"response","status":"completed","model":"test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"kind\":\"application_contract\"}"}]}]}`)
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{OpenAIBaseURL: server.URL, OpenAIAPIKey: "test", OpenAIModel: "test", OpenAITimeout: time.Second, OpenAIMaxOutputTokens: 1000})
	output, err := client.GenerateJSONSchema(context.Background(), "system", "user", "fixture", schema)
	if err != nil || string(output) != `{"kind":"application_contract"}` || calls != 1 {
		t.Fatal(string(output), err, calls)
	}
}

func TestHostedApplicationSchemaPreservesNestedDiscriminatorOrder(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["scope","tools"],"properties":{"scope":{"type":"string"},"tools":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["name","argument"],"properties":{"name":{"type":"string"},"argument":{"type":"string"}}}}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"properties":{"scope":`)) || !bytes.Contains(body, []byte(`"properties":{"name":`)) {
			t.Errorf("application schema property order changed on the wire: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp_fixture","object":"response","status":"completed","model":"test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"scope\":\"test\",\"tools\":[{\"name\":\"test\",\"argument\":\"value\"}]}"}]}]}`)
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{OpenAIBaseURL: server.URL, OpenAIAPIKey: "test", OpenAIModel: "test", OpenAITimeout: time.Second, OpenAIMaxOutputTokens: 1000})
	if _, err := client.GenerateJSONSchema(context.Background(), "system", "user", "fixture", schema); err != nil {
		t.Fatal(err)
	}
}
