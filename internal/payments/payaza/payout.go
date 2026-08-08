package payaza

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"
)

const (
	payoutPath       = "/payout-receptor/payout"
	payoutStatusPath = "/payaza-account/api/v1/mainaccounts/merchant/transaction/"
	mainAccountPath  = "/payaza-account/api/v1/mainaccounts/merchant/enquiry/main"
)

var ErrPayoutNotFound = errors.New("payaza payout not found")

func (c *Client) AvailablePayoutBalance(ctx context.Context, currencyCode string) (money.Minor, error) {
	currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode))
	if len(currencyCode) != 3 {
		return 0, errors.New("invalid payaza payout balance currency")
	}
	var response struct {
		Status bool `json:"status"`
		Data   []struct {
			Status           string      `json:"status"`
			Currency         string      `json:"currency"`
			AccountBalance   json.Number `json:"accountBalance"`
			PayazaAccountRef string      `json:"payazaAccountReference"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, mainAccountPath, true, nil, &response); err != nil {
		return 0, err
	}
	if !response.Status {
		return 0, errors.New("payaza payout balance query was unsuccessful")
	}
	configuredReference := strings.TrimSpace(c.sourceAccounts[currencyCode])
	for _, account := range response.Data {
		if account.Currency != currencyCode || !strings.EqualFold(account.Status, "active") {
			continue
		}
		if configuredReference != "" && strings.TrimSpace(account.PayazaAccountRef) != configuredReference {
			continue
		}
		balance, err := parseProviderAmount(account.AccountBalance, 2)
		if err != nil {
			return 0, fmt.Errorf("parse payaza payout balance: %w", err)
		}
		return balance, nil
	}
	return 0, errors.New("payaza payout account was not found")
}

func (c *Client) InitiatePayout(
	ctx context.Context,
	snapshot payments.PayoutSnapshot,
	recipient payments.ProviderRecipient,
) (payments.PayoutResult, error) {
	if snapshot.Provider != "payaza" || snapshot.Reference == "" || snapshot.AmountMinor <= 0 ||
		snapshot.Rail != "bank_account" || snapshot.CountryCode != "NG" || snapshot.CurrencyCode != "NGN" ||
		recipient.InstitutionCode == "" || recipient.Identifier == "" || recipient.AccountName == "" {
		return payments.PayoutResult{}, errors.New("invalid or unsupported payaza payout")
	}
	if !validTransactionPIN(c.transactionPIN) {
		return payments.PayoutResult{}, errors.New("payaza transaction PIN is not configured")
	}
	if c.payoutSender.Name == "" || c.payoutSender.Phone == "" || c.payoutSender.Address == "" {
		return payments.PayoutResult{}, errors.New("payaza payout sender identity is not configured")
	}
	if !validPayoutNarration(snapshot.Narration) {
		return payments.PayoutResult{}, errors.New("invalid payaza payout narration")
	}
	sourceAccount := strings.TrimSpace(c.sourceAccounts[snapshot.CurrencyCode])
	if sourceAccount == "" {
		return payments.PayoutResult{}, errors.New("payaza source account is not configured for payout currency")
	}
	amount, err := newDecimalNumber(int64(snapshot.AmountMinor), snapshot.CurrencyExponent)
	if err != nil {
		return payments.PayoutResult{}, err
	}
	request := payoutRequest{TransactionType: "nuban"}
	request.ServicePayload.PayoutAmount = amount
	request.ServicePayload.TransactionPIN = json.Number(c.transactionPIN)
	request.ServicePayload.AccountReference = sourceAccount
	request.ServicePayload.Currency = snapshot.CurrencyCode
	request.ServicePayload.Country = "NGA"
	request.ServicePayload.PayoutBeneficiaries = []payoutBeneficiary{{
		CreditAmount: amount, AccountNumber: recipient.Identifier, AccountName: recipient.AccountName,
		BankCode: recipient.InstitutionCode, Narration: snapshot.Narration,
		TransactionReference: snapshot.Reference,
		Sender: payoutSender{
			Name: c.payoutSender.Name, PhoneNumber: c.payoutSender.Phone, Address: c.payoutSender.Address,
		},
	}}
	var response payoutInitiationResponse
	if err := c.doJSON(ctx, http.MethodPost, payoutPath, true, request, &response); err != nil {
		return payments.PayoutResult{}, err
	}
	status := normalizePayoutStatus(response.ResponseContent.TransactionStatus, false)
	if status == payments.PayoutStatusSuccessful {
		status = payments.PayoutStatusPending
	}
	if response.ResponseCode != 200 && status == payments.PayoutStatusPending {
		status = payments.PayoutStatusUnknown
	}
	return payments.PayoutResult{
		ProviderReference: snapshot.Reference,
		ProviderStatus:    response.ResponseContent.ResponseStatus,
		Status:            status,
	}, nil
}

func (c *Client) ReconcilePayout(ctx context.Context, record payments.PayoutRecord) (payments.PayoutReconciliation, error) {
	if record.Provider != "payaza" || record.Reference == "" || record.AmountMinor <= 0 {
		return payments.PayoutReconciliation{}, errors.New("invalid payaza payout record")
	}
	var response payoutStatusResponse
	path := payoutStatusPath + url.PathEscape(record.Reference)
	if err := c.doJSON(ctx, http.MethodGet, path, true, nil, &response); err != nil {
		return payments.PayoutReconciliation{}, err
	}
	if !response.Status {
		if strings.EqualFold(strings.TrimSpace(response.Message), "Transaction does not exist") {
			return payments.PayoutReconciliation{}, ErrPayoutNotFound
		}
		return payments.PayoutReconciliation{}, errors.New("payaza payout status query was unsuccessful")
	}
	expected := record.ExpectedRecipient
	if response.Data.TransactionReference != record.Reference ||
		strings.TrimSpace(response.Data.CreditAccount) != strings.TrimSpace(expected.Identifier) ||
		strings.TrimSpace(response.Data.BankCode) != strings.TrimSpace(expected.InstitutionCode) ||
		!strings.EqualFold(strings.TrimSpace(response.Data.BeneficiaryName), strings.TrimSpace(expected.AccountName)) {
		return payments.PayoutReconciliation{}, errors.New("payaza payout beneficiary does not match the stored destination")
	}
	amount, err := parseProviderAmount(response.Data.TransactionAmount, record.CurrencyExponent)
	if err != nil || amount != record.AmountMinor || response.Data.Currency != record.CurrencyCode {
		return payments.PayoutReconciliation{}, errors.New("payaza payout reconciliation does not match the stored payout")
	}
	status := normalizePayoutStatus(response.Data.TransactionStatus, response.Data.IsReversed)
	var completedAt *time.Time
	if status == payments.PayoutStatusSuccessful || status == payments.PayoutStatusReversed {
		completedAt = parsePayazaTime(response.Data.TransactionDateTime)
	}
	return payments.PayoutReconciliation{
		ProviderStatus: response.Data.TransactionStatus,
		Status:         status,
		AmountMinor:    amount, CurrencyCode: response.Data.Currency,
		CompletedAt: completedAt,
	}, nil
}

func normalizePayoutStatus(value string, reversed bool) payments.PayoutStatus {
	if reversed {
		return payments.PayoutStatusReversed
	}
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "NIP_SUCCESS", "SUCCESS", "SUCCESSFUL":
		return payments.PayoutStatusSuccessful
	case "NIP_FAILURE", "FAILED", "FAILURE":
		return payments.PayoutStatusFailed
	case "09", "TRANSACTION_INITIATED", "PENDING", "PROCESSING":
		return payments.PayoutStatusPending
	default:
		return payments.PayoutStatusUnknown
	}
}

func validTransactionPIN(value string) bool {
	if len(value) != 6 || value[0] == '0' {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func validPayoutNarration(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 25 {
		return false
	}
	for index := range value {
		char := value[index]
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != ' ' {
			return false
		}
	}
	return true
}

type payoutRequest struct {
	TransactionType string `json:"transaction_type"`
	ServicePayload  struct {
		PayoutAmount        decimalNumber       `json:"payout_amount"`
		TransactionPIN      json.Number         `json:"transaction_pin"`
		AccountReference    string              `json:"account_reference"`
		Currency            string              `json:"currency"`
		Country             string              `json:"country"`
		PayoutBeneficiaries []payoutBeneficiary `json:"payout_beneficiaries"`
	} `json:"service_payload"`
}

type payoutBeneficiary struct {
	CreditAmount         decimalNumber `json:"credit_amount"`
	AccountNumber        string        `json:"account_number"`
	AccountName          string        `json:"account_name"`
	BankCode             string        `json:"bank_code"`
	Narration            string        `json:"narration"`
	TransactionReference string        `json:"transaction_reference"`
	Sender               payoutSender  `json:"sender"`
}

type payoutSender struct {
	Name        string `json:"sender_name"`
	PhoneNumber string `json:"sender_phone_number"`
	Address     string `json:"sender_address"`
}

type payoutInitiationResponse struct {
	ResponseCode    int `json:"response_code"`
	ResponseContent struct {
		TransactionStatus string `json:"transaction_status"`
		ResponseStatus    string `json:"response_status"`
	} `json:"response_content"`
}

type payoutStatusResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		TransactionDateTime  string      `json:"transactionDateTime"`
		TransactionReference string      `json:"transactionReference"`
		CreditAccount        string      `json:"creditAccount"`
		BankCode             string      `json:"bankCode"`
		BeneficiaryName      string      `json:"beneficiaryName"`
		TransactionAmount    json.Number `json:"transactionAmount"`
		TransactionStatus    string      `json:"transactionStatus"`
		Currency             string      `json:"currency"`
		IsReversed           bool        `json:"isReversed"`
	} `json:"data"`
}
