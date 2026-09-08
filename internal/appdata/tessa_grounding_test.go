package appdata

import (
	"booking/go-server/internal/tessa"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestTessaAvailabilityEvidenceCountsReturnedSlotsAfterTrimming(t *testing.T) {
	for _, slots := range []int{0, 200} {
		value := TessaAvailabilityResult{From: "2026-09-07", To: "2026-09-07", Timezone: "Africa/Lagos", Services: []TessaServiceAvailability{{ServiceID: "10000000-0000-4000-8000-000000000001", ServiceTitle: "Consultation", DurationMinutes: 60, ReturnedSlotCount: 999}}}
		if slots > 0 {
			day := TessaAvailabilityDay{Date: "2026-09-07"}
			for i := 0; i < slots; i++ {
				day.Slots = append(day.Slots, TessaAvailabilitySlot{StartsAt: "2026-09-07T12:00:00+01:00", Label: "12:00 PM"})
			}
			value.Services[0].Dates = []TessaAvailabilityDay{day}
		}
		payload, err := marshalTessaSafeResult(value)
		if err != nil {
			t.Fatal(err)
		}
		var result TessaAvailabilityResult
		if err = json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, day := range result.Services[0].Dates {
			count += len(day.Slots)
		}
		if result.Services[0].ReturnedSlotCount != count || len(payload) > 4096 || result.HasMore != (slots > count) {
			t.Fatal(string(payload))
		}
		if slots == 0 && count != 0 {
			t.Fatal("duration was treated as available slots")
		}
	}
}

func TestTessaAnswerNavigationUsesCoverageNotVisibleRowCount(t *testing.T) {
	const id = "10000000-0000-4000-8000-000000000001"
	payload, _ := json.Marshal(TessaBookingSearchResult{Items: []TessaBookingSummary{{BookingID: id, ServiceTitle: "Consultation"}}, HasMore: true})
	evidence := []tessa.Evidence{{Tool: "search_bookings", Result: payload}}
	execution := tessaToolExecution{Evidence: evidence}
	answer := tessa.Answer{Content: "Your first consultation.", Mentions: []tessa.AnswerMention{{EntityID: id, Quote: "consultation"}}}
	for _, tc := range []struct {
		selection string
		limit     int
		route     string
	}{{"list", 8, "bookings"}, {"first", 1, "booking_details"}, {"top", 2, "bookings"}} {
		p, r, err := tessaAnswerMetadata([]tessa.ToolRequest{{Name: "search_bookings", Selection: tc.selection, Limit: tc.limit}}, execution, answer)
		if err != nil || len(p.Actions) != 1 || p.Actions[0].RouteID != tc.route || len(r) != 1 {
			t.Fatal(tc, p, r, err)
		}
	}
}

func TestTessaUpcomingFilterUsesTheCommittedCheckClock(t *testing.T) {
	checked := time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC)
	if got := tessaUpcomingStart(tessa.ToolRequest{}, checked); got != nil {
		t.Fatal("whole-day lookup filtered")
	}
	if got := tessaUpcomingStart(tessa.ToolRequest{TimeScope: "upcoming"}, checked); got == nil || !got.Equal(checked) {
		t.Fatal(got)
	}
}

func TestTessaExactCountLinksToBookingsNotAnInventedSingleBooking(t *testing.T) {
	for _, total := range []int64{0, 1, 23} {
		payload, _ := json.Marshal(TessaBookingCount{Exact: true, TotalBookings: total, From: "2026-09-01", To: "2026-09-30", Timezone: "Africa/Lagos"})
		presentation, refs, err := tessaAnswerMetadata([]tessa.ToolRequest{{Name: "get_booking_metrics", Metric: "count"}}, tessaToolExecution{Evidence: []tessa.Evidence{{Tool: "get_booking_metrics", Result: payload}}}, tessa.Answer{})
		if err != nil || len(presentation.Actions) != 1 || presentation.Actions[0].RouteID != "bookings" || presentation.Actions[0].Label != "View bookings" || len(refs) != 0 {
			t.Fatal(total, presentation, refs, err)
		}
	}
}

func TestTessaAnswerMetadataFiltersBeforeReferenceAndActionLimits(t *testing.T) {
	var evidence []tessa.Evidence
	for page := 0; page < 2; page++ {
		items := []TessaServiceSummary{}
		for i := 0; i < 8; i++ {
			items = append(items, TessaServiceSummary{ServiceID: fmt.Sprintf("20000000-0000-4000-8000-%012d", page*8+i), Name: "Service"})
		}
		payload, _ := json.Marshal(TessaServiceSearchResult{Items: items})
		evidence = append(evidence, tessa.Evidence{Tool: "search_services", Result: payload})
	}
	const id = "10000000-0000-4000-8000-000000000001"
	payload, _ := json.Marshal(map[string]any{"found": true, "booking": TessaBookingDetail{TessaBookingSummary: TessaBookingSummary{BookingID: id, ServiceTitle: "Consultation"}}})
	evidence = append(evidence, tessa.Evidence{Tool: "get_booking", Result: payload})
	_, refs := tessaToolMetadata(evidence)
	if len(refs) != 12 {
		t.Fatal("fixture must exhaust pre-answer reference cap")
	}
	answer := tessa.Answer{Mentions: []tessa.AnswerMention{{EntityID: id}}}
	got, refs, err := tessaAnswerMetadata(nil, tessaToolExecution{Evidence: evidence}, answer)
	if err != nil || len(refs) != 1 || refs[0].ID != id || len(got.Actions) == 0 || got.Actions[0].RouteID != "booking_details" || got.Actions[0].EntityID != id {
		t.Fatal(got, refs, err)
	}
}

func TestTessaSummaryDoesNotAttachUnmentionedExamples(t *testing.T) {
	const customer = "20000000-0000-4000-8000-000000000001"
	const booking = "10000000-0000-4000-8000-000000000001"
	value := TessaCustomerBookingSummary{Customer: TessaCustomerSummary{CustomerID: customer, Name: "Ada"}, TotalBookings: 10, RecentBookings: []TessaBookingSummary{{BookingID: booking, ServiceTitle: "Consultation"}}}
	payload, _ := json.Marshal(map[string]any{"found": true, "customer": value})
	p, refs, err := tessaAnswerMetadata(nil, tessaToolExecution{Evidence: []tessa.Evidence{{Tool: "get_customer_booking_summary", Result: payload}}}, tessa.Answer{Mentions: []tessa.AnswerMention{{EntityID: customer}}})
	if err != nil || len(refs) != 1 || refs[0].ID != customer || len(p.Actions) != 1 || p.Actions[0].RouteID != "customer_details" {
		t.Fatal(p, refs, err)
	}
	payload, _ = json.Marshal(TessaBookingAttentionSummary{Total: 10, Examples: value.RecentBookings})
	p, _, err = tessaAnswerMetadata(nil, tessaToolExecution{Evidence: []tessa.Evidence{{Tool: "get_booking_attention_summary", Result: payload}}}, tessa.Answer{Mentions: []tessa.AnswerMention{{EntityID: booking}}})
	if err != nil || len(p.Actions) != 1 || p.Actions[0].RouteID != "bookings" {
		t.Fatal("example treated as whole result", p, err)
	}
}
