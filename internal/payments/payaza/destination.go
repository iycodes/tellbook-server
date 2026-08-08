package payaza

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"booking/go-server/internal/payments"
)

const (
	banksPath   = "/payaza-account/api/v1/mainaccounts/merchant/banks/"
	enquiryPath = "/payaza-account/api/v1/mainaccounts/merchant/provider/enquiry"
)

// RoutedDestinationClient keeps test financial operations on test credentials while
// allowing the read-only institution directory to use a separately configured client.
type RoutedDestinationClient struct {
	directory   *Client
	transaction *Client
}

var _ payments.DestinationProvider = (*RoutedDestinationClient)(nil)

func NewRoutedDestinationClient(directory, transaction *Client) (*RoutedDestinationClient, error) {
	if directory == nil || transaction == nil {
		return nil, errors.New("payaza destination clients are required")
	}
	return &RoutedDestinationClient{directory: directory, transaction: transaction}, nil
}

func (c *RoutedDestinationClient) ListDestinations(ctx context.Context, query payments.DestinationQuery) ([]payments.DestinationOption, error) {
	return c.directory.ListDestinations(ctx, query)
}

func (c *RoutedDestinationClient) ResolveDestination(ctx context.Context, input payments.ResolveDestinationInput) (payments.ResolvedDestination, error) {
	return c.transaction.ResolveDestination(ctx, input)
}

func (c *RoutedDestinationClient) CreateProviderRecipient(ctx context.Context, destination payments.ResolvedDestination) (payments.ProviderRecipient, error) {
	return c.transaction.CreateProviderRecipient(ctx, destination)
}

func (c *Client) ListDestinations(ctx context.Context, query payments.DestinationQuery) ([]payments.DestinationOption, error) {
	currency := strings.ToUpper(strings.TrimSpace(query.CurrencyCode))
	if currency == "" || (query.Rail != "bank_account" && query.Rail != "mobile_money_wallet") {
		return nil, errors.New("invalid payaza destination query")
	}
	var response struct {
		Status bool `json:"status"`
		Data   []struct {
			Name             string `json:"name"`
			Code             string `json:"code"`
			Active           bool   `json:"active"`
			Type             string `json:"type"`
			CountryCode      string `json:"country_code"`
			CurrencyCode     string `json:"currency_code"`
			BlockTransaction bool   `json:"block_transaction"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, banksPath+url.PathEscape(currency), true, nil, &response); err != nil {
		return nil, err
	}
	if !response.Status {
		return nil, errors.New("payaza destination directory query was unsuccessful")
	}
	options := make([]payments.DestinationOption, 0, len(response.Data))
	for _, bank := range response.Data {
		isMobileMoney := strings.EqualFold(bank.Type, "mobile_money")
		if !bank.Active || bank.BlockTransaction || strings.TrimSpace(bank.Code) == "" || strings.TrimSpace(bank.Name) == "" ||
			(bank.CurrencyCode != "" && !strings.EqualFold(bank.CurrencyCode, currency)) ||
			(query.Rail == "mobile_money_wallet") != isMobileMoney {
			continue
		}
		options = append(options, payments.DestinationOption{Code: strings.TrimSpace(bank.Code), Name: strings.TrimSpace(bank.Name)})
	}
	return options, nil
}

func (c *Client) ResolveDestination(ctx context.Context, input payments.ResolveDestinationInput) (payments.ResolvedDestination, error) {
	if input.CurrencyCode == "" || input.Institution == "" || input.Identifier == "" {
		return payments.ResolvedDestination{}, errors.New("invalid payaza destination resolution")
	}
	request := map[string]any{"service_payload": map[string]string{
		"currency": strings.ToUpper(input.CurrencyCode), "bank_code": strings.TrimSpace(input.Institution),
		"account_number": strings.TrimSpace(input.Identifier),
	}}
	var response struct {
		ResponseCode    int `json:"response_code"`
		ResponseContent struct {
			AccountNumber string `json:"account_number"`
			BankCode      string `json:"bank_code"`
			AccountName   string `json:"account_name"`
			AccountStatus string `json:"account_status"`
		} `json:"response_content"`
	}
	if err := c.doJSON(ctx, http.MethodPost, enquiryPath, true, request, &response); err != nil {
		return payments.ResolvedDestination{}, err
	}
	if response.ResponseCode != 200 || response.ResponseContent.AccountName == "" ||
		!strings.EqualFold(response.ResponseContent.AccountStatus, "active") {
		return payments.ResolvedDestination{}, errors.New("payaza could not verify the payout destination")
	}
	return payments.ResolvedDestination{
		CountryCode:     strings.ToUpper(strings.TrimSpace(input.CountryCode)),
		CurrencyCode:    strings.ToUpper(strings.TrimSpace(input.CurrencyCode)),
		Rail:            strings.TrimSpace(input.Rail),
		InstitutionCode: response.ResponseContent.BankCode,
		InstitutionName: strings.TrimSpace(input.InstitutionName),
		Identifier:      response.ResponseContent.AccountNumber,
		AccountName:     response.ResponseContent.AccountName,
	}, nil
}

func (c *Client) CreateProviderRecipient(_ context.Context, destination payments.ResolvedDestination) (payments.ProviderRecipient, error) {
	if destination.InstitutionCode == "" || destination.Identifier == "" || destination.AccountName == "" {
		return payments.ProviderRecipient{}, errors.New("invalid verified payaza destination")
	}
	return payments.ProviderRecipient{
		CountryCode: destination.CountryCode, CurrencyCode: destination.CurrencyCode, Rail: destination.Rail,
		InstitutionCode: destination.InstitutionCode, InstitutionName: destination.InstitutionName,
		Identifier: destination.Identifier, AccountName: destination.AccountName,
	}, nil
}
