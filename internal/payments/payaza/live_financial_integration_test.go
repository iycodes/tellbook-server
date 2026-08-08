package payaza

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func TestPayazaControlledLivePayout(t *testing.T) {
	amount, runID := liveFinancialCertification(t)
	client := configuredLiveIntegrationClient(t)
	bank := configuredKudaOption(t, client)
	accountNumber := certificationAccountNumber(t)
	resolved, err := client.ResolveDestination(context.Background(), payments.ResolveDestinationInput{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		Institution: bank.Code, InstitutionName: bank.Name, Identifier: accountNumber,
	})
	if err != nil {
		t.Fatalf("resolve controlled beneficiary: %v", err)
	}
	recipient, err := client.CreateProviderRecipient(context.Background(), resolved)
	if err != nil {
		t.Fatalf("create provider recipient: %v", err)
	}
	available, err := client.AvailablePayoutBalance(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("load payout balance: %v", err)
	}
	if available < amount {
		t.Fatalf("Payaza balance is below the controlled payout amount")
	}

	reference := "tellbook-payaza-" + runID
	record := payments.PayoutRecord{
		ID: uuid.New(), Reference: reference, Provider: "payaza", ProviderReference: reference,
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: amount, ExpectedRecipient: recipient,
	}
	if _, err := client.ReconcilePayout(context.Background(), record); errors.Is(err, ErrPayoutNotFound) {
		result, initiateErr := client.InitiatePayout(context.Background(), payments.PayoutSnapshot{
			PayoutID: uuid.New(), Reference: reference, Provider: "payaza", Rail: "bank_account",
			CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: amount,
			Narration: "TellBook payout",
		}, recipient)
		if initiateErr != nil {
			t.Fatalf("initiate controlled payout: %v", initiateErr)
		}
		if result.Status != payments.PayoutStatusPending && result.Status != payments.PayoutStatusUnknown {
			t.Fatalf("unexpected initiation status: %s", result.Status)
		}
	} else if err != nil {
		t.Fatalf("query existing Payaza payout: %v", err)
	}
	waitForPayazaPayout(t, client, record)
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

func waitForPayazaPayout(t *testing.T, client *Client, record payments.PayoutRecord) {
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
				t.Fatalf("Payaza payout reached terminal status %s", reconciled.Status)
			}
		} else {
			lastErr = err
		}
		time.Sleep(3 * time.Second)
	}
	if lastErr != nil && lastStatus == "" {
		t.Fatalf("Payaza payout did not become queryable: %v", lastErr)
	}
	t.Fatalf("Payaza payout did not complete before timeout; last status=%s", lastStatus)
}
