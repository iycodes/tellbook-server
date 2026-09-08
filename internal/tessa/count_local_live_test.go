package tessa_test

import (
	"booking/go-server/internal/tessa"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTessaBookingCountLocalConformance(t *testing.T) {
	if os.Getenv("RUN_TESSA_LOCAL_EVALS") != "true" {
		t.Skip("local evals disabled")
	}
	service, timeout, _ := newLocalTessaService(t)
	recent := []tessa.ContextMessage{{Role: "provider", Content: "Show my bookings this month"}, {Role: "assistant", Content: "Here are the first 8 bookings for September 2026; more results are available."}}
	for _, tc := range []struct {
		question, period, status string
		count                    bool
	}{
		{"How many bookings in total for this month", "this_month", "", true},
		{"How many in total?", "this_month", "", true},
		{"Abeg how many appointments I get this month?", "this_month", "", true},
		{"How many confirmed bookings do I have this week?", "this_week", "confirmed", true},
		{"Show my bookings this month", "this_month", "", false},
	} {
		t.Run(tc.question, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
			defer cancel()
			history := recent
			if !tc.count {
				history = []tessa.ContextMessage{{Role: "provider", Content: "How many bookings this month?"}, {Role: "assistant", Content: "You have 23 bookings for September 2026."}}
			}
			plan, err := service.GeneratePlan(ctx, service.Primary(), tessa.PlanInput{Question: tc.question, RecentMessages: history, CurrentDate: "2026-09-07", QuestionAt: "2026-09-07T18:06:00+01:00", Timezone: "Africa/Lagos"})
			if err != nil || plan.BookingCountOnly != tc.count || len(plan.Tools) != 1 {
				t.Fatal(plan, err)
			}
			tool := plan.Tools[0]
			if tc.count && (tool.Name != "get_booking_metrics" || tool.Metric != "count" || tool.TimeScope != "period" || tool.Period != tc.period || tool.ComparePrevious) {
				t.Fatal(tool)
			}
			if tc.status != "" && (len(tool.Statuses) != 1 || tool.Statuses[0] != tc.status) {
				t.Fatal("status filter lost", tool)
			}
			if !tc.count && tool.Name != "search_bookings" && tool.Name != "get_schedule" {
				t.Fatal("list incorrectly replaced by total", tool)
			}
		})
	}
	for _, total := range []int{23, 0} {
		t.Run(fmt.Sprintf("natural exact total %d", total), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
			defer cancel()
			payload := fmt.Sprintf(`{"from":"2026-09-01","to":"2026-09-30","timezone":"Africa/Lagos","total_bookings":%d,"exact":true}`, total)
			answer, err := service.GenerateAnswer(ctx, service.Primary(), tessa.SynthesisInput{BookingCountOnly: true, Question: "How many bookings in total for this month?", RecentMessages: recent, CurrentDate: "2026-09-07", Timezone: "Africa/Lagos", SourceChannel: "whatsapp", Tools: []tessa.ToolRequest{{Name: "get_booking_metrics", Metric: "count", Period: "this_month", From: "2026-09-01", To: "2026-09-30", TimeScope: "period"}}, Evidence: []tessa.Evidence{{Tool: "get_booking_metrics", Result: json.RawMessage(payload)}}})
			if err != nil {
				t.Fatal(err)
			}
			body := strings.ToLower(answer.Content)
			if answer.PartialNotice != "" || strings.Contains(body, "at least") || strings.Contains(body, "incomplete") || strings.Contains(body, "so far") {
				t.Fatal("aggregate misrepresented", answer)
			}
			if total == 23 && !strings.Contains(body, "23") && !strings.Contains(body, "twenty-three") {
				t.Fatal("exact total missing", body)
			}
			if total == 0 && !strings.Contains(body, "0") && !strings.Contains(body, "zero") && !strings.Contains(body, "no booking") && !strings.Contains(body, "don't have any") && !strings.Contains(body, "do not have any") {
				t.Fatal("zero total missing", body)
			}
			t.Log(answer.Content)
		})
	}
}
