package whatsapp

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
)

func TestClientSendsOneTypedTemplateRequest(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		if request.URL.Path != "/v24.0/333/messages" || request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("unexpected request path/header: %s %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var payload templateRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Template.Name != "provider_booking_reminder" || payload.BizOpaqueCallbackData != "delivery-1" {
			t.Errorf("unexpected payload: %#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"messages":[{"id":"wamid.test"}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server)
	result, err := client.SendTemplate(context.Background(), providerReminderMessage())
	if err != nil || result.MessageID != "wamid.test" {
		t.Fatalf("SendTemplate() = %#v, %v", result, err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count = %d", requestCount.Load())
	}
}

func TestTessaTextRequestContract(t *testing.T) {
	const correlation = "243e9047-d9a6-42f4-a4e1-244e9056f245"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Product     string `json:"messaging_product"`
			To          string `json:"to"`
			Type        string `json:"type"`
			Correlation string `json:"biz_opaque_callback_data"`
			Text        struct {
				Body    string `json:"body"`
				Preview bool   `json:"preview_url"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v24.0/333/messages" || payload.Product != "whatsapp" || payload.To != "2348142751683" || payload.Type != "text" || payload.Correlation != correlation || payload.Text.Body != "Connected to Tessa." || payload.Text.Preview {
			t.Errorf("unexpected text request: %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.tessa"}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server)
	result, err := client.SendText(context.Background(), "+2348142751683", "Connected to Tessa.", correlation)
	if err != nil || result.MessageID != "wamid.tessa" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, body := range []string{"", strings.Repeat("a", 4097)} {
		if _, err := client.SendText(context.Background(), "+2348142751683", body, correlation); err == nil {
			t.Fatal("invalid body accepted")
		}
	}
	if _, err := client.SendText(context.Background(), "+2348142751683", "text", "invalid"); err == nil {
		t.Fatal("invalid correlation accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestTessaTypingRequestContractAndNoRetry(t *testing.T) {
	for _, failure := range []bool{false, true} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			var payload struct {
				Product   string `json:"messaging_product"`
				Status    string `json:"status"`
				MessageID string `json:"message_id"`
				Typing    struct {
					Type string `json:"type"`
				} `json:"typing_indicator"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if r.URL.Path != "/v24.0/333/messages" || payload.Product != "whatsapp" || payload.Status != "read" || payload.MessageID != "wamid.inbound" || payload.Typing.Type != "text" {
				t.Error("incorrect typing request")
			}
			if failure {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":2}}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		}))
		client := newTestClient(t, server)
		err := client.SendTyping(context.Background(), "wamid.inbound")
		server.Close()
		if (err != nil) != failure || calls.Load() != 1 {
			t.Fatal("typing contract/retry mismatch", err, calls.Load())
		}
		if err = client.SendTyping(context.Background(), ""); err == nil {
			t.Fatal("empty typing source accepted")
		}
	}
}

func TestClientDoesNotRetryTransientGraphFailure(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(`{"error":{"message":"temporary","type":"OAuthException","code":2}}`))
	}))
	defer server.Close()
	client := newTestClient(t, server)
	_, err := client.SendTemplate(context.Background(), providerReminderMessage())
	var graphError *GraphError
	if !errors.As(err, &graphError) || graphError.Class != ErrorClassTransient {
		t.Fatalf("SendTemplate() error = %#v", err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("client retried: request count = %d", requestCount.Load())
	}
}

func TestClientTypesPreflightFailureBeforeNetwork(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	}))
	defer server.Close()
	client := newTestClient(t, server)
	message := providerReminderMessage()
	delete(message.Values.Body, "amount_due")
	_, err := client.SendTemplate(context.Background(), message)
	var requestError *RequestError
	if !errors.As(err, &requestError) {
		t.Fatalf("preflight error = %#v", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("preflight failure issued %d requests", requestCount.Load())
	}
}

func TestClientRejectsOversizedSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat("x", maxGraphResponseBytes+1)))
	}))
	defer server.Close()
	client := newTestClient(t, server)
	_, err := client.SendTemplate(context.Background(), providerReminderMessage())
	var transportError *TransportError
	if !errors.As(err, &transportError) || !transportError.Ambiguous {
		t.Fatalf("oversized success error = %#v", err)
	}
}

func TestGraphErrorClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   ErrorClass
	}{
		{name: "rate", status: http.StatusTooManyRequests, body: `{"error":{"code":4}}`, want: ErrorClassRateLimit},
		{name: "auth", status: http.StatusForbidden, body: `{"error":{"code":190}}`, want: ErrorClassAuth},
		{name: "transient", status: http.StatusInternalServerError, body: `{"error":{"code":2}}`, want: ErrorClassTransient},
		{name: "permanent", status: http.StatusBadRequest, body: `{"error":{"code":100}}`, want: ErrorClassPermanent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var graphError *GraphError
			if err := decodeGraphError(test.status, nil, []byte(test.body)); !errors.As(err, &graphError) || graphError.Class != test.want {
				t.Fatalf("decodeGraphError() = %#v, want %s", err, test.want)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("12", now); got != 12*time.Second {
		t.Fatalf("numeric Retry-After = %s", got)
	}
	if got := parseRetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now); got != time.Minute {
		t.Fatalf("date Retry-After = %s", got)
	}
}

func TestNewClientRejectsUnsafePathIdentifiers(t *testing.T) {
	for _, config := range []ClientConfig{
		{BaseURL: "https://graph.facebook.com", GraphVersion: "../v24.0", PhoneNumberID: "333", BusinessAccountID: "222", AccessToken: "token", Timeout: time.Second},
		{BaseURL: "https://graph.facebook.com", GraphVersion: "v24.0", PhoneNumberID: "333/messages", BusinessAccountID: "222", AccessToken: "token", Timeout: time.Second},
	} {
		if _, err := NewClient(config); err == nil {
			t.Fatalf("NewClient accepted unsafe identifiers: %#v", config)
		}
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	httpClient := server.Client()
	httpClient.Timeout = time.Second
	client, err := NewClient(ClientConfig{
		BaseURL: server.URL, GraphVersion: "v24.0", PhoneNumberID: "333",
		BusinessAccountID: "222", AccessToken: "secret-token", Timeout: time.Second,
		HTTPClient: httpClient, EnabledTemplateKeys: []string{"provider_booking_reminder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func providerReminderMessage() TemplateMessage {
	return TemplateMessage{
		To: "+2348012345678", Key: TemplateProviderBookingReminder,
		Values: TemplateValues{
			Body: map[string]string{
				"provider_name": "Ada", "service_title": "Hair styling", "customer_name": "Tayo",
				"appointment_datetime": "Tomorrow", "booking_location": "Lagos", "amount_due": "NGN 0",
			},
			Button:             map[string]string{"booking_route_suffix": "?booking=booking-id"},
			OpaqueCallbackData: "delivery-1",
		},
	}
}
