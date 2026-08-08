package money

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAmountJSONPreservesMinorUnitPrecision(t *testing.T) {
	t.Parallel()

	amount, err := NewAmount(9007199254740993, "NGN")
	if err != nil {
		t.Fatalf("NewAmount() error = %v", err)
	}
	encoded, err := json.Marshal(amount)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	got := string(encoded)
	if !strings.Contains(got, `"amount_minor":"9007199254740993"`) {
		t.Fatalf("encoded amount = %s", got)
	}

	var decoded Amount
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.AmountMinor != amount.AmountMinor || decoded.CurrencyCode != amount.CurrencyCode {
		t.Fatalf("decoded amount = %#v, want %#v", decoded, amount)
	}
}

func TestMinorRejectsJSONNumberAndNonCanonicalString(t *testing.T) {
	t.Parallel()

	for _, input := range []string{`100`, `"01"`, `"-01"`, `"-0"`, `"+1"`, `" 1"`} {
		var amount Minor
		if err := json.Unmarshal([]byte(input), &amount); err == nil {
			t.Fatalf("json.Unmarshal(%s) unexpectedly succeeded", input)
		}
	}
}

func TestNewAmountRejectsInvalidCurrency(t *testing.T) {
	t.Parallel()

	for _, currency := range []string{"ngn", "NG", "NGN "} {
		if _, err := NewAmount(100, currency); err == nil {
			t.Fatalf("NewAmount() accepted currency %q", currency)
		}
	}
}
