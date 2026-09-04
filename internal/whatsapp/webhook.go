package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxWebhookBodyBytes = 1 << 20
	defaultMaxWebhookEvents    = 200
)

type WebhookConfig struct {
	AppSecret     string
	VerifyToken   string
	BusinessID    string
	PhoneNumberID string
	MaxBodyBytes  int64
	MaxEvents     int
}

type WebhookReceipt struct {
	DedupeKey         string
	BusinessID        string
	PhoneNumberID     string
	EventKind         string
	MessageID         string
	MessageStatus     string
	ProviderTimestamp *time.Time
	ProviderErrorCode string
	ProcessingStatus  string
	control           inboundControl
}

type inboundControl struct {
	kind   string
	token  string
	sender string
}

type WebhookReceiptStore interface {
	StoreWebhookReceipts(context.Context, []WebhookReceipt) error
}

type WebhookHandler struct {
	config WebhookConfig
	store  WebhookReceiptStore
	logger *slog.Logger
}

func NewWebhookHandler(config WebhookConfig, store WebhookReceiptStore, logger *slog.Logger) (*WebhookHandler, error) {
	config.AppSecret = strings.TrimSpace(config.AppSecret)
	config.VerifyToken = strings.TrimSpace(config.VerifyToken)
	config.BusinessID = strings.TrimSpace(config.BusinessID)
	config.PhoneNumberID = strings.TrimSpace(config.PhoneNumberID)
	if config.AppSecret == "" || config.VerifyToken == "" || config.BusinessID == "" || config.PhoneNumberID == "" {
		return nil, errors.New("complete Meta webhook configuration is required")
	}
	if store == nil {
		return nil, errors.New("Meta webhook receipt store is required")
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = defaultMaxWebhookBodyBytes
	}
	if config.MaxEvents == 0 {
		config.MaxEvents = defaultMaxWebhookEvents
	}
	if config.MaxBodyBytes < 1024 || config.MaxBodyBytes > 4<<20 {
		return nil, errors.New("Meta webhook body limit must be between 1 KiB and 4 MiB")
	}
	if config.MaxEvents < 1 || config.MaxEvents > 1000 {
		return nil, errors.New("Meta webhook event limit must be between 1 and 1000")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &WebhookHandler{config: config, store: store, logger: logger}, nil
}

func (handler *WebhookHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.verifySubscription(response, request)
	case http.MethodPost:
		handler.acceptEvent(response, request)
	default:
		response.Header().Set("Allow", "GET, POST")
		writeWebhookJSON(response, http.StatusMethodNotAllowed, `{"error":{"code":"method_not_allowed","message":"Method is not allowed."}}`)
	}
}

func (handler *WebhookHandler) verifySubscription(response http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	mode := query.Get("hub.mode")
	token := query.Get("hub.verify_token")
	challenge := query.Get("hub.challenge")
	if mode != "subscribe" || challenge == "" || len(challenge) > 1024 ||
		subtle.ConstantTimeCompare([]byte(token), []byte(handler.config.VerifyToken)) != 1 {
		writeWebhookJSON(response, http.StatusForbidden, `{"error":{"code":"verification_failed","message":"Webhook verification failed."}}`)
		return
	}
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte(challenge))
}

func (handler *WebhookHandler) acceptEvent(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, handler.config.MaxBodyBytes)
	rawBody, err := io.ReadAll(request.Body)
	if err != nil {
		writeWebhookJSON(response, http.StatusRequestEntityTooLarge, `{"error":{"code":"invalid_webhook","message":"Webhook body is invalid."}}`)
		return
	}
	if !validMetaSignature(rawBody, request.Header.Get("X-Hub-Signature-256"), handler.config.AppSecret) {
		writeWebhookJSON(response, http.StatusUnauthorized, `{"error":{"code":"verification_failed","message":"Webhook verification failed."}}`)
		return
	}
	receipts, ignoredPhones, unknownEvents, err := normalizeWebhookReceipts(rawBody, handler.config, handler.config.MaxEvents)
	if err != nil {
		writeWebhookJSON(response, http.StatusBadRequest, `{"error":{"code":"invalid_webhook","message":"Webhook body is invalid."}}`)
		return
	}
	if len(receipts) > 0 {
		if err := handler.store.StoreWebhookReceipts(request.Context(), receipts); err != nil {
			handler.logger.Error("store Meta WhatsApp webhook receipts", "error", err)
			writeWebhookJSON(response, http.StatusServiceUnavailable, `{"error":{"code":"webhook_storage_failed","message":"Webhook could not be stored."}}`)
			return
		}
	}
	if ignoredPhones > 0 {
		handler.logger.Warn("ignored Meta WhatsApp events for an unconfigured phone", "event_count", ignoredPhones)
	}
	if unknownEvents > 0 {
		handler.logger.Warn("ignored unsupported Meta WhatsApp events", "event_count", unknownEvents)
	}
	writeWebhookJSON(response, http.StatusOK, `{"accepted":true}`)
}

func validMetaSignature(body []byte, signature, appSecret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

type webhookEnvelope struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				MessagingProduct string `json:"messaging_product"`
				Metadata         struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Statuses []struct {
					ID        string `json:"id"`
					Status    string `json:"status"`
					Timestamp string `json:"timestamp"`
					Errors    []struct {
						Code int `json:"code"`
					} `json:"errors"`
				} `json:"statuses"`
				Messages []struct {
					ID        string `json:"id"`
					From      string `json:"from"`
					Timestamp string `json:"timestamp"`
					Type      string `json:"type"`
					Text      struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func normalizeWebhookReceipts(body []byte, config WebhookConfig, maxEvents int) ([]WebhookReceipt, int, int, error) {
	var envelope webhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, 0, 0, err
	}
	if envelope.Object != "whatsapp_business_account" {
		return nil, 0, 1, nil
	}
	receipts := make([]WebhookReceipt, 0)
	ignoredPhones := 0
	unknownEvents := 0
	structuralCount := len(envelope.Entry)
	if structuralCount > maxEvents {
		return nil, 0, 0, errors.New("Meta webhook structure exceeds limit")
	}
	for _, entry := range envelope.Entry {
		structuralCount += len(entry.Changes) + countEntryEvents(entry)
		if structuralCount > maxEvents {
			return nil, 0, 0, errors.New("Meta webhook structure exceeds limit")
		}
		if entry.ID != config.BusinessID {
			unknownEvents += countEntryEvents(entry)
			continue
		}
		for _, change := range entry.Changes {
			if change.Field != "messages" || change.Value.MessagingProduct != "whatsapp" {
				unknownEvents += len(change.Value.Statuses) + len(change.Value.Messages)
				continue
			}
			eventCount := len(change.Value.Statuses) + len(change.Value.Messages)
			if change.Value.Metadata.PhoneNumberID != config.PhoneNumberID {
				ignoredPhones += eventCount
				continue
			}
			for _, status := range change.Value.Statuses {
				receipt, err := normalizeStatusReceipt(entry.ID, change.Value.Metadata.PhoneNumberID, status)
				if err != nil {
					return nil, 0, 0, err
				}
				receipts = append(receipts, receipt)
			}
			for _, message := range change.Value.Messages {
				receipt, err := normalizeInboundReceipt(
					entry.ID, change.Value.Metadata.PhoneNumberID,
					message.ID, message.Timestamp, message.From, message.Type, message.Text.Body,
				)
				if err != nil {
					return nil, 0, 0, err
				}
				receipts = append(receipts, receipt)
			}
		}
	}
	return receipts, ignoredPhones, unknownEvents, nil
}

func countEntryEvents(entry struct {
	ID      string `json:"id"`
	Changes []struct {
		Field string `json:"field"`
		Value struct {
			MessagingProduct string `json:"messaging_product"`
			Metadata         struct {
				PhoneNumberID string `json:"phone_number_id"`
			} `json:"metadata"`
			Statuses []struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				Timestamp string `json:"timestamp"`
				Errors    []struct {
					Code int `json:"code"`
				} `json:"errors"`
			} `json:"statuses"`
			Messages []struct {
				ID        string `json:"id"`
				From      string `json:"from"`
				Timestamp string `json:"timestamp"`
				Type      string `json:"type"`
				Text      struct {
					Body string `json:"body"`
				} `json:"text"`
			} `json:"messages"`
		} `json:"value"`
	} `json:"changes"`
}) int {
	count := 0
	for _, change := range entry.Changes {
		count += len(change.Value.Statuses) + len(change.Value.Messages)
	}
	return count
}

func normalizeStatusReceipt(businessID, phoneNumberID string, status struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
	Errors    []struct {
		Code int `json:"code"`
	} `json:"errors"`
}) (WebhookReceipt, error) {
	messageID := boundedWebhookValue(status.ID, 512)
	messageStatus := boundedWebhookValue(strings.ToLower(status.Status), 40)
	if messageID == "" || messageStatus == "" {
		return WebhookReceipt{}, errors.New("Meta status webhook is missing identity")
	}
	timestamp, err := parseProviderTimestamp(status.Timestamp)
	if err != nil {
		return WebhookReceipt{}, err
	}
	errorCode := ""
	if len(status.Errors) > 0 && status.Errors[0].Code != 0 {
		errorCode = strconv.Itoa(status.Errors[0].Code)
	}
	dedupeKey := receiptDedupeKey(phoneNumberID, messageID, messageStatus, strconv.FormatInt(timestamp.Unix(), 10), errorCode)
	processingStatus := "completed"
	if actionableMessageStatus(messageStatus) {
		processingStatus = "pending"
	}
	return WebhookReceipt{
		DedupeKey: dedupeKey, BusinessID: businessID, PhoneNumberID: phoneNumberID,
		EventKind: "status", MessageID: messageID, MessageStatus: messageStatus,
		ProviderTimestamp: &timestamp, ProviderErrorCode: errorCode, ProcessingStatus: processingStatus,
	}, nil
}

func actionableMessageStatus(status string) bool {
	switch status {
	case "sent", "delivered", "read", "failed", "deleted":
		return true
	default:
		return false
	}
}

func normalizeInboundReceipt(
	businessID, phoneNumberID, messageID, rawTimestamp, sender, messageType, body string,
) (WebhookReceipt, error) {
	messageID = boundedWebhookValue(messageID, 512)
	if messageID == "" {
		return WebhookReceipt{}, errors.New("Meta inbound webhook is missing identity")
	}
	timestamp, err := parseProviderTimestamp(rawTimestamp)
	if err != nil {
		return WebhookReceipt{}, err
	}
	dedupeKey := receiptDedupeKey(phoneNumberID, messageID)
	return WebhookReceipt{
		DedupeKey: dedupeKey, BusinessID: businessID, PhoneNumberID: phoneNumberID,
		EventKind: "inbound_message", MessageID: messageID,
		ProviderTimestamp: &timestamp, ProcessingStatus: "completed",
		control: classifyInboundControl(sender, messageType, body),
	}, nil
}

func classifyInboundControl(sender, messageType, body string) inboundControl {
	if !strings.EqualFold(strings.TrimSpace(messageType), "text") {
		return inboundControl{}
	}
	sender = boundedWebhookValue(sender, 32)
	if sender == "" {
		return inboundControl{}
	}
	command := strings.TrimSpace(body)
	if strings.EqualFold(command, "STOP") {
		return inboundControl{kind: "stop", sender: sender}
	}
	if strings.EqualFold(command, "START") {
		return inboundControl{kind: "start", sender: sender}
	}
	parts := strings.Fields(command)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "VERIFY") || !validVerificationToken(parts[1]) {
		return inboundControl{}
	}
	return inboundControl{kind: "verify", token: parts[1], sender: sender}
}

func validVerificationToken(value string) bool {
	if len(value) < 32 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func receiptDedupeKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(strconv.Itoa(len(part))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(part))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func parseProviderTimestamp(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, errors.New("Meta webhook timestamp is invalid")
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func boundedWebhookValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return ""
		}
	}
	return value
}

func writeWebhookJSON(response http.ResponseWriter, status int, body string) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_, _ = response.Write([]byte(body))
}
