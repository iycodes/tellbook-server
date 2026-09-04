package paystackclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"booking/go-server/internal/payments"
)

var _ payments.RefundProvider = (*Client)(nil)

func (c *Client) InitiateRefund(ctx context.Context, request payments.RefundRequest) (payments.RefundResult, error) {
	if request.Provider != "paystack" || request.TransactionReference == "" || request.AmountMinor <= 0 ||
		len(request.CurrencyCode) != 3 {
		return payments.RefundResult{}, errors.New("invalid Paystack refund request")
	}
	input := map[string]any{
		"transaction":   request.TransactionReference,
		"amount":        int64(request.AmountMinor),
		"currency":      request.CurrencyCode,
		"customer_note": strings.TrimSpace(request.Reason),
		"merchant_note": "Tellbook refund " + request.RequestID.String(),
	}
	var response responseEnvelope[struct {
		ID       json.Number `json:"id"`
		Status   string      `json:"status"`
		Currency string      `json:"currency"`
		Amount   json.Number `json:"amount"`
	}]
	if err := c.doRequest(ctx, http.MethodPost, "/refund", input, &response); err != nil {
		return payments.RefundResult{}, err
	}
	providerReference := response.Data.ID.String()
	amount, err := strconv.ParseInt(response.Data.Amount.String(), 10, 64)
	if err != nil || providerReference == "" || amount != int64(request.AmountMinor) ||
		!strings.EqualFold(response.Data.Currency, request.CurrencyCode) {
		return payments.RefundResult{}, errors.New("Paystack returned invalid refund evidence")
	}
	status := strings.ToLower(strings.TrimSpace(response.Data.Status))
	result := payments.RefundResult{ProviderReference: providerReference, ProviderStatus: status}
	switch status {
	case "processed", "successful", "success":
		result.Status = payments.RefundInitiationSuccessful
	case "pending", "processing", "queued":
		result.Status = payments.RefundInitiationPending
	default:
		return payments.RefundResult{}, errors.New("Paystack returned an unsupported refund status")
	}
	return result, nil
}
