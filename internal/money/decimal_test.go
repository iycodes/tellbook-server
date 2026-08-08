package money

import (
	"errors"
	"math"
	"testing"
)

func TestParseDecimal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		exponent uint8
		want     int64
	}{
		{name: "zero decimal", value: "500", exponent: 0, want: 500},
		{name: "two decimals", value: "1234.56", exponent: 2, want: 123456},
		{name: "pads fraction", value: "12.5", exponent: 2, want: 1250},
		{name: "three decimals", value: "1.234", exponent: 3, want: 1234},
		{name: "negative", value: "-0.01", exponent: 2, want: -1},
		{name: "maximum int64", value: "92233720368547758.07", exponent: 2, want: math.MaxInt64},
		{name: "minimum int64", value: "-92233720368547758.08", exponent: 2, want: math.MinInt64},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseDecimal(tt.value, tt.exponent)
			if err != nil {
				t.Fatalf("ParseDecimal() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseDecimal() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseDecimalRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value    string
		exponent uint8
		wantErr  error
	}{
		{value: "", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: " 1.00", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: "+1.00", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: ".50", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: "1.", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: "1.001", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: "1.0", exponent: 0, wantErr: ErrInvalidDecimal},
		{value: "1,000.00", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: "NGN 1.00", exponent: 2, wantErr: ErrInvalidDecimal},
		{value: "92233720368547758.08", exponent: 2, wantErr: ErrAmountOverflow},
		{value: "-92233720368547758.09", exponent: 2, wantErr: ErrAmountOverflow},
		{value: "1", exponent: MaxMinorUnitExponent + 1, wantErr: ErrInvalidExponent},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDecimal(tt.value, tt.exponent)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseDecimal() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestFormatDecimal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		amount   int64
		exponent uint8
		want     string
	}{
		{amount: 500, exponent: 0, want: "500"},
		{amount: 1, exponent: 2, want: "0.01"},
		{amount: -1, exponent: 3, want: "-0.001"},
		{amount: math.MaxInt64, exponent: 2, want: "92233720368547758.07"},
		{amount: math.MinInt64, exponent: 2, want: "-92233720368547758.08"},
	}
	for _, tt := range tests {
		got, err := FormatDecimal(tt.amount, tt.exponent)
		if err != nil {
			t.Fatalf("FormatDecimal() error = %v", err)
		}
		if got != tt.want {
			t.Fatalf("FormatDecimal(%d, %d) = %q, want %q", tt.amount, tt.exponent, got, tt.want)
		}
	}
}
