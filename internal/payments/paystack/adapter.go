package paystackclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"
)

var (
	_ payments.CollectionProvider  = (*Client)(nil)
	_ payments.WebhookVerifier     = (*Client)(nil)
	_ payments.DestinationProvider = (*Client)(nil)
	_ payments.PayoutProvider      = (*Client)(nil)
)

func (c *Client) InitializeCheckout(ctx context.Context, snapshot payments.PaymentSnapshot) (payments.CheckoutSession, error) {
	if snapshot.Method == payments.PaymentMethodBankTransfer {
		return c.initializeBankTransfer(ctx, snapshot)
	}
	channels, err := paystackCollectionChannels(snapshot.Method)
	if err != nil || snapshot.Provider != "paystack" || snapshot.Reference == "" ||
		snapshot.AmountMinor <= 0 || snapshot.CustomerEmail == "" {
		return payments.CheckoutSession{}, errors.New("invalid paystack payment snapshot")
	}
	metadata, err := json.Marshal(snapshot.Metadata)
	if err != nil {
		return payments.CheckoutSession{}, err
	}
	request := map[string]any{
		"email": snapshot.CustomerEmail, "amount": strconv.FormatInt(int64(snapshot.AmountMinor), 10),
		"currency": snapshot.CurrencyCode, "reference": snapshot.Reference,
		"channels": channels, "metadata": string(metadata),
	}
	if snapshot.ReturnURL != "" {
		request["callback_url"] = snapshot.ReturnURL
	}
	var response responseEnvelope[struct {
		AuthorizationURL string `json:"authorization_url"`
		AccessCode       string `json:"access_code"`
		Reference        string `json:"reference"`
	}]
	if err := c.doRequest(ctx, http.MethodPost, "/transaction/initialize", request, &response); err != nil {
		return payments.CheckoutSession{}, err
	}
	if response.Data.AuthorizationURL == "" || response.Data.Reference != snapshot.Reference {
		return payments.CheckoutSession{}, errors.New("paystack returned an invalid checkout session")
	}
	return payments.CheckoutSession{
		Flow: payments.CheckoutFlowHostedRedirect, ProviderReference: response.Data.Reference,
		CheckoutURL: response.Data.AuthorizationURL,
	}, nil
}

func (c *Client) initializeBankTransfer(ctx context.Context, snapshot payments.PaymentSnapshot) (payments.CheckoutSession, error) {
	if snapshot.Provider != "paystack" || snapshot.Reference == "" || snapshot.AmountMinor <= 0 ||
		snapshot.CustomerEmail == "" || snapshot.CurrencyCode != "NGN" {
		return payments.CheckoutSession{}, errors.New("invalid Paystack bank-transfer snapshot")
	}
	requestedAt := snapshot.RequestedAt.UTC()
	if requestedAt.IsZero() {
		requestedAt = time.Now().UTC()
	}
	expiresAt := requestedAt.Add(30 * time.Minute)
	request := map[string]any{
		"email": snapshot.CustomerEmail, "amount": strconv.FormatInt(int64(snapshot.AmountMinor), 10),
		"currency": snapshot.CurrencyCode, "reference": snapshot.Reference, "metadata": snapshot.Metadata,
		"bank_transfer": map[string]string{"account_expires_at": expiresAt.Format(time.RFC3339Nano)},
	}
	var response responseEnvelope[paystackCharge]
	if err := c.doRequest(ctx, http.MethodPost, "/charge", request, &response); err != nil {
		return payments.CheckoutSession{}, err
	}
	return paystackBankTransferSession(snapshot, response.Data)
}

func (c *Client) RecoverCheckout(ctx context.Context, snapshot payments.PaymentSnapshot) (payments.CheckoutSession, error) {
	switch snapshot.Method {
	case payments.PaymentMethodCard, payments.PaymentMethodHostedHistorical:
		return payments.CheckoutSession{}, payments.ErrCheckoutRecoveryNotReady
	case payments.PaymentMethodBankTransfer:
		if snapshot.RequestedAt.IsZero() || time.Since(snapshot.RequestedAt) < 10*time.Second {
			return payments.CheckoutSession{}, payments.ErrCheckoutRecoveryNotReady
		}
		charge, err := c.checkCharge(ctx, snapshot.Reference)
		if err != nil {
			return payments.CheckoutSession{}, err
		}
		return paystackBankTransferSession(snapshot, charge)
	default:
		return payments.CheckoutSession{}, errors.New("unsupported Paystack checkout method")
	}
}

func (c *Client) ReconcilePayment(ctx context.Context, record payments.PaymentRecord) (payments.PaymentReconciliation, error) {
	if record.Provider != "paystack" || record.Reference == "" || record.AmountMinor <= 0 {
		return payments.PaymentReconciliation{}, errors.New("invalid paystack payment record")
	}
	if record.Method == payments.PaymentMethodBankTransfer {
		return c.reconcileBankTransfer(ctx, record)
	}
	var response responseEnvelope[struct {
		Status    string      `json:"status"`
		Channel   string      `json:"channel"`
		Reference string      `json:"reference"`
		Amount    json.Number `json:"amount"`
		Currency  string      `json:"currency"`
		PaidAt    string      `json:"paid_at"`
	}]
	if err := c.doRequest(ctx, http.MethodGet, "/transaction/verify/"+url.PathEscape(record.Reference), nil, &response); err != nil {
		var providerError *ErrorResponse
		if errors.As(err, &providerError) &&
			(providerError.HTTPStatus == http.StatusNotFound ||
				(providerError.HTTPStatus == http.StatusBadRequest && strings.Contains(strings.ToLower(providerError.Message), "not found"))) {
			return payments.PaymentReconciliation{
				ProviderStatus: "not_found", Status: payments.PaymentStatusPending,
				ReconciliationReason: "provider has no transaction for this reference",
			}, nil
		}
		return payments.PaymentReconciliation{}, err
	}
	amount, err := strconv.ParseInt(response.Data.Amount.String(), 10, 64)
	if err != nil {
		return payments.PaymentReconciliation{}, errors.New("paystack returned an invalid payment amount")
	}
	status := normalizePaystackPaymentStatus(response.Data.Status)
	if status == payments.PaymentStatusPaid && response.Data.Reference != record.Reference {
		return payments.PaymentReconciliation{}, errors.New("paystack payment reconciliation reference mismatch")
	}
	if status == payments.PaymentStatusPaid && (money.Minor(amount) != record.AmountMinor || response.Data.Currency != record.CurrencyCode) {
		return payments.PaymentReconciliation{}, &payments.PaymentEvidenceMismatchError{
			Reconciliation: payments.PaymentReconciliation{
				ProviderStatus: response.Data.Status, Status: status, AmountMinor: money.Minor(amount),
				CurrencyCode: response.Data.Currency,
			},
			Message: "paystack payment amount or currency does not match the stored obligation",
		}
	}
	return payments.PaymentReconciliation{
		ProviderStatus: response.Data.Status, ProviderChannel: payments.CanonicalProviderChannel(response.Data.Channel),
		Status: status, AmountMinor: money.Minor(amount),
		CurrencyCode: response.Data.Currency, PaidAt: parseRFC3339(response.Data.PaidAt),
	}, nil
}

func (c *Client) reconcileBankTransfer(ctx context.Context, record payments.PaymentRecord) (payments.PaymentReconciliation, error) {
	charge, err := c.checkCharge(ctx, record.Reference)
	if err != nil {
		var providerError *ErrorResponse
		if errors.As(err, &providerError) && (providerError.HTTPStatus == http.StatusNotFound ||
			(providerError.HTTPStatus == http.StatusBadRequest && strings.Contains(strings.ToLower(providerError.Message), "not found"))) {
			return payments.PaymentReconciliation{ProviderStatus: "not_found", Status: payments.PaymentStatusPending}, nil
		}
		return payments.PaymentReconciliation{}, err
	}
	status := normalizePaystackPaymentStatus(charge.Status)
	amount := money.Minor(0)
	if status == payments.PaymentStatusPaid {
		if charge.Amount.String() == "" {
			return payments.PaymentReconciliation{}, errors.New("Paystack charge omitted the paid amount")
		}
		parsed, parseErr := strconv.ParseInt(charge.Amount.String(), 10, 64)
		if parseErr != nil {
			return payments.PaymentReconciliation{}, errors.New("Paystack bank transfer returned an invalid amount")
		}
		if charge.Reference != record.Reference {
			return payments.PaymentReconciliation{}, errors.New("Paystack bank-transfer reconciliation reference mismatch")
		}
		if money.Minor(parsed) != record.AmountMinor || !strings.EqualFold(charge.Currency, record.CurrencyCode) {
			return payments.PaymentReconciliation{}, &payments.PaymentEvidenceMismatchError{
				Reconciliation: payments.PaymentReconciliation{
					ProviderStatus: charge.Status, Status: status, AmountMinor: money.Minor(parsed),
					CurrencyCode: strings.ToUpper(charge.Currency),
				},
				Message: "Paystack bank-transfer amount or currency does not match the stored obligation",
			}
		}
		amount = money.Minor(parsed)
	}
	result := payments.PaymentReconciliation{
		ProviderStatus: charge.Status, ProviderChannel: payments.PaymentMethodBankTransfer,
		Status: status, AmountMinor: amount, CurrencyCode: strings.ToUpper(charge.Currency),
		PaidAt: parseRFC3339(firstNonEmpty(charge.PaidAt, charge.PaidAtCamel)),
	}
	if status == payments.PaymentStatusFailed {
		result.FailureCode = "provider_failed"
		result.FailureMessage = firstNonEmpty(charge.Message, charge.GatewayResponse)
	}
	return result, nil
}

func (c *Client) checkCharge(ctx context.Context, reference string) (paystackCharge, error) {
	var response responseEnvelope[paystackCharge]
	err := c.doRequest(ctx, http.MethodGet, "/charge/"+url.PathEscape(reference), nil, &response)
	return response.Data, err
}

func paystackBankTransferSession(snapshot payments.PaymentSnapshot, charge paystackCharge) (payments.CheckoutSession, error) {
	if charge.Reference != snapshot.Reference || !strings.Contains(strings.ToLower(charge.Status), "pending") ||
		strings.TrimSpace(charge.AccountName) == "" || !validPaystackAccountNumber(charge.AccountNumber) ||
		strings.TrimSpace(charge.Bank.Name) == "" {
		return payments.CheckoutSession{}, errors.New("Paystack returned invalid bank-transfer instructions")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(charge.AccountExpiresAt))
	if err != nil {
		return payments.CheckoutSession{}, errors.New("Paystack returned an invalid bank-transfer expiry")
	}
	requestedAt := snapshot.RequestedAt.UTC()
	if requestedAt.IsZero() {
		return payments.CheckoutSession{}, errors.New("Paystack bank-transfer request time is missing")
	}
	if expiresAt.Before(requestedAt.Add(15*time.Minute)) || expiresAt.After(requestedAt.Add(8*time.Hour)) {
		return payments.CheckoutSession{}, errors.New("Paystack returned a bank-transfer expiry outside the supported window")
	}
	if charge.Amount.String() != "" {
		amount, parseErr := strconv.ParseInt(charge.Amount.String(), 10, 64)
		if parseErr != nil || money.Minor(amount) != snapshot.AmountMinor {
			return payments.CheckoutSession{}, errors.New("Paystack returned a mismatched bank-transfer amount")
		}
	}
	if charge.Currency != "" && !strings.EqualFold(charge.Currency, snapshot.CurrencyCode) {
		return payments.CheckoutSession{}, errors.New("Paystack returned a mismatched bank-transfer currency")
	}
	return payments.CheckoutSession{
		Flow: payments.CheckoutFlowBankTransfer, ProviderReference: charge.Reference, ExpiresAt: &expiresAt,
		BankTransfer: &payments.BankTransferInstructions{
			AccountName: charge.AccountName, AccountNumber: charge.AccountNumber, BankName: charge.Bank.Name,
			TransferReference: charge.TransactionReference,
		},
	}, nil
}

func validPaystackAccountNumber(value string) bool {
	if len(value) != 10 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type paystackCharge struct {
	Reference            string      `json:"reference"`
	Status               string      `json:"status"`
	Message              string      `json:"message"`
	GatewayResponse      string      `json:"gateway_response"`
	Amount               json.Number `json:"amount"`
	Currency             string      `json:"currency"`
	AccountName          string      `json:"account_name"`
	AccountNumber        string      `json:"account_number"`
	AccountExpiresAt     string      `json:"account_expires_at"`
	TransactionReference string      `json:"transaction_reference"`
	PaidAt               string      `json:"paid_at"`
	PaidAtCamel          string      `json:"paidAt"`
	Bank                 struct {
		Name string `json:"name"`
	} `json:"bank"`
}

func (c *Client) ListDestinations(ctx context.Context, query payments.DestinationQuery) ([]payments.DestinationOption, error) {
	country, ok := paystackCountryName(query.CountryCode)
	if !ok {
		return nil, errors.New("unsupported Paystack destination country")
	}
	bankType, err := paystackRecipientType(query.CountryCode, query.Rail)
	if err != nil {
		return nil, err
	}
	banks, err := c.ListBanksFor(ctx, country, query.CurrencyCode, bankType)
	if err != nil {
		return nil, err
	}
	options := make([]payments.DestinationOption, 0, len(banks))
	for _, bank := range banks {
		if bank.Active && !bank.IsDeleted {
			options = append(options, payments.DestinationOption{Code: bank.Code, Name: bank.Name})
		}
	}
	return options, nil
}

func (c *Client) ResolveDestination(ctx context.Context, input payments.ResolveDestinationInput) (payments.ResolvedDestination, error) {
	resolved, err := c.ResolveAccount(ctx, input.Institution, input.Identifier)
	if err != nil {
		return payments.ResolvedDestination{}, err
	}
	if resolved.AccountNumber != strings.TrimSpace(input.Identifier) || resolved.AccountName == "" {
		return payments.ResolvedDestination{}, errors.New("paystack destination resolution mismatch")
	}
	return payments.ResolvedDestination{
		CountryCode: strings.ToUpper(input.CountryCode), CurrencyCode: strings.ToUpper(input.CurrencyCode),
		Rail: input.Rail, InstitutionCode: input.Institution, InstitutionName: input.InstitutionName,
		Identifier: resolved.AccountNumber, AccountName: resolved.AccountName,
	}, nil
}

func (c *Client) CreateProviderRecipient(ctx context.Context, destination payments.ResolvedDestination) (payments.ProviderRecipient, error) {
	recipientType, err := paystackRecipientType(destination.CountryCode, destination.Rail)
	if err != nil {
		return payments.ProviderRecipient{}, err
	}
	request := map[string]string{
		"type": recipientType, "name": destination.AccountName, "account_number": destination.Identifier,
		"bank_code": destination.InstitutionCode, "currency": destination.CurrencyCode,
	}
	var response responseEnvelope[struct {
		RecipientCode string `json:"recipient_code"`
		Active        bool   `json:"active"`
		Type          string `json:"type"`
		Currency      string `json:"currency"`
		Name          string `json:"name"`
		Details       struct {
			AccountNumber string `json:"account_number"`
			BankCode      string `json:"bank_code"`
		} `json:"details"`
	}]
	if err := c.doRequest(ctx, http.MethodPost, "/transferrecipient", request, &response); err != nil {
		return payments.ProviderRecipient{}, err
	}
	if response.Data.RecipientCode == "" || !response.Data.Active || response.Data.Type != recipientType ||
		response.Data.Currency != destination.CurrencyCode ||
		strings.TrimSpace(response.Data.Details.AccountNumber) != strings.TrimSpace(destination.Identifier) ||
		strings.TrimSpace(response.Data.Details.BankCode) != strings.TrimSpace(destination.InstitutionCode) ||
		!strings.EqualFold(strings.TrimSpace(response.Data.Name), strings.TrimSpace(destination.AccountName)) {
		return payments.ProviderRecipient{}, errors.New("paystack returned a mismatched transfer recipient")
	}
	return payments.ProviderRecipient{
		ProviderReference: response.Data.RecipientCode, CountryCode: destination.CountryCode,
		CurrencyCode: destination.CurrencyCode, Rail: destination.Rail,
		InstitutionCode: destination.InstitutionCode, InstitutionName: destination.InstitutionName,
		Identifier: response.Data.Details.AccountNumber, AccountName: destination.AccountName,
	}, nil
}

func (c *Client) InitiatePayout(ctx context.Context, snapshot payments.PayoutSnapshot, recipient payments.ProviderRecipient) (payments.PayoutResult, error) {
	if snapshot.Provider != "paystack" || snapshot.Reference == "" || snapshot.AmountMinor <= 0 ||
		recipient.ProviderReference == "" || recipient.CurrencyCode != snapshot.CurrencyCode {
		return payments.PayoutResult{}, errors.New("invalid paystack payout")
	}
	request := map[string]any{
		"source": "balance", "amount": int64(snapshot.AmountMinor), "recipient": recipient.ProviderReference,
		"reference": snapshot.Reference, "reason": snapshot.Narration, "currency": snapshot.CurrencyCode,
	}
	var response responseEnvelope[paystackTransfer]
	if err := c.doRequest(ctx, http.MethodPost, "/transfer", request, &response); err != nil {
		return payments.PayoutResult{}, err
	}
	return payments.PayoutResult{
		ProviderReference: response.Data.TransferCode, ProviderStatus: response.Data.Status,
		Status: normalizePaystackPayoutInitiationStatus(response.Data.Status),
	}, nil
}

func (c *Client) ReconcilePayout(ctx context.Context, record payments.PayoutRecord) (payments.PayoutReconciliation, error) {
	if record.Provider != "paystack" || record.Reference == "" || record.AmountMinor <= 0 {
		return payments.PayoutReconciliation{}, errors.New("invalid paystack payout record")
	}
	var response responseEnvelope[paystackTransfer]
	if err := c.doRequest(ctx, http.MethodGet, "/transfer/verify/"+url.PathEscape(record.Reference), nil, &response); err != nil {
		return payments.PayoutReconciliation{}, err
	}
	if response.Data.Reference != record.Reference || response.Data.Amount != int64(record.AmountMinor) ||
		response.Data.Currency != record.CurrencyCode {
		return payments.PayoutReconciliation{}, errors.New("paystack payout reconciliation does not match the stored payout")
	}
	var recipient paystackTransferRecipient
	if len(response.Data.Recipient) == 0 || json.Unmarshal(response.Data.Recipient, &recipient) != nil ||
		recipient.RecipientCode != record.ExpectedRecipient.ProviderReference ||
		strings.TrimSpace(recipient.Details.AccountNumber) != strings.TrimSpace(record.ExpectedRecipient.Identifier) ||
		strings.TrimSpace(recipient.Details.BankCode) != strings.TrimSpace(record.ExpectedRecipient.InstitutionCode) {
		return payments.PayoutReconciliation{}, errors.New("paystack payout recipient does not match the stored destination")
	}
	status := normalizePaystackPayoutStatus(response.Data.Status)
	return payments.PayoutReconciliation{
		ProviderStatus: response.Data.Status, Status: status, AmountMinor: money.Minor(response.Data.Amount),
		CurrencyCode: response.Data.Currency, CompletedAt: parseRFC3339(response.Data.TransferredAt),
	}, nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.secretKey)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	var envelopeStatus struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(responseBody, &envelopeStatus); err != nil {
		return fmt.Errorf("decode paystack response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelopeStatus.Status {
		return &ErrorResponse{HTTPStatus: response.StatusCode, Message: envelopeStatus.Message}
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode paystack response: %w", err)
	}
	return nil
}

type paystackTransfer struct {
	Amount        int64           `json:"amount"`
	Currency      string          `json:"currency"`
	Reference     string          `json:"reference"`
	Status        string          `json:"status"`
	TransferCode  string          `json:"transfer_code"`
	TransferredAt string          `json:"transferred_at"`
	Recipient     json.RawMessage `json:"recipient"`
}

type paystackTransferRecipient struct {
	RecipientCode string `json:"recipient_code"`
	Active        bool   `json:"active"`
	Details       struct {
		AccountNumber string `json:"account_number"`
		BankCode      string `json:"bank_code"`
	} `json:"details"`
}

func paystackCollectionChannels(method string) ([]string, error) {
	switch method {
	case payments.PaymentMethodHostedHistorical, payments.PaymentMethodCard:
		return []string{"card"}, nil
	default:
		return nil, errors.New("unsupported Paystack collection method")
	}
}

func paystackCountryName(code string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "NG":
		return "nigeria", true
	case "GH":
		return "ghana", true
	case "ZA":
		return "south africa", true
	case "KE":
		return "kenya", true
	default:
		return "", false
	}
}

func paystackRecipientType(countryCode, rail string) (string, error) {
	if rail == "mobile_money_wallet" {
		return "mobile_money", nil
	}
	if rail != "bank_account" {
		return "", errors.New("unsupported Paystack payout rail")
	}
	switch strings.ToUpper(strings.TrimSpace(countryCode)) {
	case "NG":
		return "nuban", nil
	case "GH":
		return "ghipss", nil
	case "ZA":
		return "basa", nil
	case "KE":
		return "kepss", nil
	default:
		return "", errors.New("unsupported Paystack payout country")
	}
}

func normalizePaystackPaymentStatus(value string) payments.PaymentStatus {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success":
		return payments.PaymentStatusPaid
	case "failed":
		return payments.PaymentStatusFailed
	case "reversed":
		return payments.PaymentStatusReversed
	case "abandoned":
		// Paystack can report an open hosted checkout as abandoned before the
		// customer returns and completes it. Keep reconciling until Paystack
		// reports a genuinely terminal state.
		return payments.PaymentStatusPending
	default:
		return payments.PaymentStatusPending
	}
}

func normalizePaystackPayoutStatus(value string) payments.PayoutStatus {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success":
		return payments.PayoutStatusSuccessful
	case "failed":
		return payments.PayoutStatusFailed
	case "reversed":
		return payments.PayoutStatusReversed
	case "otp":
		return payments.PayoutStatusRequiresAction
	case "pending", "processing":
		return payments.PayoutStatusPending
	default:
		return payments.PayoutStatusUnknown
	}
}

func normalizePaystackPayoutInitiationStatus(value string) payments.PayoutStatus {
	status := normalizePaystackPayoutStatus(value)
	if status == payments.PayoutStatusSuccessful {
		return payments.PayoutStatusPending
	}
	return status
}

func parseRFC3339(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return &parsed
}
