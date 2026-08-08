package payaza

import (
	"encoding/json"
	"errors"
	"strings"

	"booking/go-server/internal/money"
)

type decimalNumber string

func newDecimalNumber(amountMinor int64, exponent uint8) (decimalNumber, error) {
	if amountMinor <= 0 {
		return "", errors.New("payaza amount must be positive")
	}
	value, err := money.FormatDecimal(amountMinor, exponent)
	return decimalNumber(value), err
}

func (d decimalNumber) MarshalJSON() ([]byte, error) {
	value := string(d)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return nil, errors.New("invalid payaza decimal amount")
	}
	return []byte(value), nil
}

func parseProviderAmount(value json.Number, exponent uint8) (money.Minor, error) {
	normalized := value.String()
	whole, fraction, hasFraction := strings.Cut(normalized, ".")
	if hasFraction && len(fraction) > int(exponent) {
		extra := fraction[int(exponent):]
		if strings.Trim(extra, "0") != "" {
			return 0, errors.New("payaza amount has unsupported precision")
		}
		fraction = fraction[:int(exponent)]
		normalized = whole
		if exponent > 0 {
			normalized += "." + fraction
		}
	}
	parsed, err := money.ParseDecimal(normalized, exponent)
	return money.Minor(parsed), err
}
