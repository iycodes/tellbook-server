package publicsupport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/mailer"
	"github.com/go-chi/chi/v5"
)

type fakeSender struct {
	enabled  bool
	err      error
	messages []mailer.Message
	deadline time.Duration
}

func (s *fakeSender) Enabled() bool { return s != nil && s.enabled }
func (s *fakeSender) Send(ctx context.Context, message mailer.Message) error {
	s.messages = append(s.messages, message)
	if deadline, ok := ctx.Deadline(); ok {
		s.deadline = time.Until(deadline)
	}
	return s.err
}

func validInput() requestInput {
	return requestInput{Name: "Ada Customer", Email: "ada@example.com", Topic: "privacy", Subject: "My information", Message: "Please help me access my account information."}
}

func testHandler(sender mailer.Sender) *Handler {
	return New(Config{Recipient: "support@example.com", FromEmail: "hello@example.com", PublicURL: "https://app.tellbook.test"}, sender)
}

func submitRequest(handler *Handler, input any) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(input)
	request := httptest.NewRequest(http.MethodPost, "/requests", strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.tellbook.test")
	response := httptest.NewRecorder()
	handler.submit(response, request)
	return response
}

func TestSuccessUsesOnlyConfiguredDestinationAndValidatedReplyTo(t *testing.T) {
	sender := &fakeSender{enabled: true}
	input := validInput()
	input.Message += "\n<script>alert('text only')</script>"
	response := submitRequest(testHandler(sender), input)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"success":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(sender.messages) != 1 {
		t.Fatalf("sent %d messages", len(sender.messages))
	}
	message := sender.messages[0]
	if message.ToEmail != "support@example.com" || message.ReplyToEmail != input.Email || message.ReplyToName != input.Name {
		t.Fatalf("incorrect routing: %+v", message)
	}
	if message.HTML != "" || !strings.Contains(message.Text, input.Message) {
		t.Fatal("request must remain plain text")
	}
	if sender.deadline <= 0 || sender.deadline > SendTimeout {
		t.Fatalf("unbounded send deadline=%v", sender.deadline)
	}
}

func TestCannotChooseRecipient(t *testing.T) {
	sender := &fakeSender{enabled: true}
	encoded, _ := json.Marshal(validInput())
	var input map[string]any
	_ = json.Unmarshal(encoded, &input)
	for _, field := range []string{"to_email", "recipient", "bcc", "from_email"} {
		input[field] = "attacker@example.com"
		response := submitRequest(testHandler(sender), input)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("field %s status=%d", field, response.Code)
		}
		delete(input, field)
	}
	if len(sender.messages) != 0 {
		t.Fatal("untrusted destination caused mail")
	}
}

func TestMissingConfigAndSMTPFailureDoNotReportSuccess(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		handler *Handler
	}{
		{"no sender", testHandler(nil)},
		{"disabled sender", testHandler(&fakeSender{})},
		{"missing destination", New(Config{FromEmail: "hello@example.com", PublicURL: "https://app.tellbook.test"}, &fakeSender{enabled: true})},
		{"invalid from", New(Config{Recipient: "support@example.com", FromEmail: "invalid", PublicURL: "https://app.tellbook.test"}, &fakeSender{enabled: true})},
		{"SMTP failure", testHandler(&fakeSender{enabled: true, err: errors.New("secret SMTP failure for ada@example.com")})},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := submitRequest(scenario.handler, validInput())
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), `"success":true`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "ada@example.com") {
				t.Fatal("response leaked transport error")
			}
		})
	}
}

func TestSMTPFailureDistinguishesUncertainAcceptance(t *testing.T) {
	for _, disposition := range []mailer.TransportDisposition{mailer.DispositionRetryable, mailer.DispositionPermanent, mailer.DispositionAmbiguous} {
		t.Run(string(disposition), func(t *testing.T) {
			sender := &fakeSender{enabled: true, err: &mailer.TransportError{Disposition: disposition, Stage: "data_commit", Cause: errors.New("secret transport failure")}}
			response := submitRequest(testHandler(sender), validInput())
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d", response.Code)
			}
			unconfirmed := strings.Contains(response.Body.String(), "delivery_unconfirmed")
			if unconfirmed != (disposition == mailer.DispositionAmbiguous) {
				t.Fatalf("incorrect uncertainty=%t for %s", unconfirmed, disposition)
			}
		})
	}
}

func TestValidationAndHoneypot(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*requestInput)
	}{
		{"name header injection", func(v *requestInput) { v.Name = "Ada\r\nBcc: attacker@example.com" }},
		{"subject header injection", func(v *requestInput) { v.Subject = "hello\nBcc: attacker@example.com" }},
		{"address list", func(v *requestInput) { v.Email = "ada@example.com,attacker@example.com" }},
		{"display address", func(v *requestInput) { v.Email = "Ada <ada@example.com>" }},
		{"email injection", func(v *requestInput) { v.Email = "ada@example.com\r\nBcc: attacker@example.com" }},
		{"unknown topic", func(v *requestInput) { v.Topic = "../mail" }},
		{"short message", func(v *requestInput) { v.Message = "help" }},
		{"long message", func(v *requestInput) { v.Message = strings.Repeat("x", 6001) }},
		{"control message", func(v *requestInput) { v.Message += "\x00" }},
		{"honeypot", func(v *requestInput) { v.Website = "https://spam.example" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sender := &fakeSender{enabled: true}
			input := validInput()
			scenario.change(&input)
			response := submitRequest(testHandler(sender), input)
			if response.Code != http.StatusBadRequest || len(sender.messages) != 0 {
				t.Fatalf("status=%d sends=%d", response.Code, len(sender.messages))
			}
		})
	}
}

func TestOriginContentTypeAndPayloadProtections(t *testing.T) {
	for _, scenario := range []struct {
		name, origin, contentType, body string
		status                          int
	}{
		{"wrong origin", "https://evil.example", "application/json", "{}", 403},
		{"missing origin", "", "application/json", "{}", 403},
		{"plain form", "https://app.tellbook.test", "text/plain", "{}", 415},
		{"oversized chunked", "https://app.tellbook.test", "application/json", `{"message":"` + strings.Repeat("x", MaxPayloadBytes) + `"}`, 413},
		{"multiple JSON values", "https://app.tellbook.test", "application/json", "{} {}", 400},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sender := &fakeSender{enabled: true}
			request := httptest.NewRequest(http.MethodPost, "/requests", strings.NewReader(scenario.body))
			request.Header.Set("Origin", scenario.origin)
			request.Header.Set("Content-Type", scenario.contentType)
			request.ContentLength = -1
			response := httptest.NewRecorder()
			testHandler(sender).submit(response, request)
			if response.Code != scenario.status || len(sender.messages) != 0 {
				t.Fatalf("status=%d sends=%d", response.Code, len(sender.messages))
			}
		})
	}
}

func TestAvailabilityDoesNotExposeMailbox(t *testing.T) {
	router := chi.NewRouter()
	router.Route("/support", testHandler(&fakeSender{enabled: true}).Routes)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/support", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"available":true}` {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
