package markets

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"booking/go-server/internal/money"

	"golang.org/x/text/language"
)

const CatalogVersion = "2026-07-30.1"

type Currency struct {
	Code               string               `json:"code"`
	DisplayName        string               `json:"display_name"`
	Symbol             string               `json:"symbol"`
	MinorUnitExponent  uint8                `json:"minor_unit_exponent"`
	DecimalSeparator   string               `json:"decimal_separator"`
	GroupingSeparator  string               `json:"grouping_separator"`
	SymbolPosition     money.SymbolPosition `json:"symbol_position"`
	SpaceBetweenSymbol bool                 `json:"space_between_symbol"`
}

type ProviderParameters struct {
	Country     string
	Currency    string
	AccountType string
}

type Market struct {
	CountryCode           string                        `json:"country_code"`
	DisplayName           string                        `json:"display_name"`
	DefaultCurrencyCode   string                        `json:"default_currency_code"`
	Currencies            []Currency                    `json:"currencies"`
	DefaultLocale         string                        `json:"default_locale"`
	AllowedLocales        []string                      `json:"allowed_locales"`
	DefaultTimezone       string                        `json:"default_timezone"`
	AllowedTimezones      []string                      `json:"allowed_timezones"`
	DialingCode           string                        `json:"dialing_code"`
	DistanceUnit          string                        `json:"distance_unit"`
	MarketEnabled         bool                          `json:"market_enabled"`
	OnlinePaymentsEnabled bool                          `json:"online_payments_enabled"`
	PayoutsEnabled        bool                          `json:"payouts_enabled"`
	PaymentMethods        []string                      `json:"payment_methods"`
	PayoutRails           []string                      `json:"payout_rails"`
	ProviderParameters    map[string]ProviderParameters `json:"-"`
}

type Catalog struct {
	version   string
	markets   []Market
	byCountry map[string]Market
}

var defaultCatalog = mustNewCatalog(CatalogVersion, []Market{
	{
		CountryCode:         "NG",
		DisplayName:         "Nigeria",
		DefaultCurrencyCode: "NGN",
		Currencies: []Currency{{
			Code: "NGN", DisplayName: "Nigerian naira", Symbol: "₦", MinorUnitExponent: 2,
			DecimalSeparator: ".", GroupingSeparator: ",", SymbolPosition: money.SymbolBefore,
		}},
		DefaultLocale: "en-NG", AllowedLocales: []string{"en-NG"},
		DefaultTimezone: "Africa/Lagos", AllowedTimezones: []string{"Africa/Lagos"},
		DialingCode: "+234", DistanceUnit: "km", MarketEnabled: true,
		PaymentMethods: []string{"manual"},
	},
	{
		CountryCode:         "GH",
		DisplayName:         "Ghana",
		DefaultCurrencyCode: "GHS",
		Currencies: []Currency{{
			Code: "GHS", DisplayName: "Ghanaian cedi", Symbol: "GH₵", MinorUnitExponent: 2,
			DecimalSeparator: ".", GroupingSeparator: ",", SymbolPosition: money.SymbolBefore,
		}},
		DefaultLocale: "en-GH", AllowedLocales: []string{"en-GH"},
		DefaultTimezone: "Africa/Accra", AllowedTimezones: []string{"Africa/Accra"},
		DialingCode: "+233", DistanceUnit: "km",
		PaymentMethods: []string{"manual"},
	},
	{
		CountryCode:         "KE",
		DisplayName:         "Kenya",
		DefaultCurrencyCode: "KES",
		Currencies: []Currency{{
			Code: "KES", DisplayName: "Kenyan shilling", Symbol: "KSh", MinorUnitExponent: 2,
			DecimalSeparator: ".", GroupingSeparator: ",", SymbolPosition: money.SymbolBefore, SpaceBetweenSymbol: true,
		}},
		DefaultLocale: "en-KE", AllowedLocales: []string{"en-KE"},
		DefaultTimezone: "Africa/Nairobi", AllowedTimezones: []string{"Africa/Nairobi"},
		DialingCode: "+254", DistanceUnit: "km",
		PaymentMethods: []string{"manual"},
	},
	{
		CountryCode:         "ZA",
		DisplayName:         "South Africa",
		DefaultCurrencyCode: "ZAR",
		Currencies: []Currency{{
			Code: "ZAR", DisplayName: "South African rand", Symbol: "R", MinorUnitExponent: 2,
			DecimalSeparator: ",", GroupingSeparator: "\u00a0", SymbolPosition: money.SymbolBefore, SpaceBetweenSymbol: true,
		}},
		DefaultLocale: "en-ZA", AllowedLocales: []string{"en-ZA"},
		DefaultTimezone: "Africa/Johannesburg", AllowedTimezones: []string{"Africa/Johannesburg"},
		DialingCode: "+27", DistanceUnit: "km",
		PaymentMethods: []string{"manual"},
	},
	{
		CountryCode:         "CI",
		DisplayName:         "Cote d'Ivoire",
		DefaultCurrencyCode: "XOF",
		Currencies: []Currency{{
			Code: "XOF", DisplayName: "West African CFA franc", Symbol: "CFA", MinorUnitExponent: 0,
			GroupingSeparator: "\u00a0", SymbolPosition: money.SymbolAfter, SpaceBetweenSymbol: true,
		}},
		DefaultLocale: "fr-CI", AllowedLocales: []string{"fr-CI"},
		DefaultTimezone: "Africa/Abidjan", AllowedTimezones: []string{"Africa/Abidjan"},
		DialingCode: "+225", DistanceUnit: "km",
		PaymentMethods: []string{"manual"},
	},
})

func DefaultCatalog() *Catalog {
	return defaultCatalog
}

func NewCatalog(version string, definitions []Market) (*Catalog, error) {
	version = strings.TrimSpace(version)
	if version == "" || strings.ContainsAny(version, "\"\r\n") {
		return nil, errors.New("catalog version is invalid")
	}
	if len(definitions) == 0 {
		return nil, errors.New("market catalog cannot be empty")
	}

	catalog := &Catalog{
		version:   version,
		markets:   make([]Market, 0, len(definitions)),
		byCountry: make(map[string]Market, len(definitions)),
	}
	for _, definition := range definitions {
		if err := validateMarket(definition); err != nil {
			return nil, fmt.Errorf("market %q: %w", definition.CountryCode, err)
		}
		if _, exists := catalog.byCountry[definition.CountryCode]; exists {
			return nil, fmt.Errorf("duplicate market country code %q", definition.CountryCode)
		}
		copied := cloneMarket(definition)
		catalog.markets = append(catalog.markets, copied)
		catalog.byCountry[copied.CountryCode] = copied
	}
	sort.Slice(catalog.markets, func(i, j int) bool {
		return catalog.markets[i].DisplayName < catalog.markets[j].DisplayName
	})
	return catalog, nil
}

func (c *Catalog) Version() string {
	return c.version
}

func (c *Catalog) ETag() string {
	return `"markets-` + c.version + `"`
}

func (c *Catalog) All() []Market {
	items := make([]Market, len(c.markets))
	for i, market := range c.markets {
		items[i] = cloneMarket(market)
	}
	return items
}

func (c *Catalog) Lookup(countryCode string) (Market, bool) {
	market, ok := c.byCountry[strings.ToUpper(strings.TrimSpace(countryCode))]
	if !ok {
		return Market{}, false
	}
	return cloneMarket(market), true
}

func (c *Catalog) ValidateConfiguration(countryCode, currencyCode, timezone, locale string) error {
	market, ok := c.Lookup(countryCode)
	if !ok || !market.MarketEnabled {
		return errors.New("country is not an enabled market")
	}
	if !containsCurrency(market.Currencies, currencyCode) {
		return errors.New("currency is not enabled for the market")
	}
	if !contains(market.AllowedTimezones, timezone) {
		return errors.New("timezone is not enabled for the market")
	}
	if !contains(market.AllowedLocales, locale) {
		return errors.New("locale is not enabled for the market")
	}
	return nil
}

func validateMarket(market Market) error {
	if !isUpperAlphaCode(market.CountryCode, 2) {
		return errors.New("country code must be ISO 3166-1 alpha-2 format")
	}
	if strings.TrimSpace(market.DisplayName) == "" {
		return errors.New("display name is required")
	}
	if !isUpperAlphaCode(market.DefaultCurrencyCode, 3) {
		return errors.New("default currency code must be ISO 4217 format")
	}
	if len(market.Currencies) == 0 {
		return errors.New("at least one currency is required")
	}

	currencyCodes := make(map[string]struct{}, len(market.Currencies))
	for _, currency := range market.Currencies {
		if !isUpperAlphaCode(currency.Code, 3) {
			return errors.New("currency code must be ISO 4217 format")
		}
		if _, exists := currencyCodes[currency.Code]; exists {
			return fmt.Errorf("duplicate currency %q", currency.Code)
		}
		currencyCodes[currency.Code] = struct{}{}
		if strings.TrimSpace(currency.DisplayName) == "" || strings.TrimSpace(currency.Symbol) == "" {
			return fmt.Errorf("currency %q requires a display name and symbol", currency.Code)
		}
		if currency.MinorUnitExponent > money.MaxMinorUnitExponent {
			return fmt.Errorf("currency %q has an invalid minor-unit exponent", currency.Code)
		}
		if currency.SymbolPosition != money.SymbolBefore && currency.SymbolPosition != money.SymbolAfter {
			return fmt.Errorf("currency %q has an invalid symbol position", currency.Code)
		}
		if currency.MinorUnitExponent > 0 && currency.DecimalSeparator == "" {
			return fmt.Errorf("currency %q requires a decimal separator", currency.Code)
		}
		if currency.DecimalSeparator != "" && currency.DecimalSeparator == currency.GroupingSeparator {
			return fmt.Errorf("currency %q separators must differ", currency.Code)
		}
	}
	if _, ok := currencyCodes[market.DefaultCurrencyCode]; !ok {
		return errors.New("default currency must be in the enabled currency list")
	}

	if err := validateLocales(market.DefaultLocale, market.AllowedLocales); err != nil {
		return err
	}
	if err := validateTimezones(market.DefaultTimezone, market.AllowedTimezones); err != nil {
		return err
	}
	if len(market.DialingCode) < 2 || market.DialingCode[0] != '+' || !digitsOnly(market.DialingCode[1:]) {
		return errors.New("dialing code must start with + and contain digits")
	}
	if market.DistanceUnit != "km" && market.DistanceUnit != "mi" {
		return errors.New("distance unit must be km or mi")
	}
	if !contains(market.PaymentMethods, "manual") {
		return errors.New("manual payment must remain available for every booking market")
	}
	if market.OnlinePaymentsEnabled && len(market.PaymentMethods) < 2 {
		return errors.New("online payments require an enabled online payment method")
	}
	if market.PayoutsEnabled && len(market.PayoutRails) == 0 {
		return errors.New("payouts require at least one enabled payout rail")
	}
	return nil
}

func validateLocales(defaultLocale string, allowed []string) error {
	if len(allowed) == 0 || !contains(allowed, defaultLocale) {
		return errors.New("default locale must be in the allowed locale list")
	}
	seen := make(map[string]struct{}, len(allowed))
	for _, locale := range allowed {
		if locale == "" {
			return errors.New("locale cannot be empty")
		}
		if _, err := language.Parse(locale); err != nil {
			return fmt.Errorf("locale %q is invalid", locale)
		}
		if _, exists := seen[locale]; exists {
			return fmt.Errorf("duplicate locale %q", locale)
		}
		seen[locale] = struct{}{}
	}
	return nil
}

func validateTimezones(defaultTimezone string, allowed []string) error {
	if len(allowed) == 0 || !contains(allowed, defaultTimezone) {
		return errors.New("default timezone must be in the allowed timezone list")
	}
	seen := make(map[string]struct{}, len(allowed))
	for _, timezone := range allowed {
		if _, err := time.LoadLocation(timezone); err != nil {
			return fmt.Errorf("timezone %q is invalid", timezone)
		}
		if _, exists := seen[timezone]; exists {
			return fmt.Errorf("duplicate timezone %q", timezone)
		}
		seen[timezone] = struct{}{}
	}
	return nil
}

func cloneMarket(market Market) Market {
	market.Currencies = append([]Currency(nil), market.Currencies...)
	market.AllowedLocales = append([]string(nil), market.AllowedLocales...)
	market.AllowedTimezones = append([]string(nil), market.AllowedTimezones...)
	market.PaymentMethods = append([]string(nil), market.PaymentMethods...)
	market.PayoutRails = append([]string(nil), market.PayoutRails...)
	if market.ProviderParameters != nil {
		providerParameters := market.ProviderParameters
		market.ProviderParameters = make(map[string]ProviderParameters, len(providerParameters))
		for provider, parameters := range providerParameters {
			market.ProviderParameters[provider] = parameters
		}
	}
	return market
}

func mustNewCatalog(version string, definitions []Market) *Catalog {
	catalog, err := NewCatalog(version, definitions)
	if err != nil {
		panic(err)
	}
	return catalog
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsCurrency(currencies []Currency, expected string) bool {
	for _, currency := range currencies {
		if currency.Code == expected {
			return true
		}
	}
	return false
}

func isUpperAlphaCode(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 'A' || value[i] > 'Z' {
			return false
		}
	}
	return true
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
