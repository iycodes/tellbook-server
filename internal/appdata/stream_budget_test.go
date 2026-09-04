package appdata

import (
	"errors"
	"testing"
)

func TestStreamBudgetEnforcesAndReleasesAllDimensions(t *testing.T) {
	budget := NewStreamBudget(2, 1, 1)
	first, err := budget.Acquire("203.0.113.1", "payment-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Acquire("203.0.113.1", "payment-b"); !errors.Is(err, ErrStreamBudgetExceeded) {
		t.Fatalf("same remote error = %v, want budget exceeded", err)
	}
	if _, err := budget.Acquire("203.0.113.2", "payment-a"); !errors.Is(err, ErrStreamBudgetExceeded) {
		t.Fatalf("same resource error = %v, want budget exceeded", err)
	}
	second, err := budget.Acquire("203.0.113.2", "payment-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Acquire("203.0.113.3", "payment-c"); !errors.Is(err, ErrStreamBudgetExceeded) {
		t.Fatalf("total error = %v, want budget exceeded", err)
	}
	first()
	first()
	third, err := budget.Acquire("203.0.113.1", "payment-a")
	if err != nil {
		t.Fatalf("released budget was not reusable: %v", err)
	}
	third()
	second()
}
