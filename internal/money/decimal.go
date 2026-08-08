package money

import (
	"errors"
	"strconv"
	"strings"
)

const MaxMinorUnitExponent uint8 = 18

var (
	ErrInvalidDecimal  = errors.New("invalid decimal amount")
	ErrAmountOverflow  = errors.New("amount exceeds signed 64-bit minor units")
	ErrInvalidExponent = errors.New("invalid minor-unit exponent")
)

// ParseDecimal converts a normalized decimal amount into signed minor units.
func ParseDecimal(value string, exponent uint8) (int64, error) {
	if exponent > MaxMinorUnitExponent {
		return 0, ErrInvalidExponent
	}
	if value == "" || strings.TrimSpace(value) != value {
		return 0, ErrInvalidDecimal
	}

	negative := value[0] == '-'
	if negative {
		value = value[1:]
		if value == "" {
			return 0, ErrInvalidDecimal
		}
	}
	if value[0] == '+' {
		return 0, ErrInvalidDecimal
	}

	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || !isASCIIDigits(parts[0]) {
		return 0, ErrInvalidDecimal
	}

	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || !isASCIIDigits(fraction) {
			return 0, ErrInvalidDecimal
		}
	}
	if len(fraction) > int(exponent) {
		return 0, ErrInvalidDecimal
	}

	minorDigits := parts[0] + fraction + strings.Repeat("0", int(exponent)-len(fraction))
	magnitude, err := strconv.ParseUint(minorDigits, 10, 64)
	if err != nil {
		return 0, ErrAmountOverflow
	}

	const maxInt64Magnitude = ^uint64(0) >> 1
	limit := maxInt64Magnitude
	if negative {
		limit++
	}
	if magnitude > limit {
		return 0, ErrAmountOverflow
	}
	if magnitude == 0 {
		return 0, nil
	}
	if negative {
		if magnitude == maxInt64Magnitude+1 {
			return -1 << 63, nil
		}
		return -int64(magnitude), nil
	}
	return int64(magnitude), nil
}

// FormatDecimal converts signed minor units into a canonical decimal string.
func FormatDecimal(amount int64, exponent uint8) (string, error) {
	if exponent > MaxMinorUnitExponent {
		return "", ErrInvalidExponent
	}

	negative := amount < 0
	var magnitude uint64
	if negative {
		magnitude = uint64(-(amount + 1))
		magnitude++
	} else {
		magnitude = uint64(amount)
	}

	digits := strconv.FormatUint(magnitude, 10)
	if exponent > 0 {
		requiredLength := int(exponent) + 1
		if len(digits) < requiredLength {
			digits = strings.Repeat("0", requiredLength-len(digits)) + digits
		}
		splitAt := len(digits) - int(exponent)
		digits = digits[:splitAt] + "." + digits[splitAt:]
	}
	if negative {
		return "-" + digits, nil
	}
	return digits, nil
}

func isASCIIDigits(value string) bool {
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
