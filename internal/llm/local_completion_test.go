package llm

import (
	"booking/go-server/internal/aierror"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLocalCompletionPreservesZeroTemperatureAndRejectsTruncation(t *testing.T) {
	for _, reason := range []string{"stop", "length", "content_filter", "tool_calls", ""} {
		t.Run(reason, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if v, ok := payload["temperature"]; !ok || v != float64(0) {
					t.Error("explicit zero was omitted")
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": reason, "message": map[string]string{"content": "{}"}}}})
			}))
			defer server.Close()
			client := &Client{baseURL: server.URL, path: "/", httpClient: &http.Client{Timeout: time.Second}}
			err := client.GenerateJSON(context.Background(), "system", "question", &struct{}{})
			if (err == nil) != (reason == "stop") || calls != 1 {
				t.Fatal(reason, err, calls)
			}
			if reason == "length" {
				var typed *aierror.Error
				if !errors.As(err, &typed) || typed.Kind != aierror.KindOutputTruncated {
					t.Fatal(err)
				}
			}
		})
	}
}
