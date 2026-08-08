package paystackclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"booking/go-server/internal/payments"
)

func TestInitializeCheckoutEnablesOnlySupportedHostedChannels(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/transaction/initialize" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		var body struct {
			Channels  []string `json:"channels"`
			Reference string   `json:"reference"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Join(body.Channels, ",") != "card" {
			t.Fatalf("channels = %#v", body.Channels)
		}
		return jsonResponse(`{"status":true,"message":"Authorization URL created","data":{"authorization_url":"https://checkout.paystack.com/access","access_code":"access","reference":"pay-1"}}`), nil
	})

	session, err := client.InitializeCheckout(context.Background(), payments.PaymentSnapshot{
		Reference: "pay-1", Provider: "paystack", Method: "hosted_checkout", AmountMinor: 10000,
		CurrencyCode: "NGN", CustomerEmail: "customer@example.com",
	})
	if err != nil || session.Flow != payments.CheckoutFlowHostedRedirect || session.CheckoutURL == "" {
		t.Fatalf("session = %#v, error = %v", session, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestListBanksUsesFiltersAndCursorPagination(t *testing.T) {
	requestCount := 0
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		requestCount++
		if req.URL.Path != listBanksPath {
			t.Fatalf("path = %s, want %s", req.URL.Path, listBanksPath)
		}
		if req.Header.Get("Authorization") != "Bearer secret-key" {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		query := req.URL.Query()
		if query.Get("country") != "nigeria" || query.Get("currency") != "NGN" || query.Get("type") != "nuban" {
			t.Fatalf("unexpected filters: %s", req.URL.RawQuery)
		}
		if query.Get("perPage") != "100" || query.Get("use_cursor") != "true" {
			t.Fatalf("unexpected pagination settings: %s", req.URL.RawQuery)
		}

		if requestCount == 1 {
			if query.Get("next") != "" {
				t.Fatalf("first next cursor = %q, want empty", query.Get("next"))
			}
			return jsonResponse(`{"status":true,"message":"Banks retrieved","data":[{"name":"Access Bank","code":"044","active":true,"is_deleted":false}],"meta":{"next":"cursor-2"}}`), nil
		}
		if query.Get("next") != "cursor-2" {
			t.Fatalf("second next cursor = %q, want cursor-2", query.Get("next"))
		}
		return jsonResponse(`{"status":true,"message":"Banks retrieved","data":[{"name":"Zenith Bank","code":"057","active":true,"is_deleted":false}],"meta":{"next":null}}`), nil
	})

	banks, err := client.ListBanks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
	if len(banks) != 2 || banks[1].Code != "057" {
		t.Fatalf("banks = %#v", banks)
	}
}

func TestResolveAccount(t *testing.T) {
	client := newTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != resolvePath {
			t.Fatalf("path = %s, want %s", req.URL.Path, resolvePath)
		}
		if req.URL.Query().Get("bank_code") != "057" {
			t.Fatalf("bank_code = %q", req.URL.Query().Get("bank_code"))
		}
		if req.URL.Query().Get("account_number") != "0123456789" {
			t.Fatalf("account_number = %q", req.URL.Query().Get("account_number"))
		}
		return jsonResponse(`{"status":true,"message":"Account number resolved","data":{"account_number":"0123456789","account_name":"DAMILARE OGUNNAIKE"}}`), nil
	})

	account, err := client.ResolveAccount(context.Background(), "057", "0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if account.AccountName != "DAMILARE OGUNNAIKE" {
		t.Fatalf("account name = %q", account.AccountName)
	}
}

func newTestClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	client, err := NewClient(Config{
		SecretKey:  "secret-key",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
