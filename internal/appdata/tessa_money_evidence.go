package appdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"booking/go-server/internal/markets"
	"booking/go-server/internal/money"
)

// Convert integer amounts using the same currency catalog as the application.
// Keep raw evidence for audit, but give the model an unambiguous display value.
// Currency metadata can be on the parent of nested metrics; child currencies
// take precedence. No floats, guessed exponents, exchange rates or DB round trips.
func formatTessaMoneyEvidence(payload []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var visit func(any, string) error
	visit = func(node any, currency string) error {
		switch node := node.(type) {
		case []any:
			for _, child := range node {
				if err := visit(child, currency); err != nil {
					return err
				}
			}
		case map[string]any:
			if code, ok := node["currency_code"].(string); ok {
				currency = code
			}
			for key, child := range node {
				if strings.HasSuffix(key, "_minor") {
					var raw string
					switch n := child.(type) {
					case json.Number:
						raw = n.String()
					case string:
						raw = n
					default:
						continue
					}
					amount, err := strconv.ParseInt(raw, 10, 64)
					if err != nil {
						return fmt.Errorf("invalid Tessa monetary evidence %s: %w", key, err)
					}
					displayKey := strings.TrimSuffix(key, "_minor") + "_display"
					node[displayKey] = nil // Unknown currency is unavailable, never guessed.
					if spec, ok := tessaCurrencyFormats[currency]; ok {
						display, err := money.Format(amount, spec)
						if err != nil {
							return err
						}
						node[displayKey] = display
					}
				} else if err := visit(child, currency); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(value, ""); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

var tessaCurrencyFormats = func() map[string]money.FormatSpec {
	result := map[string]money.FormatSpec{}
	for _, market := range markets.DefaultCatalog().All() {
		for _, c := range market.Currencies {
			result[c.Code] = money.FormatSpec{CurrencyCode: c.Code, Symbol: c.Symbol, Exponent: c.MinorUnitExponent,
				DecimalSeparator: c.DecimalSeparator, GroupingSeparator: c.GroupingSeparator,
				SymbolPosition: c.SymbolPosition, SpaceBetweenSymbol: c.SpaceBetweenSymbol}
		}
	}
	return result
}()
