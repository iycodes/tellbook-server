package paystackclient

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func configuredPaystackIntegrationClient(t *testing.T) *Client {
	t.Helper()
	secretKey := os.Getenv("PAYSTACK_SECRET_KEY_TEST")
	if strings.EqualFold(os.Getenv("PAYMENTS_ENVIRONMENT"), "live") {
		secretKey = os.Getenv("PAYSTACK_SECRET_KEY")
	}
	client, err := NewClient(Config{SecretKey: secretKey, BaseURL: os.Getenv("PAYSTACK_BASE_URL")})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestConfiguredPaystackReadOnlyCapabilities(t *testing.T) {
	if os.Getenv("RUN_PROVIDER_INTEGRATION_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_INTEGRATION_TESTS is not true")
	}
	client := configuredPaystackIntegrationClient(t)
	ctx := context.Background()
	options, err := client.ListDestinations(ctx, payments.DestinationQuery{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
	})
	if err != nil {
		t.Fatalf("ListDestinations() error = %v", err)
	}
	if len(options) == 0 {
		t.Fatal("ListDestinations() returned no active NGN banks")
	}
	if _, err := client.AvailablePayoutBalance(ctx, "NGN"); err != nil {
		t.Fatalf("AvailablePayoutBalance() error = %v", err)
	}
	now := time.Now().UTC()
	if _, err := client.ListSettlementEvidence(ctx, payments.SettlementQuery{From: now.Add(-30 * 24 * time.Hour), To: now}); err != nil {
		t.Fatalf("ListSettlementEvidence() error = %v", err)
	}
}

func TestConfiguredPaystackLiveCheckoutChannels(t *testing.T) {
	if os.Getenv("RUN_PROVIDER_INTEGRATION_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_INTEGRATION_TESTS is not true")
	}
	client := configuredPaystackIntegrationClient(t)
	reference := "tellbook-live-checkout-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	session, err := client.InitializeCheckout(context.Background(), payments.PaymentSnapshot{
		PaymentID: uuid.New(), Reference: reference, Provider: "paystack", Method: "hosted_checkout",
		CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
		CustomerName: "TellBook Certification", CustomerEmail: "payments-certification@example.com",
		Description: "TellBook live checkout certification",
	})
	if err != nil {
		t.Fatalf("InitializeCheckout() error = %v", err)
	}
	if session.Flow != payments.CheckoutFlowHostedRedirect || session.CheckoutURL == "" {
		t.Fatal("InitializeCheckout() returned an incomplete hosted session")
	}
}

func TestConfiguredPaystackLiveDestinationResolution(t *testing.T) {
	if os.Getenv("RUN_PROVIDER_INTEGRATION_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_INTEGRATION_TESTS is not true")
	}
	accountNumber := strings.TrimSpace(os.Getenv("PAYMENT_CERTIFICATION_NGN_ACCOUNT_NUMBER"))
	if len(accountNumber) != 10 {
		t.Fatal("PAYMENT_CERTIFICATION_NGN_ACCOUNT_NUMBER must contain a controlled 10-digit account")
	}
	client := configuredPaystackIntegrationClient(t)
	options, err := client.ListDestinations(context.Background(), payments.DestinationQuery{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
	})
	if err != nil {
		t.Fatalf("ListDestinations() error = %v", err)
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
		t.Fatalf("ResolveDestination() error = %v", err)
	}
	if resolved.Identifier != accountNumber || strings.TrimSpace(resolved.AccountName) == "" {
		t.Fatal("ResolveDestination() returned mismatched beneficiary details")
	}
}
