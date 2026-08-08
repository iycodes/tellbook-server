package appdata

import (
	"fmt"

	"booking/go-server/internal/markets"
	"booking/go-server/internal/money"
)

func formatMarketMoney(amountMinor int64, countryCode, currencyCode string) (string, error) {
	market, ok := markets.DefaultCatalog().Lookup(countryCode)
	if !ok {
		return "", fmt.Errorf("unsupported market country %q", countryCode)
	}
	for _, currency := range market.Currencies {
		if currency.Code != currencyCode {
			continue
		}
		return money.Format(amountMinor, money.FormatSpec{
			CurrencyCode:       currency.Code,
			Symbol:             currency.Symbol,
			Exponent:           currency.MinorUnitExponent,
			DecimalSeparator:   currency.DecimalSeparator,
			GroupingSeparator:  currency.GroupingSeparator,
			SymbolPosition:     currency.SymbolPosition,
			SpaceBetweenSymbol: currency.SpaceBetweenSymbol,
		})
	}
	return "", fmt.Errorf("currency %q is not configured for market %q", currencyCode, countryCode)
}
