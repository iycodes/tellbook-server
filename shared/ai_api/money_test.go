package aiapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMinorAmountJSONPreservesPrecision(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(GenerateServiceDescriptionRequest{
		ServiceTitle:     "Lash extensions",
		PriceAmountMinor: MinorAmount(9007199254740993),
		CurrencyCode:     "NGN",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(payload), `"price_amount_minor":"9007199254740993"`) {
		t.Fatalf("encoded payload = %s", payload)
	}

	var decoded GenerateServiceDescriptionRequest
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decoded.PriceAmountMinor != MinorAmount(9007199254740993) {
		t.Fatalf("decoded amount = %d", decoded.PriceAmountMinor)
	}
}
