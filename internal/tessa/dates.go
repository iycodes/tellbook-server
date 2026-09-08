package tessa

import (
	"errors"
	"fmt"
	"time"
)

var namedPeriods = []string{"today", "yesterday", "tomorrow", "this_week", "last_week", "next_week", "this_month", "last_month", "next_month"}

func isNamedPeriod(period string) bool {
	for _, name := range namedPeriods {
		if period == name {
			return true
		}
	}
	return false
}

func dateRangeTool(name string) bool {
	switch name {
	case "search_bookings", "get_schedule", "get_availability", "get_booking_attention_summary",
		"get_payment_summary", "get_payout_summary", "get_booking_metrics", "get_review_summary":
		return true
	}
	return false
}

// Calendar dates are resolved before planning is committed. Replays use the
// committed dates, never today's clock or a second model calculation.
func resolvePlanPeriods(plan Plan, referenceDate string) (Plan, error) {
	plan.Tools = append([]ToolRequest(nil), plan.Tools...)
	for i := range plan.Tools {
		tool := &plan.Tools[i]
		if !dateRangeTool(tool.Name) {
			continue
		}
		if tool.Period == "custom" {
			continue
		}
		if !isNamedPeriod(tool.Period) {
			return Plan{}, errors.New("date-range tools require a named period or custom")
		}
		if tool.From != "" || tool.To != "" {
			return Plan{}, errors.New("named periods require empty from and to; the server calculates their dates")
		}
		start, err := time.Parse(time.DateOnly, referenceDate)
		if err != nil {
			return Plan{}, errors.New("invalid question reference date")
		}
		end := start
		switch tool.Period {
		case "yesterday":
			start = start.AddDate(0, 0, -1)
			end = start
		case "tomorrow":
			start = start.AddDate(0, 0, 1)
			end = start
		case "this_week", "last_week", "next_week":
			start = start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
			if tool.Period == "last_week" {
				start = start.AddDate(0, 0, -7)
			}
			if tool.Period == "next_week" {
				start = start.AddDate(0, 0, 7)
			}
			end = start.AddDate(0, 0, 6)
		case "this_month", "last_month", "next_month":
			start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
			if tool.Period == "last_month" {
				start = start.AddDate(0, -1, 0)
			}
			if tool.Period == "next_month" {
				start = start.AddDate(0, 1, 0)
			}
			end = start.AddDate(0, 1, -1)
		}
		tool.From, tool.To = start.Format(time.DateOnly), end.Format(time.DateOnly)
	}
	return plan, nil
}

// DateRangeBounds converts inclusive display dates into a half-open query
// interval in the provider's timezone. Calendar addition is DST-safe.
func DateRangeBounds(from, to, timezone string) (time.Time, time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid date range timezone: %w", err)
	}
	start, err := time.ParseInLocation(time.DateOnly, from, location)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := time.ParseInLocation(time.DateOnly, to, location)
	if err != nil || end.Before(start) {
		return time.Time{}, time.Time{}, errors.New("invalid date range")
	}
	return start, end.AddDate(0, 0, 1), nil
}
