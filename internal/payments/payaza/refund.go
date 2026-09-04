package payaza

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"booking/go-server/internal/payments"
)

var _ payments.RefundProvider = (*Client)(nil)

func (c *Client) InitiateRefund(ctx context.Context, request payments.RefundRequest) (payments.RefundResult, error) {
	if request.Provider != "payaza" || request.TransactionReference == "" || request.AmountMinor <= 0 {
		return payments.RefundResult{}, errors.New("invalid Payaza refund request")
	}
	amount, err := newDecimalNumber(int64(request.AmountMinor), request.CurrencyExponent)
	if err != nil {
		return payments.RefundResult{}, err
	}
	input := map[string]any{
		"transaction_reference": request.TransactionReference,
		"refund_amount":         amount,
		"refund_reason":         strings.TrimSpace(request.Reason),
	}
	var response struct {
		Data struct {
			RefundReference  string `json:"refund_transaction_reference"`
			PaymentReference string `json:"payment_transaction_reference"`
			Status           string `json:"status"`
			Successful       bool   `json:"successful"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/refund-chargeback/refund/merchant/api/refund", false, input, &response); err != nil {
		return payments.RefundResult{}, err
	}
	if !response.Data.Successful || strings.TrimSpace(response.Data.RefundReference) == "" ||
		response.Data.PaymentReference != request.TransactionReference {
		return payments.RefundResult{}, errors.New("Payaza returned invalid refund evidence")
	}
	return payments.RefundResult{
		ProviderReference: response.Data.RefundReference,
		ProviderStatus:    strings.ToLower(strings.TrimSpace(response.Data.Status)),
		Status:            payments.RefundInitiationSuccessful,
	}, nil
}
