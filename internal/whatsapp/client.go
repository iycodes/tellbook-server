package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const maxGraphResponseBytes = 1 << 20

type ClientConfig struct {
	BaseURL             string
	GraphVersion        string
	PhoneNumberID       string
	BusinessAccountID   string
	AccessToken         string
	Timeout             time.Duration
	HTTPClient          *http.Client
	EnabledTemplateKeys []string
}

type Client struct {
	baseURL           string
	graphVersion      string
	phoneNumberID     string
	businessAccountID string
	accessToken       string
	httpClient        *http.Client
	enabledTemplates  map[TemplateKey]struct{}
}

func NewClient(config ClientConfig) (*Client, error) {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.GraphVersion = strings.TrimSpace(config.GraphVersion)
	config.PhoneNumberID = strings.TrimSpace(config.PhoneNumberID)
	config.BusinessAccountID = strings.TrimSpace(config.BusinessAccountID)
	config.AccessToken = strings.TrimSpace(config.AccessToken)
	if config.BaseURL == "" || config.GraphVersion == "" || config.PhoneNumberID == "" ||
		config.BusinessAccountID == "" || config.AccessToken == "" {
		return nil, errors.New("complete WhatsApp Graph client configuration is required")
	}
	if !validGraphAPIVersion(config.GraphVersion) || !decimalPathSegment(config.PhoneNumberID) ||
		!decimalPathSegment(config.BusinessAccountID) || containsControlCharacter(config.AccessToken) {
		return nil, errors.New("WhatsApp Graph client identifiers are invalid")
	}
	if err := ValidateEnabledTemplateKeys(config.EnabledTemplateKeys); err != nil {
		return nil, err
	}
	enabledTemplates := make(map[TemplateKey]struct{}, len(config.EnabledTemplateKeys))
	for _, rawKey := range config.EnabledTemplateKeys {
		enabledTemplates[TemplateKey(strings.TrimSpace(rawKey))] = struct{}{}
	}
	parsedBaseURL, err := url.Parse(config.BaseURL)
	if err != nil || parsedBaseURL.Host == "" || (parsedBaseURL.Scheme != "http" && parsedBaseURL.Scheme != "https") ||
		parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" || parsedBaseURL.Fragment != "" {
		return nil, errors.New("WhatsApp Graph base URL is invalid")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("WhatsApp Graph timeout must be positive")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: config.Timeout,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: config.Timeout,
			},
		}
	} else if httpClient.Timeout <= 0 {
		return nil, errors.New("provided WhatsApp HTTP client must have a timeout")
	}
	configuredHTTPClient := *httpClient
	if configuredHTTPClient.CheckRedirect == nil {
		configuredHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return &Client{
		baseURL: config.BaseURL, graphVersion: config.GraphVersion,
		phoneNumberID: config.PhoneNumberID, businessAccountID: config.BusinessAccountID,
		accessToken: config.AccessToken, httpClient: &configuredHTTPClient,
		enabledTemplates: enabledTemplates,
	}, nil
}

type SendResult struct {
	MessageID string
}

// RequestError identifies a failure that occurred before any Graph request was
// issued. Callers may safely classify it as permanent without risking a
// duplicate send.
type RequestError struct {
	Cause error
}

func (err *RequestError) Error() string { return "WhatsApp request is invalid: " + err.Cause.Error() }
func (err *RequestError) Unwrap() error { return err.Cause }

type graphSendResponse struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
}

func (client *Client) SendTemplate(ctx context.Context, message TemplateMessage) (SendResult, error) {
	if _, enabled := client.enabledTemplates[message.Key]; !enabled {
		return SendResult{}, &RequestError{Cause: fmt.Errorf("template %q is not enabled", message.Key)}
	}
	payload, err := buildTemplateRequest(message)
	if err != nil {
		return SendResult{}, &RequestError{Cause: err}
	}
	return client.sendMessage(ctx, payload)
}

// SendText uses the same single-attempt transport and ambiguity classification as templates.
func (client *Client) SendText(ctx context.Context, to, body, correlation string) (SendResult, error) {
	normalized, err := normalizeInternationalE164(to)
	correlationID, correlationErr := uuid.Parse(correlation)
	if err != nil || strings.TrimSpace(body) == "" || len([]rune(body)) > 4096 || correlationErr != nil || correlationID == uuid.Nil {
		return SendResult{}, &RequestError{Cause: errors.New("invalid WhatsApp text message")}
	}
	return client.sendMessage(ctx, map[string]any{"messaging_product": "whatsapp", "to": strings.TrimPrefix(normalized, "+"),
		"type": "text", "text": map[string]any{"body": body, "preview_url": false}, "biz_opaque_callback_data": correlation})
}

type URLButton struct {
	Label string
	URL   string
}

// SendURLButton uses the same single-attempt transport and callback correlation
// as text. A rejection never triggers a second send in a different format.
func (client *Client) SendURLButton(ctx context.Context, to, body string, button URLButton, correlation string) (SendResult, error) {
	normalized, err := normalizeInternationalE164(to)
	id, idErr := uuid.Parse(correlation)
	u, urlErr := url.Parse(button.URL)
	if err != nil || idErr != nil || id == uuid.Nil || !utf8.ValidString(body) || strings.TrimSpace(body) == "" || utf8.RuneCountInString(body) > 1024 ||
		!utf8.ValidString(button.Label) || strings.TrimSpace(button.Label) == "" || utf8.RuneCountInString(button.Label) > 20 ||
		urlErr != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || containsControlCharacter(button.URL) {
		return SendResult{}, &RequestError{Cause: errors.New("invalid WhatsApp URL button message")}
	}
	return client.sendMessage(ctx, map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual", "to": strings.TrimPrefix(normalized, "+"),
		"type": "interactive", "biz_opaque_callback_data": correlation,
		"interactive": map[string]any{"type": "cta_url", "body": map[string]string{"text": body},
			"action": map[string]any{"name": "cta_url", "parameters": map[string]string{"display_text": button.Label, "url": button.URL}}},
	})
}

func (client *Client) sendMessage(ctx context.Context, payload any) (SendResult, error) {
	responseBody, err := client.postMessage(ctx, payload)
	if err != nil {
		return SendResult{}, err
	}
	var decoded graphSendResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil || len(decoded.Messages) != 1 || strings.TrimSpace(decoded.Messages[0].ID) == "" {
		return SendResult{}, &TransportError{Cause: errors.New("WhatsApp Graph success response has no message ID"), Ambiguous: true}
	}
	return SendResult{MessageID: decoded.Messages[0].ID}, nil
}

type TypingSender interface {
	SendTyping(context.Context, string) error
}

// SendTyping marks the verified inbound WAMID read and signals typing. This is
// one request with no internal retry, on the configured receiving phone only.
func (client *Client) SendTyping(ctx context.Context, inboundMessageID string) error {
	if strings.TrimSpace(inboundMessageID) == "" || len(inboundMessageID) > 512 {
		return &RequestError{Cause: errors.New("invalid WhatsApp typing source")}
	}
	response, err := client.postMessage(ctx, map[string]any{"messaging_product": "whatsapp", "status": "read", "message_id": inboundMessageID, "typing_indicator": map[string]string{"type": "text"}})
	if err != nil {
		return err
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err = json.Unmarshal(response, &result); err != nil || !result.Success {
		return errors.New("WhatsApp typing was not acknowledged")
	}
	return nil
}

func (client *Client) postMessage(ctx context.Context, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &RequestError{Cause: fmt.Errorf("encode message request: %w", err)}
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.baseURL+"/"+client.graphVersion+"/"+client.phoneNumberID+"/messages",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, &RequestError{Cause: fmt.Errorf("create message request: %w", err)}
	}
	request.Header.Set("Authorization", "Bearer "+client.accessToken)
	request.Header.Set("Content-Type", "application/json")
	var wroteRequest atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
		wroteRequest.Store(true)
	}}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, &TransportError{Cause: err, Ambiguous: wroteRequest.Load()}
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxGraphResponseBytes+1))
	if readErr != nil {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, decodeGraphError(response.StatusCode, response.Header, nil)
		}
		return nil, &TransportError{Cause: readErr, Ambiguous: true}
	}
	if len(responseBody) > maxGraphResponseBytes {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, decodeGraphError(response.StatusCode, response.Header, nil)
		}
		return nil, &TransportError{Cause: errors.New("WhatsApp Graph response exceeds limit"), Ambiguous: response.StatusCode < 300}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeGraphError(response.StatusCode, response.Header, responseBody)
	}
	return responseBody, nil
}

type ErrorClass string

const (
	ErrorClassPermanent ErrorClass = "permanent"
	ErrorClassTransient ErrorClass = "transient"
	ErrorClassRateLimit ErrorClass = "rate_limit"
	ErrorClassAuth      ErrorClass = "auth"
)

type GraphError struct {
	HTTPStatus  int
	Code        int
	Subcode     int
	Type        string
	Class       ErrorClass
	RetryAfter  time.Duration
	SafeMessage string
}

func (err *GraphError) Error() string {
	return fmt.Sprintf("WhatsApp Graph request failed: status=%d code=%d class=%s", err.HTTPStatus, err.Code, err.Class)
}

type TransportError struct {
	Cause     error
	Ambiguous bool
}

func (err *TransportError) Error() string {
	return "WhatsApp Graph transport failed: " + err.Cause.Error()
}
func (err *TransportError) Unwrap() error { return err.Cause }

func decodeGraphError(status int, headers http.Header, body []byte) error {
	var envelope struct {
		Error struct {
			Message      string `json:"message"`
			Type         string `json:"type"`
			Code         int    `json:"code"`
			ErrorSubcode int    `json:"error_subcode"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	class := ErrorClassPermanent
	switch {
	case status == http.StatusTooManyRequests || isMetaRateLimitCode(envelope.Error.Code):
		class = ErrorClassRateLimit
	case status == http.StatusUnauthorized || status == http.StatusForbidden || envelope.Error.Code == 10 || envelope.Error.Code == 190:
		class = ErrorClassAuth
	case status >= 500 || envelope.Error.Code == 1 || envelope.Error.Code == 2:
		class = ErrorClassTransient
	}
	retryAfter := parseRetryAfter(headers.Get("Retry-After"), time.Now())
	message := strings.TrimSpace(envelope.Error.Message)
	if len(message) > 512 {
		message = message[:512]
	}
	return &GraphError{
		HTTPStatus: status, Code: envelope.Error.Code, Subcode: envelope.Error.ErrorSubcode,
		Type: envelope.Error.Type, Class: class, RetryAfter: retryAfter, SafeMessage: message,
	}
}

func isMetaRateLimitCode(code int) bool {
	switch code {
	case 4, 17, 32, 613, 80007:
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func decimalPathSegment(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validGraphAPIVersion(value string) bool {
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	return strings.HasPrefix(value, "v") && len(parts) == 2 &&
		decimalPathSegment(parts[0]) && decimalPathSegment(parts[1])
}
