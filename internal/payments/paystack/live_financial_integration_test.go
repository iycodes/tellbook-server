package paystackclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func TestPaystackControlledLivePayout(t *testing.T) {
	amount, runID := liveFinancialCertification(t)
	client, err := NewClient(Config{SecretKey: os.Getenv("PAYSTACK_SECRET_KEY"), BaseURL: os.Getenv("PAYSTACK_BASE_URL")})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	accountNumber := strings.TrimSpace(os.Getenv("PAYMENT_CERTIFICATION_NGN_ACCOUNT_NUMBER"))
	if len(accountNumber) != 10 {
		t.Fatal("PAYMENT_CERTIFICATION_NGN_ACCOUNT_NUMBER must contain a controlled 10-digit account")
	}
	resolved := resolveControlledPaystackDestination(t, client, accountNumber)
	available, err := client.AvailablePayoutBalance(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("load payout balance: %v", err)
	}
	if available < amount {
		t.Fatal("Paystack balance is below the controlled payout amount")
	}

	reference := "tellbook-paystack-" + runID
	recipient, exists := existingPaystackTransferRecipient(t, client, reference, resolved)
	if !exists {
		var recipientExists bool
		recipient, recipientExists = existingPaystackTransferRecipientForDestination(t, client, resolved)
		if !recipientExists {
			recipient, err = client.CreateProviderRecipient(context.Background(), resolved)
			if err != nil {
				t.Fatalf("create transfer recipient: %v", err)
			}
		}
		result, initiateErr := client.InitiatePayout(context.Background(), payments.PayoutSnapshot{
			PayoutID: uuid.New(), Reference: reference, Provider: "paystack", Rail: "bank_account",
			CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: amount,
			Narration: "TellBook live certification",
		}, recipient)
		if initiateErr != nil {
			t.Fatalf("initiate controlled payout: %v", initiateErr)
		}
		if result.Status != payments.PayoutStatusPending && result.Status != payments.PayoutStatusUnknown {
			t.Fatalf("unexpected initiation status: %s", result.Status)
		}
	}
	record := payments.PayoutRecord{
		ID: uuid.New(), Reference: reference, Provider: "paystack", CurrencyCode: "NGN",
		CurrencyExponent: 2, AmountMinor: amount, ExpectedRecipient: recipient,
	}
	waitForPaystackPayout(t, client, record)
}

func existingPaystackTransferRecipientForDestination(t *testing.T, client *Client, resolved payments.ResolvedDestination) (payments.ProviderRecipient, bool) {
	t.Helper()
	var response responseEnvelope[[]paystackTransferRecipient]
	if err := client.doRequest(context.Background(), http.MethodGet, "/transferrecipient?perPage=100&page=1", nil, &response); err != nil {
		t.Fatalf("list existing Paystack transfer recipients: %v", err)
	}
	for _, stored := range response.Data {
		if stored.Active && stored.RecipientCode != "" &&
			stored.Details.AccountNumber == resolved.Identifier && stored.Details.BankCode == resolved.InstitutionCode {
			return providerRecipientFromPaystack(stored, resolved), true
		}
	}
	return payments.ProviderRecipient{}, false
}

func providerRecipientFromPaystack(stored paystackTransferRecipient, resolved payments.ResolvedDestination) payments.ProviderRecipient {
	return payments.ProviderRecipient{
		ProviderReference: stored.RecipientCode, CountryCode: resolved.CountryCode,
		CurrencyCode: resolved.CurrencyCode, Rail: resolved.Rail,
		InstitutionCode: resolved.InstitutionCode, InstitutionName: resolved.InstitutionName,
		Identifier: resolved.Identifier, AccountName: resolved.AccountName,
	}
}

func resolveControlledPaystackDestination(t *testing.T, client *Client, accountNumber string) payments.ResolvedDestination {
	t.Helper()
	options, err := client.ListDestinations(context.Background(), payments.DestinationQuery{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
	})
	if err != nil {
		t.Fatalf("list destinations: %v", err)
	}
	var bank payments.DestinationOption
	for _, option := range options {
		if strings.Contains(strings.ToLower(option.Name), "kuda") {
			bank = option
			break
		}
	}
	if bank.Code == "" {
		t.Fatal("Paystack bank list did not contain Kuda")
	}
	resolved, err := client.ResolveDestination(context.Background(), payments.ResolveDestinationInput{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		Institution: bank.Code, InstitutionName: bank.Name, Identifier: accountNumber,
	})
	if err != nil {
		t.Fatalf("resolve controlled beneficiary: %v", err)
	}
	return resolved
}

func existingPaystackTransferRecipient(t *testing.T, client *Client, reference string, resolved payments.ResolvedDestination) (payments.ProviderRecipient, bool) {
	t.Helper()
	var response responseEnvelope[paystackTransfer]
	err := client.doRequest(context.Background(), http.MethodGet, "/transfer/verify/"+url.PathEscape(reference), nil, &response)
	if err != nil {
		var providerErr *ErrorResponse
		if errors.As(err, &providerErr) && providerErr.HTTPStatus == http.StatusNotFound {
			return payments.ProviderRecipient{}, false
		}
		t.Fatalf("query existing Paystack transfer: %v", err)
	}
	var stored paystackTransferRecipient
	if json.Unmarshal(response.Data.Recipient, &stored) != nil || stored.RecipientCode == "" ||
		stored.Details.AccountNumber != resolved.Identifier || stored.Details.BankCode != resolved.InstitutionCode {
		t.Fatal("existing Paystack transfer recipient does not match the controlled destination")
	}
	return providerRecipientFromPaystack(stored, resolved), true
}

func liveFinancialCertification(t *testing.T) (money.Minor, string) {
	t.Helper()
	if os.Getenv("RUN_PROVIDER_LIVE_FINANCIAL_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_LIVE_FINANCIAL_TESTS is not true")
	}
	if os.Getenv("LIVE_FINANCIAL_TEST_CONFIRMATION") != "I_ACKNOWLEDGE_REAL_MONEY" {
		t.Fatal("LIVE_FINANCIAL_TEST_CONFIRMATION is missing")
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("PAYMENT_CERTIFICATION_MAX_AMOUNT_MINOR")), 10, 64)
	if err != nil || parsed != 10000 {
		t.Fatal("PAYMENT_CERTIFICATION_MAX_AMOUNT_MINOR must be exactly 10000 for this certification")
	}
	runID := strings.TrimSpace(os.Getenv("PAYMENT_CERTIFICATION_RUN_ID"))
	if len(runID) < 8 || len(runID) > 32 {
		t.Fatal("PAYMENT_CERTIFICATION_RUN_ID must contain 8 to 32 safe characters")
	}
	for _, char := range runID {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			t.Fatal("PAYMENT_CERTIFICATION_RUN_ID must use lowercase letters, digits, or hyphens")
		}
	}
	return money.Minor(parsed), runID
}

func waitForPaystackPayout(t *testing.T, client *Client, record payments.PayoutRecord) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastStatus payments.PayoutStatus
	var lastErr error
	for time.Now().Before(deadline) {
		reconciled, err := client.ReconcilePayout(context.Background(), record)
		if err == nil {
			lastStatus = reconciled.Status
			switch reconciled.Status {
			case payments.PayoutStatusSuccessful:
				return
			case payments.PayoutStatusFailed, payments.PayoutStatusReversed, payments.PayoutStatusCancelled:
				t.Fatalf("Paystack payout reached terminal status %s", reconciled.Status)
			}
		} else {
			lastErr = err
		}
		time.Sleep(3 * time.Second)
	}
	if lastErr != nil && lastStatus == "" {
		t.Fatalf("Paystack payout did not become queryable: %v", lastErr)
	}
	t.Fatalf("Paystack payout did not complete before timeout; last status=%s", lastStatus)
}
