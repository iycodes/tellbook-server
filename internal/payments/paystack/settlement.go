package paystackclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"
)

const (
	settlementPageSize = 50
	maxSettlementPages = 100
)

var (
	_ payments.SettlementProvider      = (*Client)(nil)
	_ payments.PayoutLiquidityProvider = (*Client)(nil)
)

type paystackSettlement struct {
	ID             int64  `json:"id"`
	Status         string `json:"status"`
	Currency       string `json:"currency"`
	SettlementDate string `json:"settlement_date"`
	UpdatedAt      string `json:"updatedAt"`
}

type paystackSettlementTransaction struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
}

func (c *Client) ListSettlementEvidence(ctx context.Context, query payments.SettlementQuery) ([]payments.SettlementEvidence, error) {
	if query.From.IsZero() || query.To.IsZero() || query.To.Before(query.From) {
		return nil, errors.New("invalid Paystack settlement query")
	}

	settlements, err := c.listSuccessfulSettlements(ctx, query)
	if err != nil {
		return nil, err
	}
	evidence := make([]payments.SettlementEvidence, 0)
	for _, settlement := range settlements {
		availableAt := parseRFC3339(settlement.UpdatedAt)
		if availableAt == nil {
			availableAt = parseRFC3339(settlement.SettlementDate)
		}
		if settlement.ID <= 0 || availableAt == nil || !strings.EqualFold(settlement.Status, "success") {
			return nil, errors.New("Paystack returned invalid successful settlement metadata")
		}
		transactions, err := c.listSettlementTransactions(ctx, settlement.ID)
		if err != nil {
			return nil, err
		}
		for _, transaction := range transactions {
			if !strings.EqualFold(strings.TrimSpace(transaction.Status), "success") {
				continue
			}
			reference := strings.TrimSpace(transaction.Reference)
			currency := strings.ToUpper(strings.TrimSpace(transaction.Currency))
			if reference == "" || transaction.Amount <= 0 || len(currency) != 3 || currency != strings.ToUpper(strings.TrimSpace(settlement.Currency)) {
				return nil, errors.New("Paystack returned invalid settlement transaction evidence")
			}
			evidence = append(evidence, payments.SettlementEvidence{
				Provider: "paystack", SettlementReference: strconv.FormatInt(settlement.ID, 10),
				PaymentReference: reference, ProviderStatus: settlement.Status, SettlementStatus: "available",
				AmountMinor: money.Minor(transaction.Amount), CurrencyCode: currency,
				AvailableAt: availableAt.UTC(),
			})
		}
	}
	return evidence, nil
}

func (c *Client) listSuccessfulSettlements(ctx context.Context, query payments.SettlementQuery) ([]paystackSettlement, error) {
	items := make([]paystackSettlement, 0)
	for page := 1; page <= maxSettlementPages; page++ {
		values := url.Values{
			"perPage": {strconv.Itoa(settlementPageSize)}, "page": {strconv.Itoa(page)},
			"status": {"success"}, "subaccount": {"none"},
			"from": {query.From.UTC().Format(time.RFC3339)}, "to": {query.To.UTC().Format(time.RFC3339)},
		}
		var response responseEnvelope[[]paystackSettlement]
		if err := c.doRequest(ctx, http.MethodGet, "/settlement?"+values.Encode(), nil, &response); err != nil {
			return nil, err
		}
		items = append(items, response.Data...)
		if response.Meta.PageCount == 0 || page >= response.Meta.PageCount {
			return items, nil
		}
	}
	return nil, errors.New("Paystack settlement list exceeded pagination limit")
}

func (c *Client) listSettlementTransactions(ctx context.Context, settlementID int64) ([]paystackSettlementTransaction, error) {
	items := make([]paystackSettlementTransaction, 0)
	for page := 1; page <= maxSettlementPages; page++ {
		values := url.Values{"perPage": {strconv.Itoa(settlementPageSize)}, "page": {strconv.Itoa(page)}}
		path := fmt.Sprintf("/settlement/%d/transactions?%s", settlementID, values.Encode())
		var response responseEnvelope[[]paystackSettlementTransaction]
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		items = append(items, response.Data...)
		if response.Meta.PageCount == 0 || page >= response.Meta.PageCount {
			return items, nil
		}
	}
	return nil, errors.New("Paystack settlement transaction list exceeded pagination limit")
}

func (c *Client) AvailablePayoutBalance(ctx context.Context, currencyCode string) (money.Minor, error) {
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	if len(currencyCode) != 3 {
		return 0, errors.New("invalid Paystack balance currency")
	}
	var response responseEnvelope[[]struct {
		Currency string `json:"currency"`
		Balance  int64  `json:"balance"`
	}]
	if err := c.doRequest(ctx, http.MethodGet, "/balance", nil, &response); err != nil {
		return 0, err
	}
	for _, balance := range response.Data {
		if strings.EqualFold(strings.TrimSpace(balance.Currency), currencyCode) {
			if balance.Balance < 0 {
				return 0, errors.New("Paystack returned a negative payout balance")
			}
			return money.Minor(balance.Balance), nil
		}
	}
	return 0, errors.New("Paystack payout balance is unavailable for currency")
}
