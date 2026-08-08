package bookingdomain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type FulfillmentMode string

const (
	FulfillmentProviderLocation FulfillmentMode = "provider_location"
	FulfillmentCustomerLocation FulfillmentMode = "customer_location"
	FulfillmentVirtual          FulfillmentMode = "virtual"
)

func ParseFulfillmentMode(value string) (FulfillmentMode, error) {
	mode := FulfillmentMode(strings.TrimSpace(value))
	switch mode {
	case FulfillmentProviderLocation, FulfillmentCustomerLocation, FulfillmentVirtual:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid fulfillment mode %q", value)
	}
}

func (m FulfillmentMode) IsPhysical() bool {
	return m == FulfillmentProviderLocation || m == FulfillmentCustomerLocation
}

type AvailabilityMode string

const (
	AvailabilityInheritBusinessHours AvailabilityMode = "inherit_business_hours"
	AvailabilityCustom               AvailabilityMode = "custom"
)

func ParseAvailabilityMode(value string) (AvailabilityMode, error) {
	mode := AvailabilityMode(strings.TrimSpace(value))
	switch mode {
	case AvailabilityInheritBusinessHours, AvailabilityCustom:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid availability mode %q", value)
	}
}

type SurchargeType string

const (
	SurchargeFixedAmount SurchargeType = "fixed_amount"
	SurchargePercentage  SurchargeType = "percentage"
)

type ShortNoticeRule struct {
	ID                    string
	ThresholdMinutes      int
	Type                  SurchargeType
	AmountMinor           int64
	PercentageBasisPoints int
}

var ErrMinimumNoticeNotMet = errors.New("minimum booking notice is not met")

func SelectShortNoticeRule(
	appointmentStart time.Time,
	quotedAt time.Time,
	minimumNoticeMinutes int,
	rules []ShortNoticeRule,
) (*ShortNoticeRule, error) {
	if minimumNoticeMinutes < 0 {
		return nil, fmt.Errorf("minimum notice cannot be negative")
	}

	minutesUntilStart := appointmentStart.Sub(quotedAt) / time.Minute
	if minutesUntilStart < time.Duration(minimumNoticeMinutes) {
		return nil, ErrMinimumNoticeNotMet
	}

	var selected *ShortNoticeRule
	for index := range rules {
		rule := rules[index]
		if err := rule.Validate(minimumNoticeMinutes); err != nil {
			return nil, err
		}
		if minutesUntilStart > time.Duration(rule.ThresholdMinutes) {
			continue
		}
		if selected == nil || rule.ThresholdMinutes < selected.ThresholdMinutes {
			copy := rule
			selected = &copy
		}
	}

	return selected, nil
}

func (r ShortNoticeRule) Validate(minimumNoticeMinutes int) error {
	if r.ThresholdMinutes <= 0 {
		return fmt.Errorf("short-notice threshold must be positive")
	}
	if r.ThresholdMinutes < minimumNoticeMinutes {
		return fmt.Errorf("short-notice threshold must not be inside minimum notice")
	}
	switch r.Type {
	case SurchargeFixedAmount:
		if r.AmountMinor <= 0 || r.PercentageBasisPoints != 0 {
			return fmt.Errorf("fixed short-notice rule has invalid values")
		}
	case SurchargePercentage:
		if r.AmountMinor != 0 || r.PercentageBasisPoints < 1 || r.PercentageBasisPoints > 10000 {
			return fmt.Errorf("percentage short-notice rule has invalid values")
		}
	default:
		return fmt.Errorf("invalid short-notice surcharge type %q", r.Type)
	}
	return nil
}
