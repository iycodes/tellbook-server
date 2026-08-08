package appdata

import (
	"math"
	"testing"
)

func TestFindPromotionDiscountAmount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		candidate promotionCandidate
		subtotal  int64
		want      int64
	}{
		{
			name: "fractional percentage uses basis points",
			candidate: promotionCandidate{
				DiscountType:          "percentage",
				DiscountPercentageBPS: 1250,
			},
			subtotal: 10001,
			want:     1250,
		},
		{
			name: "full percentage does not overflow",
			candidate: promotionCandidate{
				DiscountType:          "percentage",
				DiscountPercentageBPS: 10000,
			},
			subtotal: math.MaxInt64,
			want:     math.MaxInt64,
		},
		{
			name: "fixed amount is capped at subtotal",
			candidate: promotionCandidate{
				DiscountType:       "fixed_amount",
				DiscountValueMinor: 2000,
			},
			subtotal: 1500,
			want:     1500,
		},
		{
			name: "set price returns the reduction",
			candidate: promotionCandidate{
				DiscountType:       "set_price",
				DiscountValueMinor: 3500,
			},
			subtotal: 10000,
			want:     6500,
		},
		{
			name: "set price above subtotal gives no discount",
			candidate: promotionCandidate{
				DiscountType:       "set_price",
				DiscountValueMinor: 10001,
			},
			subtotal: 10000,
			want:     0,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := findPromotionDiscountAmount(test.candidate, test.subtotal); got != test.want {
				t.Fatalf("findPromotionDiscountAmount() = %d, want %d", got, test.want)
			}
		})
	}
}
