package payaza

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"booking/go-server/internal/payments"
)

func (c *Client) VerifyAndDecodeWebhook(rawBody []byte, headers http.Header) (payments.VerifiedEvent, error) {
	if len(rawBody) == 0 {
		return payments.VerifiedEvent{}, errors.New("empty payaza webhook body")
	}
	received, err := base64.StdEncoding.DecodeString(strings.TrimSpace(headers.Get("x-payaza-signature")))
	if err != nil || len(received) == 0 {
		return payments.VerifiedEvent{}, errors.New("invalid payaza webhook signature encoding")
	}
	mac := hmac.New(sha512.New, []byte(c.secretKey))
	_, _ = mac.Write(rawBody)
	if !hmac.Equal(received, mac.Sum(nil)) {
		return payments.VerifiedEvent{}, errors.New("invalid payaza webhook signature")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawBody))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return payments.VerifiedEvent{}, errors.New("invalid payaza webhook JSON")
	}
	transactionType, _ := payload["transaction_type"].(string)
	transactionStatus, _ := payload["transaction_status"].(string)
	eventType := strings.ToLower(strings.TrimSpace(transactionType))
	if eventType == "" {
		eventType = "collection"
	}
	if transactionStatus != "" {
		eventType += "." + strings.ToLower(strings.ReplaceAll(strings.TrimSpace(transactionStatus), " ", "_"))
	}
	normalized := make(map[string]any)
	for _, key := range []string{
		"transaction_reference", "merchant_reference", "merchant_transaction_reference", "transaction_type", "transaction_status",
		"status", "status_reason", "response_code", "response_message", "currency", "currency_code",
		"amount_received", "request_amount", "transaction_fee", "amount_validation", "is_reversed",
		"initiated_date", "current_status_date", "channel", "session_id",
	} {
		if value, exists := payload[key]; exists {
			normalized[key] = value
		}
	}
	return payments.VerifiedEvent{
		ProviderEventID: "",
		EventType:       eventType,
		Normalized:      normalized,
	}, nil
}
