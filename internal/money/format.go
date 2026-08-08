package money

import (
	"errors"
	"strings"
	"unicode/utf8"
)

type SymbolPosition string

const (
	SymbolBefore SymbolPosition = "before"
	SymbolAfter  SymbolPosition = "after"
)

type FormatSpec struct {
	CurrencyCode       string
	Symbol             string
	Exponent           uint8
	DecimalSeparator   string
	GroupingSeparator  string
	SymbolPosition     SymbolPosition
	SpaceBetweenSymbol bool
}

func Format(amount int64, spec FormatSpec) (string, error) {
	if !isCurrencyCode(spec.CurrencyCode) {
		return "", ErrInvalidCurrencyCode
	}
	if spec.Exponent > MaxMinorUnitExponent {
		return "", ErrInvalidExponent
	}
	if spec.SymbolPosition != SymbolBefore && spec.SymbolPosition != SymbolAfter {
		return "", errors.New("invalid currency symbol position")
	}
	if spec.Exponent > 0 && spec.DecimalSeparator == "" {
		return "", errors.New("decimal separator is required")
	}
	if containsASCIIDigit(spec.DecimalSeparator) || containsASCIIDigit(spec.GroupingSeparator) {
		return "", errors.New("number separators cannot contain digits")
	}
	if spec.DecimalSeparator != "" && spec.DecimalSeparator == spec.GroupingSeparator {
		return "", errors.New("decimal and grouping separators must differ")
	}

	decimal, err := FormatDecimal(amount, spec.Exponent)
	if err != nil {
		return "", err
	}

	negative := strings.HasPrefix(decimal, "-")
	if negative {
		decimal = strings.TrimPrefix(decimal, "-")
	}

	parts := strings.SplitN(decimal, ".", 2)
	formatted := groupDigits(parts[0], spec.GroupingSeparator)
	if len(parts) == 2 {
		formatted += spec.DecimalSeparator + parts[1]
	}

	symbol := spec.Symbol
	if symbol == "" {
		symbol = spec.CurrencyCode
	}
	space := ""
	if spec.SpaceBetweenSymbol {
		space = "\u00a0"
	}
	if spec.SymbolPosition == SymbolBefore {
		formatted = symbol + space + formatted
	} else {
		formatted = formatted + space + symbol
	}
	if negative {
		formatted = "-" + formatted
	}
	return formatted, nil
}

func groupDigits(digits, separator string) string {
	if separator == "" || len(digits) <= 3 {
		return digits
	}

	firstGroupLength := len(digits) % 3
	if firstGroupLength == 0 {
		firstGroupLength = 3
	}

	var builder strings.Builder
	builder.Grow(len(digits) + (len(digits)-1)/3*utf8.RuneCountInString(separator))
	builder.WriteString(digits[:firstGroupLength])
	for offset := firstGroupLength; offset < len(digits); offset += 3 {
		builder.WriteString(separator)
		builder.WriteString(digits[offset : offset+3])
	}
	return builder.String()
}

func containsASCIIDigit(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= '0' && value[i] <= '9' {
			return true
		}
	}
	return false
}
