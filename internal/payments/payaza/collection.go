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
	merchantReferenceStatusPath         = "/merchant-collection/transfer_notification_controller/merchant/transaction-query"
	dynamicVirtualAccountPath           = "/merchant-collection/merchant/virtual_account/generate_virtual_account"
	virtualAccountTransactionStatusPath = "/merchant-collection/transfer_notification_controller/transaction-query"
)

func (c *Client) InitializeCheckout(ctx context.Context, snapshot payments.PaymentSnapshot) (payments.CheckoutSession, error) {
	if err := validatePaymentSnapshot(snapshot); err != nil {
		return payments.CheckoutSession{}, err
	}
	switch snapshot.Method {
	case payments.PaymentMethodCard, payments.PaymentMethodHostedHistorical:
		return c.initializeHostedCard(snapshot)
	case payments.PaymentMethodBankTransfer:
		return c.initializeDynamicVirtualAccount(ctx, snapshot)
	default:
		return payments.CheckoutSession{}, errors.New("payaza checkout method is not configured")
	}
}

func (c *Client) initializeHostedCard(snapshot payments.PaymentSnapshot) (payments.CheckoutSession, error) {
	amount, err := newDecimalNumber(int64(snapshot.AmountMinor), snapshot.CurrencyExponent)
	if err != nil {
		return payments.CheckoutSession{}, err
	}
	firstName, lastName := splitName(snapshot.CustomerName)
	return payments.CheckoutSession{
		Flow: payments.CheckoutFlowHostedModal, ProviderReference: snapshot.Reference,
		PublicKey: c.publicKey,
		Instructions: map[string]string{
			"connection_mode": strings.ToUpper(c.tenantID[:1]) + c.tenantID[1:],
			"checkout_amount": string(amount), "currency_code": snapshot.CurrencyCode,
			"currency_exponent": fmt.Sprint(snapshot.CurrencyExponent),
			"email_address":     snapshot.CustomerEmail, "phone_number": snapshot.CustomerPhone,
			"first_name": firstName, "last_name": lastName,
			"transaction_reference": snapshot.Reference, "biller_name": "TellBook",
		},
	}, nil
}

func (c *Client) initializeDynamicVirtualAccount(ctx context.Context, snapshot payments.PaymentSnapshot) (payments.CheckoutSession, error) {
	if c.dvaBankCode == "" || c.dvaEnquiryBankCode == "" || c.dvaBankName == "" {
		return payments.CheckoutSession{}, errors.New("payaza DVA bank is not configured")
	}
	amount, err := newDecimalNumber(int64(snapshot.AmountMinor), snapshot.CurrencyExponent)
	if err != nil {
		return payments.CheckoutSession{}, err
	}
	firstName, lastName := splitName(snapshot.CustomerName)
	request := map[string]string{
		"account_name": "TellBook - " + strings.TrimSpace(snapshot.CustomerName),
		"account_type": "Dynamic", "bank_code": c.dvaBankCode,
		"has_amount_validation": "true", "account_reference": snapshot.Reference,
		"customer_first_name": firstName, "customer_last_name": lastName,
		"customer_email": snapshot.CustomerEmail, "customer_phone_number": snapshot.CustomerPhone,
		"transaction_description": snapshot.Description, "transaction_amount": string(amount),
	}
	if c.dvaBankCode == "1067" {
		request["expires_in_minutes"] = "30"
	}
	var response dynamicVirtualAccountResponse
	if err := c.doJSON(ctx, http.MethodPost, dynamicVirtualAccountPath, false, request, &response); err != nil {
		return payments.CheckoutSession{}, err
	}
	return c.dynamicVirtualAccountSession(snapshot, response)
}

func (c *Client) RecoverCheckout(ctx context.Context, snapshot payments.PaymentSnapshot) (payments.CheckoutSession, error) {
	if err := validatePaymentSnapshot(snapshot); err != nil {
		return payments.CheckoutSession{}, err
	}
	switch snapshot.Method {
	case payments.PaymentMethodCard, payments.PaymentMethodHostedHistorical:
		return c.initializeHostedCard(snapshot)
	case payments.PaymentMethodBankTransfer:
		status, err := c.queryDynamicVirtualAccount(ctx, snapshot.Reference)
		if err != nil {
			return payments.CheckoutSession{}, err
		}
		if !status.Success || status.Data.TransactionReference != snapshot.Reference ||
			status.Data.MerchantTransactionReference != snapshot.Reference ||
			!strings.EqualFold(status.Data.Currency, snapshot.CurrencyCode) ||
			!strings.EqualFold(status.Data.TransactionType, "VirtualAccount") ||
			!validNUBAN(status.Data.VirtualAccountNumber) {
			return payments.CheckoutSession{}, errors.New("payaza returned invalid DVA recovery evidence")
		}
		resolved, err := c.ResolveDestination(ctx, payments.ResolveDestinationInput{
			CountryCode: snapshot.CountryCode, CurrencyCode: snapshot.CurrencyCode, Rail: "bank_account",
			Institution: c.dvaEnquiryBankCode, InstitutionName: c.dvaBankName,
			Identifier: status.Data.VirtualAccountNumber,
		})
		if err != nil {
			return payments.CheckoutSession{}, fmt.Errorf("recover payaza DVA account name: %w", err)
		}
		requestedAt := snapshot.RequestedAt.UTC()
		if requestedAt.IsZero() {
			return payments.CheckoutSession{}, errors.New("payaza DVA recovery request time is missing")
		}
		expiresAt := requestedAt.Add(30 * time.Minute)
		return payments.CheckoutSession{
			Flow: payments.CheckoutFlowBankTransfer, ProviderReference: snapshot.Reference, ExpiresAt: &expiresAt,
			BankTransfer: &payments.BankTransferInstructions{
				AccountName: resolved.AccountName, AccountNumber: resolved.Identifier, BankName: c.dvaBankName,
			},
		}, nil
	default:
		return payments.CheckoutSession{}, errors.New("payaza checkout method is not configured")
	}
}

func (c *Client) ReconcilePayment(ctx context.Context, record payments.PaymentRecord) (payments.PaymentReconciliation, error) {
	if record.Provider != "payaza" || record.Reference == "" || record.AmountMinor <= 0 {
		return payments.PaymentReconciliation{}, errors.New("invalid payaza payment record")
	}
	if record.Method != payments.PaymentMethodHostedHistorical && record.Method != payments.PaymentMethodCard &&
		record.Method != payments.PaymentMethodBankTransfer {
		return payments.PaymentReconciliation{}, errors.New("unsupported payaza payment method")
	}
	if record.Method == payments.PaymentMethodBankTransfer {
		return c.reconcileDynamicVirtualAccount(ctx, record)
	}
	var response merchantReferenceStatusResponse
	path := merchantReferenceStatusPath + "?" + url.Values{"merchant_reference": {record.Reference}}.Encode()
	if err := c.doJSON(ctx, http.MethodGet, path, false, nil, &response); err != nil {
		var providerError *ErrorResponse
		if errors.As(err, &providerError) && providerError.HTTPStatus == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(providerError.Message), "transaction not found") {
			return payments.PaymentReconciliation{
				ProviderStatus: "not_found", Status: payments.PaymentStatusPending,
				ReconciliationReason: "provider has no transaction for merchant reference",
			}, nil
		}
		return payments.PaymentReconciliation{}, err
	}
	if !response.Success {
		return payments.PaymentReconciliation{}, errors.New("payaza payment status query was unsuccessful")
	}
	status := normalizeCollectionStatus(response.Data.TransactionStatus)
	referenceMatches := strings.TrimSpace(response.Data.MerchantTransactionReference) == record.Reference
	amount := money.Minor(0)
	if response.Data.AmountReceived.String() != "" {
		parsed, err := parseProviderAmount(response.Data.AmountReceived, record.CurrencyExponent)
		if err != nil {
			return payments.PaymentReconciliation{}, fmt.Errorf("parse payaza payment amount: %w", err)
		}
		amount = parsed
	}
	if status == payments.PaymentStatusPaid && !referenceMatches {
		return payments.PaymentReconciliation{}, errors.New("payaza payment reconciliation reference mismatch")
	}
	if status == payments.PaymentStatusPaid && (amount != record.AmountMinor || !strings.EqualFold(response.Data.Currency, record.CurrencyCode)) {
		return payments.PaymentReconciliation{}, &payments.PaymentEvidenceMismatchError{
			Reconciliation: payments.PaymentReconciliation{
				ProviderStatus: response.Data.TransactionStatus, Status: status, AmountMinor: amount,
				CurrencyCode: strings.ToUpper(response.Data.Currency),
			},
			Message: "payaza payment amount or currency does not match the stored obligation",
		}
	}
	paidAt := parsePayazaTime(response.Data.CurrentStatusDate)
	reconciliation := payments.PaymentReconciliation{
		ProviderStatus: response.Data.TransactionStatus, Status: status,
		ProviderChannel: payments.CanonicalProviderChannel(response.Data.TransactionType),
		AmountMinor:     amount, CurrencyCode: strings.ToUpper(response.Data.Currency), PaidAt: paidAt,
	}
	if status == payments.PaymentStatusFailed {
		reconciliation.FailureCode = "provider_failed"
		reconciliation.FailureMessage = strings.TrimSpace(response.Data.StatusReason)
		if reconciliation.FailureMessage == "" {
			reconciliation.FailureMessage = "The payment provider declined this transaction."
		}
	}
	return reconciliation, nil
}

func (c *Client) reconcileDynamicVirtualAccount(ctx context.Context, record payments.PaymentRecord) (payments.PaymentReconciliation, error) {
	response, err := c.queryDynamicVirtualAccount(ctx, record.Reference)
	if err != nil {
		var providerError *ErrorResponse
		if errors.As(err, &providerError) && providerError.HTTPStatus == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(providerError.Message), "not found") {
			return payments.PaymentReconciliation{ProviderStatus: "not_found", Status: payments.PaymentStatusPending}, nil
		}
		return payments.PaymentReconciliation{}, err
	}
	if !response.Success || response.Data.TransactionReference != record.Reference ||
		response.Data.MerchantTransactionReference != record.Reference ||
		!strings.EqualFold(response.Data.TransactionType, "VirtualAccount") {
		return payments.PaymentReconciliation{}, errors.New("payaza DVA reconciliation reference mismatch")
	}
	status := normalizeCollectionStatus(response.Data.TransactionStatus)
	amount := money.Minor(0)
	if status == payments.PaymentStatusPaid {
		amount, err = parseProviderAmount(response.Data.AmountReceived, record.CurrencyExponent)
		if err != nil {
			return payments.PaymentReconciliation{}, errors.New("payaza DVA returned an invalid payment amount")
		}
		if amount != record.AmountMinor || !strings.EqualFold(response.Data.Currency, record.CurrencyCode) {
			return payments.PaymentReconciliation{}, &payments.PaymentEvidenceMismatchError{
				Reconciliation: payments.PaymentReconciliation{
					ProviderStatus: response.Data.TransactionStatus, Status: status, AmountMinor: amount,
					CurrencyCode: strings.ToUpper(response.Data.Currency),
				},
				Message: "payaza DVA amount or currency does not match the stored obligation",
			}
		}
	}
	result := payments.PaymentReconciliation{
		ProviderStatus: response.Data.TransactionStatus, ProviderChannel: payments.PaymentMethodBankTransfer,
		Status: status, AmountMinor: amount, CurrencyCode: strings.ToUpper(response.Data.Currency),
		PaidAt: parsePayazaTime(response.Data.CurrentStatusDate),
	}
	if status == payments.PaymentStatusFailed {
		result.FailureCode = "provider_failed"
		result.FailureMessage = strings.TrimSpace(response.Data.StatusReason)
	}
	return result, nil
}

func (c *Client) queryDynamicVirtualAccount(ctx context.Context, reference string) (dynamicVirtualAccountTransactionResponse, error) {
	var response dynamicVirtualAccountTransactionResponse
	path := virtualAccountTransactionStatusPath + "?" + url.Values{"transaction_reference": {reference}}.Encode()
	err := c.doJSON(ctx, http.MethodGet, path, false, nil, &response)
	return response, err
}

func (c *Client) dynamicVirtualAccountSession(snapshot payments.PaymentSnapshot, response dynamicVirtualAccountResponse) (payments.CheckoutSession, error) {
	if !response.Success || response.Data.AccountExpired || response.Data.AccountType != "Dynamic" ||
		response.Data.AccountReference != snapshot.Reference || response.Data.TransactionReference != snapshot.Reference ||
		strings.TrimSpace(response.Data.AccountName) == "" || !validNUBAN(response.Data.AccountNumber) ||
		strings.TrimSpace(response.Data.BankName) == "" {
		return payments.CheckoutSession{}, errors.New("payaza returned an invalid DVA session")
	}
	amount, err := parseProviderAmount(response.Data.TransactionAmountPayable, snapshot.CurrencyExponent)
	if err != nil || amount != snapshot.AmountMinor {
		return payments.CheckoutSession{}, errors.New("payaza returned a mismatched DVA amount")
	}
	minutes := response.Data.ExpiresInMinutes
	if minutes <= 0 {
		minutes = 30
	}
	requestedAt := snapshot.RequestedAt.UTC()
	if requestedAt.IsZero() {
		requestedAt = time.Now().UTC()
	}
	expiresAt := requestedAt.Add(time.Duration(minutes) * time.Minute)
	return payments.CheckoutSession{
		Flow: payments.CheckoutFlowBankTransfer, ProviderReference: response.Data.TransactionReference,
		ExpiresAt: &expiresAt,
		BankTransfer: &payments.BankTransferInstructions{
			AccountName: response.Data.AccountName, AccountNumber: response.Data.AccountNumber,
			BankName: response.Data.BankName,
		},
	}, nil
}

func validNUBAN(value string) bool {
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

type dynamicVirtualAccountResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		AccountName              string      `json:"account_name"`
		AccountNumber            string      `json:"account_number"`
		AccountType              string      `json:"account_type"`
		BankName                 string      `json:"bank_name"`
		AccountReference         string      `json:"account_reference"`
		AccountExpired           bool        `json:"account_expired"`
		TransactionAmountPayable json.Number `json:"transaction_amount_payable"`
		TransactionReference     string      `json:"transaction_reference"`
		ExpiresInMinutes         int         `json:"expires_in_minutes"`
	} `json:"data"`
}

type dynamicVirtualAccountTransactionResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		TransactionReference         string      `json:"transaction_reference"`
		MerchantTransactionReference string      `json:"merchant_transaction_reference"`
		AmountReceived               json.Number `json:"amount_received"`
		TransactionStatus            string      `json:"transaction_status"`
		TransactionType              string      `json:"transaction_type"`
		StatusReason                 string      `json:"status_reason"`
		CurrentStatusDate            string      `json:"current_status_date"`
		Currency                     string      `json:"currency"`
		VirtualAccountNumber         string      `json:"virtual_account_number"`
	} `json:"data"`
}

func validatePaymentSnapshot(snapshot payments.PaymentSnapshot) error {
	if snapshot.Provider != "payaza" || snapshot.Reference == "" || snapshot.AmountMinor <= 0 ||
		snapshot.CurrencyCode == "" || snapshot.CustomerEmail == "" || snapshot.CustomerName == "" ||
		snapshot.CustomerPhone == "" {
		return errors.New("invalid payaza payment snapshot")
	}
	return nil
}

func splitName(name string) (string, string) {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "Customer", "Customer"
	}
	if len(parts) == 1 {
		return parts[0], parts[0]
	}
	return parts[0], strings.Join(parts[1:], " ")
}

func normalizeCollectionStatus(value string) payments.PaymentStatus {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed", "funds received", "successful", "success", "nip_success":
		return payments.PaymentStatusPaid
	case "failed", "transaction failed", "declined", "nip_failure":
		return payments.PaymentStatusFailed
	case "expired":
		return payments.PaymentStatusExpired
	default:
		return payments.PaymentStatusPending
	}
}

func parsePayazaTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		parsed = parsed.UTC()
		return &parsed
	}
	payazaLocation := time.FixedZone("WAT", 60*60)
	for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05.999", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, payazaLocation); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

type merchantReferenceStatusResponse struct {
	Success bool `json:"success"`
	Data    struct {
		TransactionReference         string      `json:"transaction_reference"`
		MerchantTransactionReference string      `json:"merchant_transaction_reference"`
		AmountReceived               json.Number `json:"amount_received"`
		TransactionStatus            string      `json:"transaction_status"`
		TransactionType              string      `json:"transaction_type"`
		StatusReason                 string      `json:"status_reason"`
		CurrentStatusDate            string      `json:"current_status_date"`
		Currency                     string      `json:"currency"`
	} `json:"data"`
}
