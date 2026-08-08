package bookingdomain

import (
	"fmt"
	"math"
)

type DepositType string

const (
	DepositFixed      DepositType = "fixed"
	DepositPercentage DepositType = "percentage"
)

type PricingInput struct {
	BaseServiceAmountMinor     int64
	ServiceDiscountAmountMinor int64
	DepositDiscountAmountMinor int64
	ShortNoticeFeeMinor        int64
	TravelFeeMinor             int64
	DepositRequired            bool
	DepositType                DepositType
	DepositAmountMinor         int64
	DepositPercentageBPS       int
}

type PricingBreakdown struct {
	BaseServiceAmountMinor       int64
	ServiceDiscountAmountMinor   int64
	DiscountedServiceAmountMinor int64
	ShortNoticeFeeMinor          int64
	TravelFeeMinor               int64
	FinalTotalMinor              int64
	DepositDueMinor              int64
	RemainingBalanceMinor        int64
}

func CalculatePricing(input PricingInput) (PricingBreakdown, error) {
	if input.BaseServiceAmountMinor < 0 || input.ServiceDiscountAmountMinor < 0 ||
		input.DepositDiscountAmountMinor < 0 || input.ShortNoticeFeeMinor < 0 ||
		input.TravelFeeMinor < 0 || input.DepositAmountMinor < 0 {
		return PricingBreakdown{}, fmt.Errorf("pricing amounts cannot be negative")
	}
	if input.ServiceDiscountAmountMinor > input.BaseServiceAmountMinor {
		return PricingBreakdown{}, fmt.Errorf("service discount exceeds base service amount")
	}

	discountedService := input.BaseServiceAmountMinor - input.ServiceDiscountAmountMinor
	finalTotal, err := checkedAdd(discountedService, input.ShortNoticeFeeMinor)
	if err != nil {
		return PricingBreakdown{}, err
	}
	finalTotal, err = checkedAdd(finalTotal, input.TravelFeeMinor)
	if err != nil {
		return PricingBreakdown{}, err
	}

	dueNow := finalTotal
	if input.DepositRequired {
		serviceDeposit, err := calculateServiceDeposit(input)
		if err != nil {
			return PricingBreakdown{}, err
		}
		if input.DepositDiscountAmountMinor > serviceDeposit {
			serviceDeposit = 0
		} else {
			serviceDeposit -= input.DepositDiscountAmountMinor
		}
		if serviceDeposit > discountedService {
			serviceDeposit = discountedService
		}

		dueNow, err = checkedAdd(serviceDeposit, input.ShortNoticeFeeMinor)
		if err != nil {
			return PricingBreakdown{}, err
		}
		dueNow, err = checkedAdd(dueNow, input.TravelFeeMinor)
		if err != nil {
			return PricingBreakdown{}, err
		}
		if dueNow > finalTotal {
			dueNow = finalTotal
		}
	}

	return PricingBreakdown{
		BaseServiceAmountMinor:       input.BaseServiceAmountMinor,
		ServiceDiscountAmountMinor:   input.ServiceDiscountAmountMinor,
		DiscountedServiceAmountMinor: discountedService,
		ShortNoticeFeeMinor:          input.ShortNoticeFeeMinor,
		TravelFeeMinor:               input.TravelFeeMinor,
		FinalTotalMinor:              finalTotal,
		DepositDueMinor:              dueNow,
		RemainingBalanceMinor:        finalTotal - dueNow,
	}, nil
}

func ShortNoticeFee(baseAmountMinor int64, rule *ShortNoticeRule) (int64, error) {
	if rule == nil {
		return 0, nil
	}
	if err := rule.Validate(0); err != nil {
		return 0, err
	}
	if rule.Type == SurchargeFixedAmount {
		return rule.AmountMinor, nil
	}
	return applyBasisPoints(baseAmountMinor, rule.PercentageBasisPoints)
}

func CalculateDepositAmount(
	baseAmountMinor int64,
	required bool,
	depositType DepositType,
	fixedAmountMinor int64,
	percentageBPS int,
) (int64, error) {
	if baseAmountMinor < 0 {
		return 0, fmt.Errorf("base amount cannot be negative")
	}
	if !required {
		return baseAmountMinor, nil
	}
	amount, err := calculateServiceDeposit(PricingInput{
		BaseServiceAmountMinor: baseAmountMinor,
		DepositRequired:        true,
		DepositType:            depositType,
		DepositAmountMinor:     fixedAmountMinor,
		DepositPercentageBPS:   percentageBPS,
	})
	if err != nil {
		return 0, err
	}
	if amount > baseAmountMinor {
		return baseAmountMinor, nil
	}
	return amount, nil
}

func calculateServiceDeposit(input PricingInput) (int64, error) {
	switch input.DepositType {
	case DepositFixed:
		return input.DepositAmountMinor, nil
	case DepositPercentage:
		if input.DepositPercentageBPS < 1 || input.DepositPercentageBPS > 10000 {
			return 0, fmt.Errorf("deposit percentage must be between 1 and 10000 basis points")
		}
		return applyBasisPoints(input.BaseServiceAmountMinor, input.DepositPercentageBPS)
	default:
		return 0, fmt.Errorf("invalid deposit type %q", input.DepositType)
	}
}

func applyBasisPoints(amount int64, basisPoints int) (int64, error) {
	if amount < 0 || basisPoints < 0 {
		return 0, fmt.Errorf("amount and basis points cannot be negative")
	}
	if basisPoints == 0 || amount == 0 {
		return 0, nil
	}
	if amount > math.MaxInt64/int64(basisPoints) {
		return 0, fmt.Errorf("money calculation overflow")
	}
	product := amount * int64(basisPoints)
	quotient := product / 10000
	if product%10000 >= 5000 {
		if quotient == math.MaxInt64 {
			return 0, fmt.Errorf("money calculation overflow")
		}
		quotient++
	}
	return quotient, nil
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, fmt.Errorf("money calculation overflow")
	}
	return left + right, nil
}
