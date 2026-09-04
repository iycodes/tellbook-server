package payaza

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
		if r.URL.Path != "/refund-chargeback/refund/merchant/api/refund" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input["transaction_reference"] != "pay-1" || input["refund_amount"] != 25.5 {
			t.Fatalf("unexpected input: %#v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Operation Completed","data":{"refund_transaction_reference":"RF-1","payment_transaction_reference":"pay-1","status":"SUCCESS","successful":true}}`))
	}))
	defer server.Close()
	client, err := NewClient(Config{PublicKey: "public", SecretKey: "secret", TenantID: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.InitiateRefund(context.Background(), payments.RefundRequest{
		RequestID: uuid.New(), Provider: "payaza", TransactionReference: "pay-1",
		AmountMinor: 2550, CurrencyCode: "NGN", CurrencyExponent: 2,
	})
	if err != nil || result.ProviderReference != "RF-1" || result.Status != payments.RefundInitiationSuccessful {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}
