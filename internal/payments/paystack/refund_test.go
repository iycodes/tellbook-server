package paystackclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"booking/go-server/internal/payments"

	"github.com/google/uuid"
)

func TestInitiateRefund(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/refund" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input["transaction"] != "pay-1" || input["amount"] != float64(2500) {
			t.Fatalf("unexpected input: %#v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":true,"message":"queued","data":{"id":42,"status":"pending","currency":"NGN","amount":2500}}`))
	}))
	defer server.Close()
	client, err := NewClient(Config{SecretKey: "secret", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.InitiateRefund(context.Background(), payments.RefundRequest{
		RequestID: uuid.New(), Provider: "paystack", TransactionReference: "pay-1",
		AmountMinor: 2500, CurrencyCode: "NGN", CurrencyExponent: 2,
	})
	if err != nil || result.ProviderReference != "42" || result.Status != payments.RefundInitiationPending {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}
