package bookingdomain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestSelectShortNoticeRuleUsesSmallestMatchingThreshold(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	rules := []ShortNoticeRule{
		{ID: "72h", ThresholdMinutes: 72 * 60, Type: SurchargePercentage, PercentageBasisPoints: 1000},
		{ID: "24h", ThresholdMinutes: 24 * 60, Type: SurchargePercentage, PercentageBasisPoints: 2500},
	}

	selected, err := SelectShortNoticeRule(now.Add(5*time.Hour), now, 120, rules)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.ID != "24h" {
		t.Fatalf("selected = %#v, want 24h rule", selected)
	}
}

func TestSelectShortNoticeRuleRejectsMinimumNoticeBoundaryViolation(t *testing.T) {
	now := time.Now()
	_, err := SelectShortNoticeRule(now.Add(119*time.Minute), now, 120, nil)
	if !errors.Is(err, ErrMinimumNoticeNotMet) {
		t.Fatalf("error = %v, want ErrMinimumNoticeNotMet", err)
	}
}

func TestCalculatePricingAppliesFeesAfterDiscount(t *testing.T) {
	result, err := CalculatePricing(PricingInput{
		BaseServiceAmountMinor:     20000,
		ServiceDiscountAmountMinor: 2000,
		DepositDiscountAmountMinor: 500,
		ShortNoticeFeeMinor:        5000,
		TravelFeeMinor:             3000,
		DepositRequired:            true,
		DepositType:                DepositPercentage,
		DepositPercentageBPS:       2500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalTotalMinor != 26000 {
		t.Fatalf("total = %d, want 26000", result.FinalTotalMinor)
	}
	if result.DepositDueMinor != 12500 {
		t.Fatalf("due now = %d, want 12500", result.DepositDueMinor)
	}
	if result.RemainingBalanceMinor != 13500 {
		t.Fatalf("remaining = %d, want 13500", result.RemainingBalanceMinor)
	}
}

func TestCalculatePricingChargesFullTotalWithoutDepositConfiguration(t *testing.T) {
	result, err := CalculatePricing(PricingInput{
		BaseServiceAmountMinor: 10000,
		ShortNoticeFeeMinor:    1000,
		TravelFeeMinor:         2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DepositDueMinor != 13000 || result.RemainingBalanceMinor != 0 {
		t.Fatalf("breakdown = %#v", result)
	}
}

func TestShortNoticeFeeRoundsHalfUp(t *testing.T) {
	fee, err := ShortNoticeFee(101, &ShortNoticeRule{
		ThresholdMinutes:      60,
		Type:                  SurchargePercentage,
		PercentageBasisPoints: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fee != 51 {
		t.Fatalf("fee = %d, want 51", fee)
	}
}

func TestShortNoticeFeeRejectsOverflow(t *testing.T) {
	_, err := ShortNoticeFee(math.MaxInt64, &ShortNoticeRule{
		ThresholdMinutes:      60,
		Type:                  SurchargePercentage,
		PercentageBasisPoints: 10000,
	})
	if err == nil {
		t.Fatal("expected overflow error")
	}
}
