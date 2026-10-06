// Package publicsupport handles anonymous requests sent only to the operator's
// configured support mailbox. It never accepts an email destination from callers.
package publicsupport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"booking/go-server/internal/mailer"
	"github.com/go-chi/chi/v5"
)

const (
	MaxPayloadBytes    = 32 * 1024
	SendTimeout        = 10 * time.Second
	UnavailableMessage = "The support form is temporarily unavailable. Your message has not been sent. Please try again later."
)

type Config struct {
	Recipient string
	FromEmail string
	PublicURL string
}

type Handler struct {
	recipient  string
	origin     string
	sender     mailer.Sender
	configured bool
}

func New(cfg Config, sender mailer.Sender) *Handler {
	origin := ""
	if parsed, err := url.Parse(cfg.PublicURL); err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil {
		origin = parsed.Scheme + "://" + parsed.Host
	}
	return &Handler{
		recipient: cfg.Recipient, origin: origin, sender: sender,
		configured: mailer.ValidMailbox(cfg.Recipient) && mailer.ValidMailbox(cfg.FromEmail) && origin != "",
	}
}

func (h *Handler) available() bool {
	return h.configured && h.sender != nil && h.sender.Enabled()
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/", h.status)
	r.Post("/requests", h.submit)
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"available": h.available()})
}

type requestInput struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Topic   string `json:"topic"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	Website string `json:"website"` // Honeypot; never included in mail.
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	// Browser mutations require the configured app origin, even when CORS is
	// permissive for another API route. The SvelteKit action forwards its own origin.
	if h.origin == "" || r.Header.Get("Origin") != h.origin {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Reload the support page and try again."})
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"message": "Send the request as JSON."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxPayloadBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input requestInput
	if err := decoder.Decode(&input); err != nil {
		var limitError *http.MaxBytesError
		if errors.As(err, &limitError) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "Your message is too large. Please shorten it and try again."})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Check the request and try again."})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Check the request and try again."})
		return
	}
	if input.Website != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "The request could not be submitted. Reload the page and try again."})
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(input.Email)
	input.Topic = strings.TrimSpace(input.Topic)
	input.Subject = strings.TrimSpace(input.Subject)
	input.Message = strings.TrimSpace(input.Message)
	if fields := validate(input); len(fields) != 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Please check the highlighted fields.", "errors": fields})
		return
	}
	if !h.available() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": UnavailableMessage})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), SendTimeout)
	defer cancel()
	// Plain text avoids treating user-provided content as HTML. The SMTP From
	// stays configured on the sender; the validated visitor address is Reply-To.
	message := mailer.Message{
		ToEmail: h.recipient, ToName: "Tellbook support",
		ReplyToEmail: input.Email, ReplyToName: input.Name,
		Subject: fmt.Sprintf("[Tellbook support · %s] %s", input.Topic, input.Subject),
		Text:    fmt.Sprintf("Support request for Tellbook\n\nName: %s\nEmail: %s\nTopic: %s\nSubject: %s\n\n%s\n", input.Name, input.Email, input.Topic, input.Subject, input.Message),
	}
	if err := h.sender.Send(ctx, message); err != nil {
		// Transport errors may include destinations or server credentials. They
		// must not be sent to the browser or placed in public request logs.
		disposition, _ := mailer.ClassifyTransportError(err)
		if disposition == mailer.DispositionAmbiguous {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "delivery_unconfirmed", "message": "We could not confirm delivery. Please wait before trying again."})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": UnavailableMessage})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func validate(input requestInput) map[string]string {
	errors := map[string]string{}
	for _, field := range []struct {
		name, value string
		max         int
	}{
		{"name", input.Name, 120}, {"subject", input.Subject, 180},
	} {
		if field.value == "" || utf8.RuneCountInString(field.value) > field.max || strings.IndexFunc(field.value, unicode.IsControl) != -1 {
			errors[field.name] = fmt.Sprintf("Enter a %s of 1–%d characters on one line.", field.name, field.max)
		}
	}
	if !mailer.ValidMailbox(input.Email) {
		errors["email"] = "Enter a valid email address so we can reply."
	}
	switch input.Topic {
	case "account", "bookings", "payments", "connected-apps", "privacy", "other":
	default:
		errors["topic"] = "Choose a support topic."
	}
	length := utf8.RuneCountInString(input.Message)
	if length < 20 || length > 6000 || strings.IndexFunc(input.Message, func(character rune) bool {
		return unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t'
	}) != -1 {
		errors["message"] = "Describe what happened in 20–6,000 characters."
	}
	return errors
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
