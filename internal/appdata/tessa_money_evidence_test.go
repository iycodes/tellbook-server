package appdata

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTessaMoneyEvidenceUsesCurrencyUnitsAndExactIntegers(t *testing.T) {
	for _, tc := range []struct{ input, field, want string }{
		{`{"currency_code":"NGN","price_amount_minor":2500000}`, "price_amount_display", "₦25,000.00"},
		{`{"currency_code":"NGN","revenue_minor":0}`, "revenue_display", "₦0.00"},
		{`{"currency_code":"NGN","balance_minor":-123}`, "balance_display", "-₦1.23"},
		{`{"currency_code":"XOF","price_minor":25000}`, "price_display", "25\u00a0000\u00a0CFA"},
		{`{"currency_code":"NGN","amount_minor":"9007199254740993"}`, "amount_display", "₦90,071,992,547,409.93"},
	} {
		payload, err := formatTessaMoneyEvidence([]byte(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err = json.Unmarshal(payload, &value); err != nil {
			t.Fatal(err)
		}
		if value[tc.field] != tc.want {
			t.Errorf("%s: got %v, want %s", tc.input, value[tc.field], tc.want)
		}
	}
	result, err := formatTessaMoneyEvidence([]byte(`{"currency_code":"NGN","metrics":{"amount_minor":100},"other":{"currency_code":"XXX","amount_minor":100}}`))
	if err != nil || !strings.Contains(string(result), `"amount_display":"₦1.00"`) || !strings.Contains(string(result), `"amount_display":null`) {
		t.Fatal(string(result), err)
	}
	if _, err = formatTessaMoneyEvidence([]byte(`{"currency_code":"NGN","amount_minor":1.5}`)); err == nil {
		t.Fatal("fractional minor units accepted")
	}
}
