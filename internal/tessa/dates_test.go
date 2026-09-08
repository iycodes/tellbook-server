package tessa

import (
	"context"
	"testing"
	"time"
)

func TestCalendarPeriodResolution(t *testing.T) {
	for _, tc := range []struct{ period, date, from, to string }{
		{"today", "2026-09-07", "2026-09-07", "2026-09-07"},
		{"yesterday", "2026-01-01", "2025-12-31", "2025-12-31"},
		{"tomorrow", "2026-12-31", "2027-01-01", "2027-01-01"},
		{"this_week", "2026-09-07", "2026-09-07", "2026-09-13"},
		{"this_week", "2026-09-13", "2026-09-07", "2026-09-13"},
		{"last_week", "2026-09-07", "2026-08-31", "2026-09-06"},
		{"next_week", "2026-12-31", "2027-01-04", "2027-01-10"},
		{"this_week", "2026-01-01", "2025-12-29", "2026-01-04"},
		{"this_month", "2024-02-29", "2024-02-01", "2024-02-29"},
		{"last_month", "2025-03-31", "2025-02-01", "2025-02-28"},
		{"next_month", "2026-01-31", "2026-02-01", "2026-02-28"},
		{"next_month", "2026-12-31", "2027-01-01", "2027-01-31"},
	} {
		t.Run(tc.period+"/"+tc.date, func(t *testing.T) {
			plan := Plan{Scope: "in_scope", Intent: "schedule", AnswerMode: "tools", Tools: []ToolRequest{{Name: "get_schedule", Period: tc.period, TimeScope: "period"}}}
			resolved, err := resolvePlanPeriods(plan, tc.date)
			if err != nil || resolved.Tools[0].From != tc.from || resolved.Tools[0].To != tc.to {
				t.Fatal(resolved, err)
			}
			if plan.Tools[0].From != "" {
				t.Fatal("mutated the model request")
			}
			if err := ValidatePlan(resolved); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDateRangeBoundsAreHalfOpenAndDSTSafe(t *testing.T) {
	for _, tc := range []struct {
		from, to, zone string
		hours          float64
	}{
		{"2026-09-07", "2026-09-13", "Africa/Lagos", 168},
		{"2026-03-02", "2026-03-08", "America/New_York", 167},
		{"2026-10-26", "2026-11-01", "America/New_York", 169},
		{"2026-09-10", "2026-09-15", "Africa/Lagos", 144},
	} {
		start, end, err := DateRangeBounds(tc.from, tc.to, tc.zone)
		if err != nil || end.Sub(start).Hours() != tc.hours || start.Hour() != 0 || end.Hour() != 0 {
			t.Fatal(tc, start, end, err)
		}
		last, _ := time.ParseInLocation(time.DateOnly, tc.to, start.Location())
		if !last.Before(end) || !end.Equal(last.AddDate(0, 0, 1)) {
			t.Fatal("inclusive end was not converted once")
		}
	}
	for _, tc := range [][3]string{{"2026-02-30", "2026-03-01", "Africa/Lagos"}, {"2026-09-15", "2026-09-10", "Africa/Lagos"}, {"2026-09-07", "2026-09-13", "Not/AZone"}} {
		if _, _, err := DateRangeBounds(tc[0], tc[1], tc[2]); err == nil {
			t.Fatal("invalid range accepted", tc)
		}
	}
}

func TestDatePeriodRequestRejectsAmbiguousContracts(t *testing.T) {
	for _, tool := range []ToolRequest{
		{Name: "get_schedule", Period: "this_week", From: "2026-09-07", To: "2026-09-14", TimeScope: "period"},
		{Name: "get_schedule", From: "2026-09-07", To: "2026-09-14", TimeScope: "period"},
		{Name: "get_schedule", Period: "rolling_week", TimeScope: "period"},
		{Name: "get_schedule", Period: "custom", TimeScope: "period"},
		{Name: "get_inbox_summary", Period: "this_week"},
	} {
		plan := Plan{Scope: "in_scope", Intent: "lookup", AnswerMode: "tools", Tools: []ToolRequest{tool}}
		resolved, err := resolvePlanPeriods(plan, "2026-09-07")
		if err == nil {
			_, err = NormalizePlan(resolved)
		}
		if err == nil {
			t.Fatal("invalid period contract accepted", tool)
		}
	}
}

func TestGeneratePlanResolvesEveryDatedToolUsingQuestionTimezone(t *testing.T) {
	for _, name := range []string{"search_bookings", "get_schedule", "get_availability", "get_booking_attention_summary", "get_payment_summary", "get_payout_summary", "get_booking_metrics", "get_review_summary"} {
		g := &scriptedGenerator{fill: func(_ int, out any) error {
			tool := ToolRequest{Name: name, Period: "this_week"}
			if name == "get_booking_metrics" {
				tool.Metric, tool.TimeScope = "summary", "period"
			}
			if name == "get_schedule" || name == "search_bookings" {
				tool.TimeScope = "period"
			}
			if name == "search_bookings" {
				tool.Selection = "list"
			}
			*out.(*Plan) = Plan{Scope: "in_scope", Intent: "lookup", AnswerMode: "tools", Tools: []ToolRequest{tool}}
			return nil
		}}
		s, err := NewService(Provider{Name: "local", Model: "test", Generator: g, Timeout: time.Second}, nil, 12000)
		if err != nil {
			t.Fatal(err)
		}
		// Sunday UTC is already Monday locally. The later processing date must
		// not move the requested period into another week.
		plan, err := s.GeneratePlan(context.Background(), s.Primary(), PlanInput{Question: "This week", CurrentDate: "2026-09-21", QuestionAt: "2026-09-06T23:30:00Z", Timezone: "Africa/Lagos"})
		if err != nil || plan.Tools[0].From != "2026-09-07" || plan.Tools[0].To != "2026-09-13" || plan.Tools[0].Period != "this_week" || g.calls != 1 {
			t.Fatal(name, plan, err)
		}
	}
}
