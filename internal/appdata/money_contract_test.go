package appdata

import (
	"encoding/json"
	"strings"
	"testing"

	"booking/go-server/internal/money"
)

func TestPublicMoneyJSONUsesQuotedMinorAmounts(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(PublicBookingSummaryResponse{
		OriginalAmountMinor: money.Minor(9007199254740993),
		DiscountAmountMinor: money.Minor(100),
		TotalAmountMinor:    money.Minor(9007199254740893),
		DepositAmountMinor:  money.Minor(500),
		CountryCode:         "GH",
		CurrencyCode:        "GHS",
		Timezone:            "Africa/Accra",
		Locale:              "en-GH",
	})
	if err != nil {
		t.Fatalf("marshal public booking summary: %v", err)
	}

	encoded := string(payload)
	for _, expected := range []string{
		`"original_amount_minor":"9007199254740993"`,
		`"discount_amount_minor":"100"`,
		`"total_amount_minor":"9007199254740893"`,
		`"deposit_amount_minor":"500"`,
		`"country_code":"GH"`,
		`"currency_code":"GHS"`,
	} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("encoded payload %s does not contain %s", encoded, expected)
		}
	}
}

func TestPromotionMoneyInputRequiresQuotedMinorAmount(t *testing.T) {
	t.Parallel()

	var input CreatePromotionInput
	if err := json.Unmarshal([]byte(`{
		"minimum_spend_minor":"9007199254740993",
		"discount_value_minor":"9007199254740995"
	}`), &input); err != nil {
		t.Fatalf("unmarshal quoted minor amount: %v", err)
	}
	if input.MinimumSpendMinor != money.Minor(9007199254740993) {
		t.Fatalf("minimum spend = %d", input.MinimumSpendMinor)
	}
	if input.DiscountValueMinor != money.Minor(9007199254740995) {
		t.Fatalf("discount value = %d", input.DiscountValueMinor)
	}

	if err := json.Unmarshal([]byte(`{"discount_value_minor":100}`), &input); err == nil {
		t.Fatal("unquoted minor amount unexpectedly accepted")
	}
}
