package payaza

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	return newTestClientForTenant(t, "test", handler)
}

func newTestClientForTenant(t *testing.T, tenantID string, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{
		PublicKey: "public-key", SecretKey: "secret-key", BaseURL: server.URL,
		TenantID: tenantID, TransactionPIN: "419374",
		SourceAccounts: map[string]string{"NGN": "1010000009"}, HTTPClient: server.Client(),
		PayoutSender: PayoutSender{Name: "TellBook", Phone: "08000000000", Address: "Lagos Nigeria"},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestRoutedDestinationClientSeparatesDirectoryAndTransactionEnvironments(t *testing.T) {
	directoryCalled := false
	directory := newTestClientForTenant(t, "live", func(w http.ResponseWriter, r *http.Request) {
		directoryCalled = true
		if r.URL.Path != banksPath+"NGN" || r.Header.Get("X-TenantID") != "live" {
			t.Fatalf("directory request = %s headers=%#v", r.URL.Path, r.Header)
		}
		_, _ = io.WriteString(w, `{"status":true,"data":[{"name":"Access Bank","code":"044","active":true,"type":"bank","country_code":"NG"}]}`)
	})
	transactionCalled := false
	transaction := newTestClientForTenant(t, "test", func(w http.ResponseWriter, r *http.Request) {
		transactionCalled = true
		if r.URL.Path != enquiryPath || r.Header.Get("X-TenantID") != "test" {
			t.Fatalf("transaction request = %s headers=%#v", r.URL.Path, r.Header)
		}
		_, _ = io.WriteString(w, `{"response_code":200,"response_content":{"account_number":"0123456789","bank_code":"044","account_name":"ADA OKAFOR","account_status":"active"}}`)
	})
	client, err := NewRoutedDestinationClient(directory, transaction)
	if err != nil {
		t.Fatalf("NewRoutedDestinationClient() error = %v", err)
	}

	options, err := client.ListDestinations(context.Background(), payments.DestinationQuery{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
	})
	if err != nil || len(options) != 1 || options[0].Code != "044" {
		t.Fatalf("options = %#v, error = %v", options, err)
	}
	resolved, err := client.ResolveDestination(context.Background(), payments.ResolveDestinationInput{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
		Institution: "044", InstitutionName: "Access Bank", Identifier: "0123456789",
	})
	if err != nil || resolved.AccountName != "ADA OKAFOR" {
		t.Fatalf("resolved = %#v, error = %v", resolved, err)
	}
	if !directoryCalled || !transactionCalled {
		t.Fatalf("directory called = %t, transaction called = %t", directoryCalled, transactionCalled)
	}
}

func TestInitializeHostedCheckoutBuildsTellBookModalSession(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("hosted modal initialization must not call Payaza")
	})
	session, err := client.InitializeCheckout(context.Background(), payments.PaymentSnapshot{
		PaymentID: uuid.New(), Reference: "pay-1", Provider: "payaza", Method: "hosted_checkout",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: money.Minor(123456),
		CustomerName: "Ada Okafor", CustomerEmail: "ada@example.test", CustomerPhone: "08000000000",
	})
	if err != nil || session.Flow != payments.CheckoutFlowHostedModal ||
		session.Instructions["checkout_amount"] != "1234.56" || session.Instructions["biller_name"] != "TellBook" {
		t.Fatalf("session = %#v, error = %v", session, err)
	}
}

func TestInitializeDynamicVirtualAccountUsesCertifiedGlobusContract(t *testing.T) {
	requestedAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != dynamicVirtualAccountPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["account_type"] != "Dynamic" || body["bank_code"] != "140" ||
			body["account_reference"] != "pay-dva-1" || body["transaction_amount"] != "100.00" ||
			body["has_amount_validation"] != "true" {
			t.Fatalf("payload = %#v", body)
		}
		if _, present := body["expires_in_minutes"]; present {
			t.Fatalf("Globus request unexpectedly set expires_in_minutes: %#v", body)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"account_name":"TELLBOOK ADA OKAFOR","account_number":"0123456789","account_type":"Dynamic","bank_name":"Globus Bank","account_reference":"pay-dva-1","account_expired":false,"transaction_amount_payable":100,"transaction_reference":"pay-dva-1"}}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{
		PublicKey: "public-key", SecretKey: "secret-key", BaseURL: server.URL, TenantID: "live",
		DVABankCode: "140", DVAEnquiryBankCode: "000027", DVABankName: "Globus Bank", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.InitializeCheckout(context.Background(), payments.PaymentSnapshot{
		PaymentID: uuid.New(), Reference: "pay-dva-1", Provider: "payaza", Method: payments.PaymentMethodBankTransfer,
		CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
		CustomerName: "Ada Okafor", CustomerEmail: "ada@example.test", CustomerPhone: "08000000000",
		Description: "Lash appointment", RequestedAt: requestedAt,
	})
	if err != nil || session.Flow != payments.CheckoutFlowBankTransfer || session.BankTransfer == nil ||
		session.BankTransfer.AccountNumber != "0123456789" || session.ExpiresAt == nil ||
		!session.ExpiresAt.Equal(requestedAt.Add(30*time.Minute)) {
		t.Fatalf("session = %#v, error = %v", session, err)
	}
}

func TestRecoverDynamicVirtualAccountUsesPersistedReferenceAndAccountEnquiry(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch r.URL.Path {
		case virtualAccountTransactionStatusPath:
			if r.URL.Query().Get("transaction_reference") != "pay-dva-2" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{"transaction_reference":"pay-dva-2","merchant_transaction_reference":"pay-dva-2","amount_received":100,"transaction_status":"Initialized","transaction_type":"VirtualAccount","currency":"NGN","virtual_account_number":"0123456789"}}`)
		case enquiryPath:
			var body struct {
				ServicePayload map[string]string `json:"service_payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ServicePayload["bank_code"] != "000027" || body.ServicePayload["account_number"] != "0123456789" {
				t.Fatalf("enquiry = %#v", body)
			}
			_, _ = io.WriteString(w, `{"response_code":200,"response_content":{"account_number":"0123456789","bank_code":"000027","account_name":"TELLBOOK ADA OKAFOR","account_status":"active"}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{
		PublicKey: "public-key", SecretKey: "secret-key", BaseURL: server.URL, TenantID: "live",
		DVABankCode: "140", DVAEnquiryBankCode: "000027", DVABankName: "Globus Bank", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.RecoverCheckout(context.Background(), payments.PaymentSnapshot{
		Reference: "pay-dva-2", Provider: "payaza", Method: payments.PaymentMethodBankTransfer,
		CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
		CustomerName: "Ada Okafor", CustomerEmail: "ada@example.test", CustomerPhone: "08000000000",
		RequestedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil || requestCount != 2 || session.BankTransfer == nil || session.BankTransfer.AccountName != "TELLBOOK ADA OKAFOR" {
		t.Fatalf("session = %#v, requests = %d, error = %v", session, requestCount, err)
	}
}

func TestReconcileInitializedDynamicVirtualAccountDoesNotTreatAmountAsPayment(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":{"transaction_reference":"pay-dva-3","merchant_transaction_reference":"pay-dva-3","amount_received":100,"transaction_status":"Initialized","transaction_type":"VirtualAccount","currency":"NGN","virtual_account_number":"0123456789"}}`)
	})
	reconciliation, err := client.ReconcilePayment(context.Background(), payments.PaymentRecord{
		Reference: "pay-dva-3", Provider: "payaza", Method: payments.PaymentMethodBankTransfer,
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
	})
	if err != nil || reconciliation.Status != payments.PaymentStatusPending || reconciliation.AmountMinor != 0 {
		t.Fatalf("reconciliation = %#v, error = %v", reconciliation, err)
	}
}

func TestReconcileHostedPaymentRequiresExactEvidence(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != merchantReferenceStatusPath || r.URL.Query().Get("merchant_reference") != "pay-1" || r.Header.Get("X-TenantID") != "" {
			t.Fatalf("request = %s headers=%#v", r.URL.Path, r.Header)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"transaction_reference":"provider-1","merchant_transaction_reference":"pay-1","amount_received":20.28,"transaction_status":"Completed","transaction_type":"Card","current_status_date":"2026-07-31 12:00:00","currency":"NGN"}}`)
	})
	reconciliation, err := client.ReconcilePayment(context.Background(), payments.PaymentRecord{
		Reference: "pay-1", Provider: "payaza", Method: "hosted_checkout", CurrencyCode: "NGN",
		CurrencyExponent: 2, AmountMinor: money.Minor(2028),
	})
	if err != nil || reconciliation.Status != payments.PaymentStatusPaid || reconciliation.AmountMinor != 2028 || reconciliation.ProviderChannel != "card" {
		t.Fatalf("reconciliation = %#v, error = %v", reconciliation, err)
	}
}

func TestReconcileHostedPaymentKeepsUnknownReferencePending(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"Transaction not found","success":false,"data":null}`)
	})
	reconciliation, err := client.ReconcilePayment(context.Background(), payments.PaymentRecord{
		Reference: "pay-missing", Provider: "payaza", Method: "hosted_checkout",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
	})
	if err != nil || reconciliation.Status != payments.PaymentStatusPending || reconciliation.ProviderStatus != "not_found" {
		t.Fatalf("reconciliation = %#v, error = %v", reconciliation, err)
	}
}

func TestReconcileHostedPaymentReturnsProviderFailureReason(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":{"transaction_reference":"provider-1","merchant_transaction_reference":"pay-failed","amount_received":100,"transaction_status":"Failed","transaction_type":"Card","status_reason":"Invalid Transaction","currency":"NGN"}}`)
	})
	reconciliation, err := client.ReconcilePayment(context.Background(), payments.PaymentRecord{
		Reference: "pay-failed", Provider: "payaza", Method: "hosted_checkout",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
	})
	if err != nil || reconciliation.Status != payments.PaymentStatusFailed ||
		reconciliation.FailureMessage != "Invalid Transaction" || reconciliation.ProviderChannel != "card" {
		t.Fatalf("reconciliation = %#v, error = %v", reconciliation, err)
	}
}

func TestReconcileHostedPaymentRejectsMismatchedPaidReference(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"data":{"transaction_reference":"provider-1","merchant_transaction_reference":"other-reference","amount_received":100,"transaction_status":"Completed","transaction_type":"Card","currency":"NGN"}}`)
	})
	_, err := client.ReconcilePayment(context.Background(), payments.PaymentRecord{
		Reference: "pay-expected", Provider: "payaza", Method: "hosted_checkout",
		CurrencyCode: "NGN", CurrencyExponent: 2, AmountMinor: 10000,
	})
	if err == nil {
		t.Fatal("mismatched paid merchant reference was accepted")
	}
}

func TestParsePayazaTimeTreatsZoneLessProviderTimestampAsWAT(t *testing.T) {
	parsed := parsePayazaTime("2026-08-04 21:31:46.800304")
	if parsed == nil || !parsed.Equal(time.Date(2026, 8, 4, 20, 31, 46, 800304000, time.UTC)) {
		t.Fatalf("parsePayazaTime(zone-less) = %v", parsed)
	}

	explicit := parsePayazaTime("2026-08-04T21:31:46.800304+02:00")
	if explicit == nil || !explicit.Equal(time.Date(2026, 8, 4, 19, 31, 46, 800304000, time.UTC)) {
		t.Fatalf("parsePayazaTime(explicit offset) = %v", explicit)
	}
}

func TestReconcilePayoutVerifiesReferenceAndBeneficiary(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != payoutStatusPath+"pout-1" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"status":true,"data":{"transactionDateTime":"2026-07-31 12:00:00","transactionReference":"pout-1","creditAccount":"0123456789","bankCode":"000013","beneficiaryName":"JANE DOE","transactionAmount":20.28,"transactionStatus":"NIP_SUCCESS","currency":"NGN","isReversed":false}}`)
	})

	result, err := client.ReconcilePayout(context.Background(), payments.PayoutRecord{
		Reference: "pout-1", Provider: "payaza", CurrencyCode: "NGN", CurrencyExponent: 2,
		AmountMinor: money.Minor(2028), ExpectedRecipient: payments.ProviderRecipient{
			Identifier: "0123456789", InstitutionCode: "000013", AccountName: "Jane Doe",
		},
	})
	if err != nil || result.Status != payments.PayoutStatusSuccessful {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestReconcilePayoutReturnsTypedNotFoundError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":false,"message":"Transaction does not exist","data":null}`)
	})
	_, err := client.ReconcilePayout(context.Background(), payments.PayoutRecord{
		Reference: "pout-missing", Provider: "payaza", CurrencyCode: "NGN",
		CurrencyExponent: 2, AmountMinor: 10000,
	})
	if !errors.Is(err, ErrPayoutNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestWebhookVerificationUsesRawBodyAndSanitizesNormalizedData(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {})
	rawBody := []byte(`{"transaction_reference":"ref-1","transaction_type":"DEBIT","transaction_status":"NIP_SUCCESS","amount_received":20,"sent_to":{"account_number":"0123456789"}}`)
	mac := hmac.New(sha512.New, []byte("secret-key"))
	_, _ = mac.Write(rawBody)
	headers := make(http.Header)
	headers.Set("x-payaza-signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	event, err := client.VerifyAndDecodeWebhook(rawBody, headers)
	if err != nil {
		t.Fatalf("VerifyAndDecodeWebhook() error = %v", err)
	}
	if event.EventType != "debit.nip_success" || event.ProviderEventID != "" {
		t.Fatalf("event = %#v", event)
	}
	if _, leaked := event.Normalized["sent_to"]; leaked {
		t.Fatalf("normalized webhook leaked beneficiary details: %#v", event.Normalized)
	}
	headers.Set("x-payaza-signature", base64.StdEncoding.EncodeToString(make([]byte, sha512.Size)))
	if _, err := client.VerifyAndDecodeWebhook(rawBody, headers); err == nil {
		t.Fatal("VerifyAndDecodeWebhook() accepted an invalid signature")
	}
}

func TestWebhookLifecycleEventsUseBodyBasedDeduplication(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {})
	for _, rawBody := range [][]byte{
		[]byte(`{"transaction_reference":"pout-1","transaction_type":"DEBIT","transaction_status":"NIP_SUCCESS","is_reversed":false}`),
		[]byte(`{"transaction_reference":"pout-1","transaction_type":"DEBIT","transaction_status":"NIP_SUCCESS","is_reversed":true}`),
	} {
		mac := hmac.New(sha512.New, []byte("secret-key"))
		_, _ = mac.Write(rawBody)
		headers := make(http.Header)
		headers.Set("x-payaza-signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		event, err := client.VerifyAndDecodeWebhook(rawBody, headers)
		if err != nil || event.ProviderEventID != "" {
			t.Fatalf("event = %#v, error = %v", event, err)
		}
	}
}

func TestListDestinationsRejectsFailedDirectoryResponse(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":false,"message":"Unavailable","data":[]}`)
	})
	if _, err := client.ListDestinations(context.Background(), payments.DestinationQuery{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
	}); err == nil {
		t.Fatal("ListDestinations() accepted a failed Payaza response")
	}
}

func TestListDestinationsExcludesBlockedInstitutions(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":true,"data":[{"name":"Blocked Bank","code":"001","active":true,"type":"bank","currency_code":"NGN","block_transaction":true},{"name":"Open Bank","code":"002","active":true,"type":"bank","currency_code":"NGN","block_transaction":false}]}`)
	})
	options, err := client.ListDestinations(context.Background(), payments.DestinationQuery{
		CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account",
	})
	if err != nil || len(options) != 1 || options[0].Code != "002" {
		t.Fatalf("options = %#v, error = %v", options, err)
	}
}

func TestInitiatePayoutUsesConfiguredTenantSourceAndExactAmounts(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != payoutPath || r.Header.Get("X-TenantID") != "test" {
			t.Fatalf("request = %s headers=%#v", r.URL.Path, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"payout_amount":1234.56`) ||
			!strings.Contains(string(body), `"credit_amount":1234.56`) ||
			!strings.Contains(string(body), `"transaction_pin":419374`) ||
			!strings.Contains(string(body), `"account_reference":"1010000009"`) ||
			!strings.Contains(string(body), `"sender":{"sender_name":"TellBook","sender_phone_number":"08000000000","sender_address":"Lagos Nigeria"}`) {
			t.Fatalf("request body = %s", body)
		}
		_, _ = io.WriteString(w, `{"response_code":200,"response_content":{"transaction_status":"09","response_status":"TRANSACTION_INITIATED"}}`)
	})
	result, err := client.InitiatePayout(context.Background(), payments.PayoutSnapshot{
		PayoutID: uuid.New(), Reference: "pout-1", Provider: "payaza", Rail: "bank_account",
		CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2,
		AmountMinor: money.Minor(123456), Narration: "TellBook payout",
	}, payments.ProviderRecipient{
		InstitutionCode: "000013", Identifier: "9207067319", AccountName: "ADA OKAFOR",
	})
	if err != nil || result.Status != payments.PayoutStatusPending {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestInitiatePayoutRejectsInvalidNarration(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("provider should not be called")
	})
	_, err := client.InitiatePayout(context.Background(), payments.PayoutSnapshot{
		PayoutID: uuid.New(), Reference: "pout-2", Provider: "payaza", Rail: "bank_account",
		CountryCode: "NG", CurrencyCode: "NGN", CurrencyExponent: 2,
		AmountMinor: 10000, Narration: "TellBook payout: invalid",
	}, payments.ProviderRecipient{InstitutionCode: "000013", Identifier: "9207067319", AccountName: "ADA OKAFOR"})
	if err == nil {
		t.Fatal("InitiatePayout() accepted an invalid narration")
	}
}

func TestAvailablePayoutBalanceMatchesConfiguredSourceAccount(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mainAccountPath || r.Header.Get("X-TenantID") != "test" {
			t.Fatalf("request = %s headers=%#v", r.URL.Path, r.Header)
		}
		_, _ = io.WriteString(w, `{"status":true,"data":[{"status":"ACTIVE","currency":"NGN","accountBalance":990.13,"payazaAccountReference":"1010000009"}]}`)
	})
	balance, err := client.AvailablePayoutBalance(context.Background(), "NGN")
	if err != nil || balance != money.Minor(99013) {
		t.Fatalf("balance = %d, error = %v", balance, err)
	}
}

func TestParseProviderAmountAcceptsOnlySurplusZeroPrecision(t *testing.T) {
	amount, err := parseProviderAmount(json.Number("1000.000000"), 2)
	if err != nil || amount != money.Minor(100000) {
		t.Fatalf("amount = %d, error = %v", amount, err)
	}
	if _, err := parseProviderAmount(json.Number("1000.001"), 2); err == nil {
		t.Fatal("parseProviderAmount() accepted lossy precision")
	}
}
