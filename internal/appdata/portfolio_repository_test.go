package appdata

import (
	"testing"

	"github.com/google/uuid"
)

func TestIsExactPortfolioOrder(t *testing.T) {
	first := uuid.New()
	second := uuid.New()
	existing := map[uuid.UUID]struct{}{first: {}, second: {}}

	tests := []struct {
		name  string
		order []uuid.UUID
		want  bool
	}{
		{name: "same items reordered", order: []uuid.UUID{second, first}, want: true},
		{name: "missing item", order: []uuid.UUID{first}, want: false},
		{name: "duplicate item", order: []uuid.UUID{first, first}, want: false},
		{name: "foreign item", order: []uuid.UUID{first, uuid.New()}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isExactPortfolioOrder(existing, test.order); got != test.want {
				t.Fatalf("isExactPortfolioOrder() = %v, want %v", got, test.want)
			}
		})
	}
}
