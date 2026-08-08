package money

import "testing"

func TestFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		amount int64
		spec   FormatSpec
		want   string
	}{
		{
			name:   "naira",
			amount: 123456,
			spec: FormatSpec{
				CurrencyCode: "NGN", Symbol: "₦", Exponent: 2,
				DecimalSeparator: ".", GroupingSeparator: ",", SymbolPosition: SymbolBefore,
			},
			want: "₦1,234.56",
		},
		{
			name:   "south african locale",
			amount: 123456,
			spec: FormatSpec{
				CurrencyCode: "ZAR", Symbol: "R", Exponent: 2,
				DecimalSeparator: ",", GroupingSeparator: "\u00a0", SymbolPosition: SymbolBefore, SpaceBetweenSymbol: true,
			},
			want: "R\u00a01\u00a0234,56",
		},
		{
			name:   "zero decimal negative",
			amount: -123456,
			spec: FormatSpec{
				CurrencyCode: "XOF", Symbol: "CFA", Exponent: 0,
				GroupingSeparator: "\u00a0", SymbolPosition: SymbolAfter, SpaceBetweenSymbol: true,
			},
			want: "-123\u00a0456\u00a0CFA",
		},
		{
			name:   "three decimals",
			amount: 1234567,
			spec: FormatSpec{
				CurrencyCode: "BHD", Symbol: "BHD", Exponent: 3,
				DecimalSeparator: ".", GroupingSeparator: ",", SymbolPosition: SymbolBefore, SpaceBetweenSymbol: true,
			},
			want: "BHD\u00a01,234.567",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Format(tt.amount, tt.spec)
			if err != nil {
				t.Fatalf("Format() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Format() = %q, want %q", got, tt.want)
			}
		})
	}
}
