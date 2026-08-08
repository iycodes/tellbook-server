package paystackclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func TestInitializeCheckoutUsesQuotedMinorUnitsAndHostedURL(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/transaction/initialize" || req.Method != http.MethodPost {
			t.Fatalf("request = %s %s", req.Method, req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["amount"] != "123456" || payload["reference"] != "payment-reference-1234" {
			t.Fatalf("payload = %s", body)
		}
		return jsonResponse(`{"status":true,"message":"Authorization URL created","data":{"authorization_url":"https://checkout.paystack.test/session","access_code":"access","reference":"payment-reference-1234"}}`), nil
	})
	session, err := client.InitializeCheckout(context.Background(), payments.PaymentSnapshot{
		PaymentID: uuid.New(), Provider: "paystack", Method: "hosted_checkout", Reference: "payment-reference-1234",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: money.Minor(123456),
		CustomerEmail: "customer@example.test", Metadata: map[string]string{"booking": "public-token"},
	})
	if err != nil || session.Flow != payments.CheckoutFlowHostedRedirect || session.CheckoutURL == "" {
		t.Fatalf("session = %#v, error = %v", session, err)
	}
}

func TestInitializeBankTransferUsesChargeWithExactInstructions(t *testing.T) {
	requestedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/charge" {
			t.Fatalf("request = %s %s", req.Method, req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bankTransfer, ok := body["bank_transfer"].(map[string]any)
		if body["amount"] != "10000" || body["reference"] != "pay-transfer-1" || !ok ||
			bankTransfer["account_expires_at"] == "" {
			t.Fatalf("payload = %#v", body)
		}
		return jsonResponse(fmt.Sprintf(`{"status":true,"message":"Charge attempted","data":{"reference":"pay-transfer-1","status":"pending_bank_transfer","amount":10000,"currency":"NGN","account_name":"TellBook Ada","account_number":"0123456789","account_expires_at":%q,"transaction_reference":"PSK-TRANSFER-1","bank":{"name":"Titan Paystack"}}}`, requestedAt.Add(30*time.Minute).Format(time.RFC3339Nano))), nil
	})
	session, err := client.InitializeCheckout(context.Background(), payments.PaymentSnapshot{
		Provider: "paystack", Method: payments.PaymentMethodBankTransfer, Reference: "pay-transfer-1",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
		CustomerEmail: "ada@example.test", RequestedAt: requestedAt,
	})
	if err != nil || session.Flow != payments.CheckoutFlowBankTransfer || session.BankTransfer == nil ||
		session.BankTransfer.TransferReference != "PSK-TRANSFER-1" {
		t.Fatalf("session = %#v, error = %v", session, err)
	}
}

func TestRecoverBankTransferWaitsTenSecondsBeforeChargeQuery(t *testing.T) {
	called := false
	client := newTestClient(t, func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	})
	_, err := client.RecoverCheckout(context.Background(), payments.PaymentSnapshot{
		Provider: "paystack", Method: payments.PaymentMethodBankTransfer, Reference: "pay-transfer-2",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
		CustomerEmail: "ada@example.test", RequestedAt: time.Now().UTC(),
	})
	if !errors.Is(err, payments.ErrCheckoutRecoveryNotReady) || called {
		t.Fatalf("called = %t, error = %v", called, err)
	}
}

func TestNormalizePaystackPaymentStatusKeepsAbandonedCheckoutPending(t *testing.T) {
	if got := normalizePaystackPaymentStatus("abandoned"); got != payments.PaymentStatusPending {
		t.Fatalf("normalizePaystackPaymentStatus(abandoned) = %q, want %q", got, payments.PaymentStatusPending)
	}
	if got := normalizePaystackPaymentStatus("failed"); got != payments.PaymentStatusFailed {
		t.Fatalf("normalizePaystackPaymentStatus(failed) = %q, want %q", got, payments.PaymentStatusFailed)
	}
}

func TestReconcilePaymentKeepsMissingProviderReferencePending(t *testing.T) {
	client := newTestClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":false,"message":"Transaction reference not found"}`)),
		}, nil
	})
	reconciliation, err := client.ReconcilePayment(context.Background(), payments.PaymentRecord{
		Provider: "paystack", Reference: "payment-reference-1234", Method: "hosted_checkout",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
	})
	if err != nil || reconciliation.Status != payments.PaymentStatusPending || reconciliation.ProviderStatus != "not_found" {
		t.Fatalf("reconciliation = %#v, error = %v", reconciliation, err)
	}
}

func TestPaystackWebhookVerificationSanitizesPayload(t *testing.T) {
	client := newTestClient(t, func(*http.Request) (*http.Response, error) { return nil, nil })
	rawBody := []byte(`{"event":"charge.success","data":{"reference":"payment-reference-1234","status":"success","amount":123456,"currency":"NGN","authorization":{"authorization_code":"AUTH_secret"}}}`)
	mac := hmac.New(sha512.New, []byte("secret-key"))
	_, _ = mac.Write(rawBody)
	headers := make(http.Header)
	headers.Set("x-paystack-signature", hex.EncodeToString(mac.Sum(nil)))
	event, err := client.VerifyAndDecodeWebhook(rawBody, headers)
	if err != nil {
		t.Fatalf("VerifyAndDecodeWebhook() error = %v", err)
	}
	if event.EventType != "charge.success" || event.Normalized["amount"] != json.Number("123456") {
		t.Fatalf("event = %#v", event)
	}
	if _, leaked := event.Normalized["authorization"]; leaked {
		t.Fatalf("normalized webhook leaked authorization: %#v", event.Normalized)
	}
}

func TestPaystackWebhookWithoutTransactionReferenceIsStoredAsUnrelated(t *testing.T) {
	client := newTestClient(t, func(*http.Request) (*http.Response, error) { return nil, nil })
	rawBody := []byte(`{"event":"customeridentification.failed","data":{"customer_code":"CUS_test","reason":"invalid"}}`)
	mac := hmac.New(sha512.New, []byte("secret-key"))
	_, _ = mac.Write(rawBody)
	headers := make(http.Header)
	headers.Set("x-paystack-signature", hex.EncodeToString(mac.Sum(nil)))

	event, err := client.VerifyAndDecodeWebhook(rawBody, headers)
	if err != nil {
		t.Fatalf("VerifyAndDecodeWebhook() error = %v", err)
	}
	if event.ProviderEventID != "customeridentification.failed:CUS_test" || event.EventType != "customeridentification.failed" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCreateProviderRecipientValidatesReturnedDestination(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/transferrecipient" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		return jsonResponse(`{"status":true,"message":"Transfer recipient created","data":{"recipient_code":"RCP_test","active":true,"type":"nuban","currency":"NGN","name":"ADA OKAFOR","details":{"account_number":"0123456789","bank_code":"044"}}}`), nil
	})
	recipient, err := client.CreateProviderRecipient(context.Background(), payments.ResolvedDestination{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		InstitutionCode: "044", InstitutionName: "Access Bank", Identifier: "0123456789", AccountName: "ADA OKAFOR",
	})
	if err != nil || recipient.ProviderReference != "RCP_test" || recipient.Identifier != "0123456789" {
		t.Fatalf("recipient = %#v, error = %v", recipient, err)
	}
}

func TestCreateProviderRecipientRejectsReturnedAccountMismatch(t *testing.T) {
	client := newTestClient(t, func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"status":true,"message":"Transfer recipient created","data":{"recipient_code":"RCP_test","active":true,"type":"nuban","currency":"NGN","name":"ADA OKAFOR","details":{"account_number":"9999999999","bank_code":"044"}}}`), nil
	})
	_, err := client.CreateProviderRecipient(context.Background(), payments.ResolvedDestination{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		InstitutionCode: "044", InstitutionName: "Access Bank", Identifier: "0123456789", AccountName: "ADA OKAFOR",
	})
	if err == nil {
		t.Fatal("CreateProviderRecipient() accepted a mismatched account number")
	}
}

func TestPaystackPayoutOTPRequiresActionWithoutFinalizing(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/transfer" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"amount":50000`) || !strings.Contains(string(body), `"recipient":"RCP_test"`) {
			t.Fatalf("body = %s", body)
		}
		return jsonResponse(`{"status":true,"message":"Transfer requires OTP to continue","data":{"amount":50000,"currency":"NGN","reference":"payout-reference-1234","status":"otp","transfer_code":"TRF_test"}}`), nil
	})
	result, err := client.InitiatePayout(context.Background(), payments.PayoutSnapshot{
		PayoutID: uuid.New(), Provider: "paystack", Reference: "payout-reference-1234",
		CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2,
		Rail: "bank_account", AmountMinor: money.Minor(50000), Narration: "TellBook payout",
	}, payments.ProviderRecipient{ProviderReference: "RCP_test", CurrencyCode: "NGN"})
	if err != nil || result.Status != payments.PayoutStatusRequiresAction {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestPaystackPayoutReconciliationVerifiesRecipient(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/transfer/verify/payout-reference-1234" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		return jsonResponse(`{"status":true,"message":"Transfer retrieved","data":{"amount":50000,"currency":"NGN","reference":"payout-reference-1234","status":"success","transfer_code":"TRF_test","recipient":{"recipient_code":"RCP_test","details":{"account_number":"0123456789","bank_code":"044"}}}}`), nil
	})
	result, err := client.ReconcilePayout(context.Background(), payments.PayoutRecord{
		Reference: "payout-reference-1234", Provider: "paystack", CurrencyCode: "NGN",
		AmountMinor: money.Minor(50000), ExpectedRecipient: payments.ProviderRecipient{
			ProviderReference: "RCP_test", Identifier: "0123456789", InstitutionCode: "044",
		},
	})
	if err != nil || result.Status != payments.PayoutStatusSuccessful {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestPaystackSettlementEvidenceMatchesSuccessfulTransactions(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/settlement":
			if req.URL.Query().Get("status") != "success" || req.URL.Query().Get("subaccount") != "none" {
				t.Fatalf("settlement query = %s", req.URL.RawQuery)
			}
			return jsonResponse(`{"status":true,"message":"Settlements retrieved","data":[{"id":3090024,"status":"success","currency":"NGN","settlement_date":"2026-08-04T00:00:00Z","updatedAt":"2026-08-04T08:12:01Z"}],"meta":{"page":1,"pageCount":1}}`), nil
		case "/settlement/3090024/transactions":
			return jsonResponse(`{"status":true,"message":"Transactions retrieved","data":[{"reference":"payment-reference-1234","status":"success","amount":123456,"currency":"NGN"},{"reference":"ignored-failed","status":"failed","amount":100,"currency":"NGN"}],"meta":{"page":1,"pageCount":1}}`), nil
		default:
			t.Fatalf("unexpected path = %s", req.URL.Path)
			return nil, nil
		}
	})
	evidence, err := client.ListSettlementEvidence(context.Background(), payments.SettlementQuery{
		From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
	})
	if err != nil || len(evidence) != 1 || evidence[0].SettlementReference != "3090024" ||
		evidence[0].PaymentReference != "payment-reference-1234" || evidence[0].AmountMinor != money.Minor(123456) {
		t.Fatalf("evidence = %#v, error = %v", evidence, err)
	}
}

func TestPaystackAvailablePayoutBalanceUsesExactMinorUnits(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/balance" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		return jsonResponse(`{"status":true,"message":"Balances retrieved","data":[{"currency":"NGN","balance":987654}]}`), nil
	})
	balance, err := client.AvailablePayoutBalance(context.Background(), "NGN")
	if err != nil || balance != money.Minor(987654) {
		t.Fatalf("balance = %d, error = %v", balance, err)
	}
}
