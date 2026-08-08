package paystackclient

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func TestPaystackControlledSandboxPayout(t *testing.T) {
	amount, runID := sandboxFinancialCertification(t)
	secretKey := strings.TrimSpace(os.Getenv("PAYSTACK_SECRET_KEY_TEST"))
	if !strings.HasPrefix(secretKey, "sk_test_") {
		t.Fatal("PAYSTACK_SECRET_KEY_TEST must use the sk_test_ prefix")
	}
	client, err := NewClient(Config{SecretKey: secretKey, BaseURL: os.Getenv("PAYSTACK_BASE_URL")})
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
		t.Fatalf("load sandbox payout balance: %v", err)
	}
	if available < amount {
		t.Fatalf("Paystack sandbox balance is below the controlled payout amount")
	}

	reference := "tellbook-paystack-test-" + runID
	recipient, exists := existingPaystackTransferRecipient(t, client, reference, resolved)
	if !exists {
		var recipientExists bool
		recipient, recipientExists = existingPaystackTransferRecipientForDestination(t, client, resolved)
		if !recipientExists {
			recipient, err = client.CreateProviderRecipient(context.Background(), resolved)
			if err != nil {
				t.Fatalf("create sandbox transfer recipient: %v", err)
			}
		}
		result, initiateErr := client.InitiatePayout(context.Background(), payments.PayoutSnapshot{
			PayoutID: uuid.New(), Reference: reference, Provider: "paystack", Rail: "bank_account",
			CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: amount,
			Narration: "TellBook sandbox certification",
		}, recipient)
		if initiateErr != nil {
			t.Fatalf("initiate controlled sandbox payout: %v", initiateErr)
		}
		if result.Status != payments.PayoutStatusPending && result.Status != payments.PayoutStatusUnknown {
			t.Fatalf("unexpected sandbox initiation status: %s", result.Status)
		}
	}

	record := payments.PayoutRecord{
		ID: uuid.New(), Reference: reference, Provider: "paystack", CurrencyCode: "NGN",
		CurrencyExponent: 2, AmountMinor: amount, ExpectedRecipient: recipient,
	}
	waitForPaystackPayout(t, client, record)
}

func sandboxFinancialCertification(t *testing.T) (money.Minor, string) {
	t.Helper()
	if os.Getenv("RUN_PROVIDER_SANDBOX_FINANCIAL_TESTS") != "true" {
		t.Skip("RUN_PROVIDER_SANDBOX_FINANCIAL_TESTS is not true")
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
