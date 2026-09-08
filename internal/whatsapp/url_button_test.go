package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestURLButtonContractAndValidation(t *testing.T) {
	const correlation = "243e9047-d9a6-42f4-a4e1-244e9056f245"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Type          string `json:"type"`
			Product       string `json:"messaging_product"`
			RecipientType string `json:"recipient_type"`
			To            string `json:"to"`
			Correlation   string `json:"biz_opaque_callback_data"`
			Interactive   struct {
				Type string `json:"type"`
				Body struct {
					Text string `json:"text"`
				} `json:"body"`
				Action struct {
					Name       string `json:"name"`
					Parameters struct {
						Label string `json:"display_text"`
						URL   string `json:"url"`
					} `json:"parameters"`
				} `json:"action"`
			} `json:"interactive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v24.0/333/messages" || r.Header.Get("Authorization") != "Bearer secret-token" || request.Type != "interactive" || request.Product != "whatsapp" || request.RecipientType != "individual" || request.To != "2348142751683" || request.Correlation != correlation || request.Interactive.Type != "cta_url" || request.Interactive.Body.Text != "7 September · 9:10 AM" || request.Interactive.Action.Name != "cta_url" || request.Interactive.Action.Parameters.Label != "View booking" || request.Interactive.Action.Parameters.URL != "https://provider.example.invalid/bookings?booking=123" {
			t.Errorf("unexpected request: %+v", request)
		}
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.button"}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server)
	button := URLButton{Label: "View booking", URL: "https://provider.example.invalid/bookings?booking=123"}
	result, err := client.SendURLButton(context.Background(), "+2348142751683", "7 September · 9:10 AM", button, correlation)
	if err != nil || result.MessageID != "wamid.button" {
		t.Fatal(result, err)
	}
	for _, test := range []struct{ to, body, label, url, id string }{
		{"invalid", "body", button.Label, button.URL, correlation},
		{"+2348142751683", "", button.Label, button.URL, correlation},
		{"+2348142751683", strings.Repeat("😀", 1025), button.Label, button.URL, correlation},
		{"+2348142751683", "body", strings.Repeat("x", 21), button.URL, correlation},
		{"+2348142751683", "body", button.Label, "http://evil.invalid", correlation},
		{"+2348142751683", "body", button.Label, "https://user:pass@evil.invalid", correlation},
		{"+2348142751683", "body", button.Label, button.URL, "invalid"},
	} {
		_, err := client.SendURLButton(context.Background(), test.to, test.body, URLButton{Label: test.label, URL: test.url}, test.id)
		var invalid *RequestError
		if !errors.As(err, &invalid) {
			t.Fatalf("expected pre-dispatch rejection, got %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("unexpected network calls: %d", calls)
	}
}

func TestURLButtonDoesNotRetryOrResendAsText(t *testing.T) {
	for _, response := range []struct {
		status    int
		body      string
		ambiguous bool
	}{
		{503, `{"error":{"code":2,"message":"temporary"}}`, false},
		{200, `{"messages":[]}`, true},
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(response.status)
			_, _ = w.Write([]byte(response.body))
		}))
		client := newTestClient(t, server)
		_, err := client.SendURLButton(context.Background(), "+2348142751683", "body", URLButton{Label: "View bookings", URL: "https://provider.example.invalid/bookings"}, "243e9047-d9a6-42f4-a4e1-244e9056f245")
		server.Close()
		if err == nil || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		if response.ambiguous {
			var transport *TransportError
			if !errors.As(err, &transport) || !transport.Ambiguous {
				t.Fatalf("lost ambiguity: %v", err)
			}
		}
	}
}
