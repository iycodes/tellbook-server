package appdata

import "testing"

func TestFormatMarketMoneyUsesMarketCurrencyRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		amountMinor  int64
		countryCode  string
		currencyCode string
		want         string
	}{
		{name: "ngn", amountMinor: 123456, countryCode: "NG", currencyCode: "NGN", want: "₦1,234.56"},
		{name: "zar", amountMinor: 123456, countryCode: "ZA", currencyCode: "ZAR", want: "R\u00a01\u00a0234,56"},
		{name: "xof", amountMinor: 123456, countryCode: "CI", currencyCode: "XOF", want: "123\u00a0456\u00a0CFA"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := formatMarketMoney(tt.amountMinor, tt.countryCode, tt.currencyCode)
			if err != nil {
				t.Fatalf("formatMarketMoney() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("formatMarketMoney() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatMarketMoneyRejectsMismatchedCurrency(t *testing.T) {
	t.Parallel()

	if _, err := formatMarketMoney(100, "NG", "GHS"); err == nil {
		t.Fatal("formatMarketMoney() accepted a currency outside the market")
	}
}

func TestParseServiceMoneyUsesCurrencyExponent(t *testing.T) {
	t.Parallel()

	if got, err := parseMoneyToMinor("1234.56", 2); err != nil || got != 123456 {
		t.Fatalf("two-decimal parse = %d, %v", got, err)
	}
	if got, err := parseMoneyToMinor("1234", 0); err != nil || got != 1234 {
		t.Fatalf("zero-decimal parse = %d, %v", got, err)
	}
	if _, err := parseMoneyToMinor("1234.50", 0); err == nil {
		t.Fatal("zero-decimal currency accepted a fractional amount")
	}
	if _, err := parseMoneyToMinor("$10.00", 2); err == nil {
		t.Fatal("money parser accepted a currency symbol")
	}
}
