package markets

import "testing"

func TestDefaultCatalogContainsGhanaDefaults(t *testing.T) {
	t.Parallel()

	market, ok := DefaultCatalog().Lookup("gh")
	if !ok {
		t.Fatal("Ghana market was not found")
	}
	if market.DefaultCurrencyCode != "GHS" {
		t.Fatalf("default currency = %q, want GHS", market.DefaultCurrencyCode)
	}
	if market.DefaultTimezone != "Africa/Accra" || market.DefaultLocale != "en-GH" {
		t.Fatalf("unexpected Ghana defaults: %#v", market)
	}
	if market.MarketEnabled || market.OnlinePaymentsEnabled || market.PayoutsEnabled {
		t.Fatal("Ghana must remain disabled until profile persistence and provider capabilities are activated")
	}
}

func TestCatalogResultsCannotMutateDefinitions(t *testing.T) {
	t.Parallel()

	first, ok := DefaultCatalog().Lookup("NG")
	if !ok {
		t.Fatal("Nigeria market was not found")
	}
	first.Currencies[0].Code = "USD"
	first.PaymentMethods[0] = "card"

	second, _ := DefaultCatalog().Lookup("NG")
	if second.Currencies[0].Code != "NGN" || second.PaymentMethods[0] != "manual" {
		t.Fatalf("catalog definition was mutated: %#v", second)
	}
}

func TestCatalogCopiesInternalProviderParameters(t *testing.T) {
	t.Parallel()

	base := DefaultCatalog().All()[0]
	base.CountryCode = "AA"
	base.DisplayName = "Example"
	base.ProviderParameters = map[string]ProviderParameters{
		"example": {Country: "example-country", Currency: base.DefaultCurrencyCode},
	}
	catalog, err := NewCatalog("test", []Market{base})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}

	first, _ := catalog.Lookup("AA")
	first.ProviderParameters["example"] = ProviderParameters{Country: "mutated"}
	second, _ := catalog.Lookup("AA")
	if second.ProviderParameters["example"].Country != "example-country" {
		t.Fatalf("provider parameters were mutated: %#v", second.ProviderParameters)
	}
}

func TestValidateConfiguration(t *testing.T) {
	t.Parallel()

	catalog := DefaultCatalog()
	if err := catalog.ValidateConfiguration("NG", "NGN", "Africa/Lagos", "en-NG"); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	for name, values := range map[string][4]string{
		"country":  {"US", "USD", "America/New_York", "en-US"},
		"disabled": {"KE", "KES", "Africa/Nairobi", "en-KE"},
		"currency": {"NG", "GHS", "Africa/Lagos", "en-NG"},
		"timezone": {"NG", "NGN", "Africa/Accra", "en-NG"},
		"locale":   {"NG", "NGN", "Africa/Lagos", "en-GH"},
	} {
		if err := catalog.ValidateConfiguration(values[0], values[1], values[2], values[3]); err == nil {
			t.Fatalf("%s mismatch unexpectedly accepted", name)
		}
	}
}

func TestNewCatalogRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	_, err := NewCatalog("test", []Market{{
		CountryCode: "GH",
		DisplayName: "Ghana",
	}})
	if err == nil {
		t.Fatal("NewCatalog() accepted an incomplete market")
	}
}
