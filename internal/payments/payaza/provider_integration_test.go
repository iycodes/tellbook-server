package payaza

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func configuredIntegrationClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("RUN_PROVIDER_INTEGRATION_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_INTEGRATION_TESTS is not true")
	}
	tenantID := os.Getenv("PAYMENTS_ENVIRONMENT")
	if tenantID == "" {
		tenantID = "test"
	}
	publicKey := os.Getenv("PAYAZA_PUBLIC_KEY_TEST")
	secretKey := os.Getenv("PAYAZA_SECRET_KEY_TEST")
	if tenantID == "live" {
		publicKey = os.Getenv("PAYAZA_PUBLIC_KEY")
		secretKey = os.Getenv("PAYAZA_SECRET_KEY")
	}
	client, err := NewClient(Config{
		PublicKey: publicKey, SecretKey: secretKey,
		BaseURL: os.Getenv("PAYAZA_BASE_URL"), TenantID: tenantID,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func configuredLiveIntegrationClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("RUN_PROVIDER_INTEGRATION_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_INTEGRATION_TESTS is not true")
	}
	sourceAccounts := map[string]string{}
	if raw := os.Getenv("PAYAZA_SOURCE_ACCOUNTS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &sourceAccounts); err != nil {
			t.Fatalf("decode PAYAZA_SOURCE_ACCOUNTS: %v", err)
		}
	}
	client, err := NewClient(Config{
		PublicKey: os.Getenv("PAYAZA_PUBLIC_KEY"), SecretKey: os.Getenv("PAYAZA_SECRET_KEY"),
		BaseURL: os.Getenv("PAYAZA_BASE_URL"), TenantID: "live",
		TransactionPIN: os.Getenv("PAYAZA_TRANSFER_PIN"), SourceAccounts: sourceAccounts,
		PayoutSender: PayoutSender{
			Name: os.Getenv("PAYAZA_PAYOUT_SENDER_NAME"), Phone: os.Getenv("PAYAZA_PAYOUT_SENDER_PHONE"),
			Address: os.Getenv("PAYAZA_PAYOUT_SENDER_ADDRESS"),
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestConfiguredPayazaLiveHostedCheckout(t *testing.T) {
	if os.Getenv("RUN_PROVIDER_LIVE_RESOURCE_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_LIVE_RESOURCE_TESTS is not true")
	}
	client := configuredLiveIntegrationClient(t)
	reference := "tellbook-live-checkout-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	session, err := client.InitializeCheckout(context.Background(), payments.PaymentSnapshot{
		PaymentID: uuid.New(), Reference: reference, Provider: "payaza", Method: "hosted_checkout",
		CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
		CustomerName: "TellBook Certification", CustomerEmail: "payments-certification@example.com",
		CustomerPhone: "08000000000", Description: "TellBook live hosted-checkout certification",
	})
	if err != nil {
		t.Fatalf("InitializeCheckout() error = %v", err)
	}
	if session.Flow != payments.CheckoutFlowHostedModal || session.ProviderReference != reference ||
		session.Instructions["biller_name"] != "TellBook" {
		t.Fatal("InitializeCheckout() returned incomplete hosted checkout instructions")
	}
}

func certificationAccountNumber(t *testing.T) string {
	t.Helper()
	accountNumber := strings.TrimSpace(os.Getenv("PAYMENT_CERTIFICATION_NGN_ACCOUNT_NUMBER"))
	if len(accountNumber) != 10 {
		t.Fatal("PAYMENT_CERTIFICATION_NGN_ACCOUNT_NUMBER must contain a controlled 10-digit account")
	}
	return accountNumber
}

func configuredKudaOption(t *testing.T, client *Client) payments.DestinationOption {
	t.Helper()
	options, err := client.ListDestinations(context.Background(), payments.DestinationQuery{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
	})
	if err != nil {
		t.Fatalf("ListDestinations() error = %v", err)
	}
	for _, option := range options {
		if strings.Contains(strings.ToLower(option.Name), "kuda") {
			return option
		}
	}
	t.Fatal("Payaza live bank list did not contain Kuda")
	return payments.DestinationOption{}
}

func TestConfiguredPayazaAccountAccess(t *testing.T) {
	client := configuredIntegrationClient(t)
	var response map[string]any
	if err := client.doJSON(context.Background(), http.MethodGet, mainAccountPath, true, nil, &response); err != nil {
		t.Fatalf("main account enquiry error = %v", err)
	}
	if len(response) == 0 {
		t.Fatal("main account enquiry returned an empty response")
	}
}

func TestConfiguredPayazaLiveDestinationList(t *testing.T) {
	client := configuredLiveIntegrationClient(t)
	_ = configuredKudaOption(t, client)
}

func TestConfiguredPayazaLiveDestinationResolution(t *testing.T) {
	client := configuredLiveIntegrationClient(t)
	bank := configuredKudaOption(t, client)
	resolved, err := client.ResolveDestination(context.Background(), payments.ResolveDestinationInput{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		Institution: bank.Code, InstitutionName: bank.Name, Identifier: certificationAccountNumber(t),
	})
	if err != nil {
		t.Fatalf("ResolveDestination() error = %v", err)
	}
	if resolved.Identifier != certificationAccountNumber(t) || strings.TrimSpace(resolved.AccountName) == "" {
		t.Fatal("ResolveDestination() returned mismatched beneficiary details")
	}
	if _, err := client.AvailablePayoutBalance(context.Background(), "NGN"); err != nil {
		t.Fatalf("AvailablePayoutBalance() error = %v", err)
	}
}
