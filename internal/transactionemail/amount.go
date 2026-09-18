package transactionemail

import (
	"booking/go-server/internal/money"
	"strings"
)

func formatEmailAmount(minor int64, currency string, exponent uint8) (string, error) {
	if _, err := money.NewAmount(minor, currency); err != nil {
		return "", err
	}
	amount, err := money.FormatDecimal(minor, exponent)
	if err != nil {
		return "", err
	}
	parts := strings.SplitN(amount, ".", 2)
	for i := len(parts[0]) - 3; i > 0; i -= 3 {
		parts[0] = parts[0][:i] + "," + parts[0][i:]
	}
	return strings.Join(parts, "."), nil
}

// Fit long amounts in narrow clients, including those that strip style tags.
func emailAmountSize(amount string) int {
	switch {
	case len(amount) > 21:
		return 12
	case len(amount) > 17:
		return 15
	case len(amount) > 13:
		return 18
	case len(amount) > 10:
		return 23
	default:
		return 30
	}
}
