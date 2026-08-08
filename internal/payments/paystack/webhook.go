package paystackclient

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"booking/go-server/internal/payments"
)

func (c *Client) VerifyAndDecodeWebhook(rawBody []byte, headers http.Header) (payments.VerifiedEvent, error) {
	if len(rawBody) == 0 {
		return payments.VerifiedEvent{}, errors.New("empty paystack webhook body")
	}
	received, err := hex.DecodeString(strings.TrimSpace(headers.Get("x-paystack-signature")))
	if err != nil || len(received) == 0 {
		return payments.VerifiedEvent{}, errors.New("invalid paystack webhook signature encoding")
	}
	mac := hmac.New(sha512.New, []byte(c.secretKey))
	_, _ = mac.Write(rawBody)
	if !hmac.Equal(received, mac.Sum(nil)) {
		return payments.VerifiedEvent{}, errors.New("invalid paystack webhook signature")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawBody))
	decoder.UseNumber()
	var payload struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	if err := decoder.Decode(&payload); err != nil || strings.TrimSpace(payload.Event) == "" {
		return payments.VerifiedEvent{}, errors.New("invalid paystack webhook JSON")
	}
	reference, _ := payload.Data["reference"].(string)
	normalized := map[string]any{"event": payload.Event}
	for _, key := range []string{
		"reference", "status", "amount", "currency", "paid_at", "paidAt", "transferred_at",
		"transfer_code", "gateway_response", "channel", "fees", "created_at", "updated_at",
		"transaction_reference", "refund_reference", "refund_amount", "resolution", "id", "reason",
	} {
		if value, exists := payload.Data[key]; exists {
			normalized[key] = value
		}
	}
	if transaction, ok := payload.Data["transaction"].(map[string]any); ok {
		if nestedReference, ok := transaction["reference"].(string); ok && strings.TrimSpace(nestedReference) != "" {
			normalized["reference"] = strings.TrimSpace(nestedReference)
			reference = nestedReference
		}
	}
	if merchantReference, ok := payload.Data["merchant_transaction_reference"].(string); ok {
		normalized["merchant_reference"] = merchantReference
	}
	providerEventID := ""
	for _, key := range []string{"refund_reference", "id", "transfer_code", "customer_code", "subscription_code", "invoice_code"} {
		if value, exists := payload.Data[key]; exists && strings.TrimSpace(fmt.Sprint(value)) != "" && fmt.Sprint(value) != "<nil>" {
			providerEventID = payload.Event + ":" + fmt.Sprint(value)
			break
		}
	}
	if providerEventID == "" && strings.TrimSpace(reference) != "" && !strings.HasPrefix(payload.Event, "refund.") {
		providerEventID = strings.TrimSpace(reference) + ":" + payload.Event
	}
	return payments.VerifiedEvent{
		ProviderEventID: providerEventID,
		EventType:       payload.Event,
		Normalized:      normalized,
	}, nil
}
