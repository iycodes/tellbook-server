package payments

import (
	"encoding/json"
	"testing"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
)

func TestAdjustmentFromPaystackRefundWebhook(t *testing.T) {
	payment := FinancialPayment{
		ID: uuid.New(), Provider: "paystack", CurrencyCode: "NGN", AmountMinor: money.Minor(10000),
	}
	event := StoredWebhookEvent{ID: uuid.New(), Provider: "paystack", EventType: "refund.processed"}
	input, handled, err := adjustmentFromWebhook(event, json.RawMessage(`{
		"transaction_reference":"pay_test","refund_reference":"refund_test",
		"amount":"2500","currency":"NGN"
	}`), payment)
	if err != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, err)
	}
	if input.Kind != "partial_refund" || input.Status != "successful" ||
		input.AmountMinor != 2500 || input.ProviderReference != "refund_test" {
		t.Fatalf("input = %#v", input)
	}
}

func TestAdjustmentFromPaystackDisputeResolution(t *testing.T) {
	payment := FinancialPayment{
		ID: uuid.New(), Provider: "paystack", CurrencyCode: "NGN", AmountMinor: money.Minor(10000),
	}
	event := StoredWebhookEvent{ID: uuid.New(), Provider: "paystack", EventType: "charge.dispute.resolve"}
	input, handled, err := adjustmentFromWebhook(event, json.RawMessage(`{
		"reference":"pay_test","id":42,"refund_amount":4000,"resolution":"declined"
	}`), payment)
	if err != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, err)
	}
	if input.Kind != "dispute" || input.Status != "failed" ||
		input.AmountMinor != 4000 || input.ProviderReference != "42" {
		t.Fatalf("input = %#v", input)
	}
}

func TestAdjustmentFromPayazaReversal(t *testing.T) {
	payment := FinancialPayment{
		ID: uuid.New(), Provider: "payaza", CurrencyCode: "NGN", AmountMinor: money.Minor(10000),
	}
	event := StoredWebhookEvent{ID: uuid.New(), Provider: "payaza", EventType: "collection.completed"}
	input, handled, err := adjustmentFromWebhook(event, json.RawMessage(`{"is_reversed":true}`), payment)
	if err != nil || !handled {
		t.Fatalf("handled = %v, error = %v", handled, err)
	}
	if input.Kind != "reversal" || input.Status != "successful" || input.AmountMinor != 10000 {
		t.Fatalf("input = %#v", input)
	}
}

func TestWebhookReconciliationCompleteRejectsNonterminalProviderState(t *testing.T) {
	for _, status := range []PaymentStatus{PaymentStatusCreated, PaymentStatusPending, PaymentStatusRequiresAction} {
		if webhookReconciliationComplete(status) {
			t.Fatalf("webhookReconciliationComplete(%q) = true", status)
		}
	}
	for _, status := range []PaymentStatus{
		PaymentStatusPaid, PaymentStatusPartiallyRefunded, PaymentStatusRefunded,
		PaymentStatusDisputed, PaymentStatusReversed, PaymentStatusFailed,
		PaymentStatusExpired, PaymentStatusCancelled,
	} {
		if !webhookReconciliationComplete(status) {
			t.Fatalf("webhookReconciliationComplete(%q) = false", status)
		}
	}
}
