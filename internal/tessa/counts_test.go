package tessa

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func countTestTool() ToolRequest {
	return ToolRequest{Name: "get_booking_metrics", Metric: "count", Period: "this_month", From: "2026-09-01", To: "2026-09-30", TimeScope: "period"}
}

func TestCountOnlyRequiresAggregatePlan(t *testing.T) {
	valid := countTestTool()
	for _, tc := range []struct {
		tool  ToolRequest
		valid bool
	}{
		{valid, true},
		{ToolRequest{Name: "search_bookings", Period: "this_month", From: valid.From, To: valid.To, Selection: "list", TimeScope: "period"}, false},
		{ToolRequest{Name: "get_booking_metrics", Metric: "summary", Period: "this_month", From: valid.From, To: valid.To, TimeScope: "period"}, false},
	} {
		_, err := NormalizePlan(Plan{Scope: "in_scope", Intent: "booking_count", AnswerMode: "tools", BookingCountOnly: true, Tools: []ToolRequest{tc.tool}})
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
	_, err := NormalizePlan(Plan{Scope: "in_scope", Intent: "greeting", AnswerMode: "direct", BookingCountOnly: true, DirectResponse: "There are 8"})
	if err == nil {
		t.Fatal("count without tools accepted")
	}
}

func TestCountEvidenceValidationBeforeGeneration(t *testing.T) {
	for _, tc := range []struct {
		payload string
		valid   bool
	}{
		{`{"from":"2026-09-01","to":"2026-09-30","exact":true,"total_bookings":23}`, true},
		{`{"from":"2026-09-01","to":"2026-09-30","exact":true,"total_bookings":0}`, true},
		{`{"from":"2026-09-01","to":"2026-09-30","exact":false,"total_bookings":8}`, false},
		{`{"from":"2026-09-01","to":"2026-09-30","exact":true}`, false},
		{`{"from":"2026-09-01","to":"2026-09-30","exact":true,"total_bookings":-1}`, false},
		{`{"from":"2026-09-01","to":"2026-09-07","exact":true,"total_bookings":23}`, false},
		{`{"items":[],"has_more":true}`, false},
	} {
		g := &scriptedGenerator{fill: func(_ int, out any) error {
			out.(*Answer).Parts = []AnswerPart{{Text: "Here is your booking total."}}
			return nil
		}}
		s, err := NewService(Provider{Name: "local", Model: "test", Generator: g, Timeout: time.Second}, nil, 12000)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.GenerateAnswer(context.Background(), s.Primary(), SynthesisInput{BookingCountOnly: true, Tools: []ToolRequest{countTestTool()}, Evidence: []Evidence{{Tool: "get_booking_metrics", Result: json.RawMessage(tc.payload)}}})
		if (err == nil) != tc.valid || (!tc.valid && g.calls != 0) {
			t.Fatal(tc, err, g.calls)
		}
	}
	for _, input := range []SynthesisInput{
		{BookingCountOnly: true},
		{BookingCountOnly: true, Tools: []ToolRequest{countTestTool()}},
		{BookingCountOnly: true, Tools: []ToolRequest{{Name: "search_bookings"}}},
	} {
		if validateBookingCountEvidence(input) == nil {
			t.Fatal("missing aggregate accepted")
		}
	}
}

func TestCountPlanUsesExistingRepairAttempt(t *testing.T) {
	g := &scriptedGenerator{fill: func(call int, out any) error {
		tool := ToolRequest{Name: "search_bookings", Period: "this_month", Selection: "list", TimeScope: "period"}
		if call == 2 {
			tool = ToolRequest{Name: "get_booking_metrics", Metric: "count", Period: "this_month", TimeScope: "period"}
		}
		*out.(*Plan) = Plan{Scope: "in_scope", Intent: "booking_count", BookingCountOnly: true, AnswerMode: "tools", Tools: []ToolRequest{tool}}
		return nil
	}}
	s, _ := NewService(Provider{Name: "local", Model: "test", Generator: g, Timeout: time.Second}, nil, 12000)
	plan, err := s.GeneratePlan(context.Background(), s.Primary(), PlanInput{Question: "How many this month?", CurrentDate: "2026-09-07", Timezone: "Africa/Lagos"})
	if err != nil || g.calls != 2 || !plan.BookingCountOnly || plan.Tools[0].Metric != "count" {
		t.Fatal(plan, err, g.calls)
	}
}
