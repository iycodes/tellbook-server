package appdata

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/tessa"
)

func TestTessaToolMetadataBuildsOnlyAllowlistedBookingNavigation(t *testing.T) {
	booking := TessaBookingSummary{
		BookingID: "10000000-0000-4000-8000-000000000001", ServiceTitle: "Braiding",
		StartsAt: "2026-08-31T10:00:00+01:00", EndsAt: "2026-08-31T11:00:00+01:00",
		Timezone: "Africa/Lagos", Status: "booked", PaymentStatus: "paid_in_full",
		AgreementState: "not_required",
	}
	payload, err := json.Marshal(TessaBookingSearchResult{Items: []TessaBookingSummary{booking}})
	if err != nil {
		t.Fatal(err)
	}
	presentation, references := tessaToolMetadata([]tessa.Evidence{{Tool: "get_schedule", Result: payload}})
	if presentation.Kind != "navigation_actions" || presentation.Version != 1 || len(presentation.Actions) != 2 {
		t.Fatalf("presentation = %+v", presentation)
	}
	if presentation.Actions[0].RouteID != "booking_details" || presentation.Actions[0].EntityID != booking.BookingID ||
		presentation.Actions[1].RouteID != "bookings" {
		t.Fatalf("actions = %+v", presentation.Actions)
	}
	if len(references) != 1 || references[0].ID != booking.BookingID || references[0].RouteID != "booking_details" {
		t.Fatalf("references = %+v", references)
	}
}

func TestTessaToolMetadataBuildsOperationalNavigationAndTypedReferences(t *testing.T) {
	serviceID := "20000000-0000-4000-8000-000000000001"
	customerID := "30000000-0000-4000-8000-000000000001"
	servicePayload, _ := json.Marshal(TessaServiceSearchResult{Items: []TessaServiceSummary{{
		ServiceID: serviceID, Name: "Consultation",
	}}})
	customerPayload, _ := json.Marshal(TessaCustomerSearchResult{Items: []TessaCustomerSummary{{
		CustomerID: customerID, Name: "Ada Test",
	}}})
	presentation, references := tessaToolMetadata([]tessa.Evidence{
		{Tool: "search_services", Result: servicePayload},
		{Tool: "search_customers", Result: customerPayload},
		{Tool: "get_booking_metrics", Result: json.RawMessage(`{}`)},
	})
	if presentation.Kind != "navigation_actions" || len(presentation.Actions) != 4 {
		t.Fatalf("presentation = %+v", presentation)
	}
	if presentation.Actions[0].RouteID != "service_details" ||
		presentation.Actions[0].EntityID != serviceID ||
		presentation.Actions[2].RouteID != "customer_details" ||
		presentation.Actions[2].EntityID != customerID {
		t.Fatalf("actions = %+v", presentation.Actions)
	}
	if len(references) != 2 || references[0].Kind != "service" || references[1].Kind != "customer" {
		t.Fatalf("references = %+v", references)
	}
}

func TestTessaDateRangeUsesProviderTimezoneAndInclusiveEnd(t *testing.T) {
	from, to, err := tessaDateRange("2026-08-30", "2026-08-31", "Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	if from.Format(time.RFC3339) != "2026-08-30T00:00:00+01:00" ||
		to.Format(time.RFC3339) != "2026-09-01T00:00:00+01:00" || tessaCalendarDays(from, to) != 2 {
		t.Fatalf("range = %s to %s", from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
}

func TestMarshalTessaSafeResultTrimsRowsAndMarksTruncation(t *testing.T) {
	items := make([]TessaBookingSummary, 20)
	for index := range items {
		items[index] = TessaBookingSummary{
			BookingID:    "10000000-0000-4000-8000-000000000001",
			ServiceTitle: "A deliberately long service title used to exercise the persisted evidence byte bound",
			CustomerName: "A deliberately long customer display name", StartsAt: "2026-08-31T10:00:00+01:00",
			EndsAt: "2026-08-31T11:00:00+01:00", Timezone: "Africa/Lagos",
			Status: "booked", PaymentStatus: "paid_in_full", AgreementState: "not_required",
		}
	}
	payload, err := marshalTessaSafeResult(TessaBookingSearchResult{Items: items})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 4096 {
		t.Fatalf("payload bytes = %d", len(payload))
	}
	var result TessaBookingSearchResult
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if !result.HasMore || len(result.Items) >= len(items) {
		t.Fatalf("trimmed result = %+v", result)
	}
}

func TestMarshalTessaSafeResultBoundsAvailabilityServiceTitles(t *testing.T) {
	payload, err := marshalTessaSafeResult(TessaAvailabilityResult{
		Services: []TessaServiceAvailability{{
			ServiceID:    "10000000-0000-4000-8000-000000000001",
			ServiceTitle: strings.Repeat("Long service title ", 300),
			Dates:        []TessaAvailabilityDay{},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 4096 {
		t.Fatalf("payload bytes = %d", len(payload))
	}
	var result TessaAvailabilityResult
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(result.Services[0].ServiceTitle)); got > 120 {
		t.Fatalf("service title runes = %d, want <= 120", got)
	}
}

func TestMarshalTessaSafeResultBoundsMultibyteReviewEvidence(t *testing.T) {
	items := make([]TessaReviewItem, 5)
	for index := range items {
		items[index] = TessaReviewItem{
			ReviewID:   "10000000-0000-4000-8000-000000000001",
			AuthorName: strings.Repeat("🙂", 100), ServiceTitle: strings.Repeat("✨", 160),
			ReviewExcerpt: strings.Repeat("👍", 400), Rating: 5,
			CreatedAt: "2026-08-31T10:00:00+01:00",
		}
	}
	payload, err := marshalTessaSafeResult(TessaReviewSummary{Recent: items})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 4096 {
		t.Fatalf("payload bytes = %d", len(payload))
	}
}

func TestTessaToolMetadataBoundsReferencesIndependentlyFromActions(t *testing.T) {
	items := make([]TessaServiceSummary, 30)
	for index := range items {
		items[index] = TessaServiceSummary{
			ServiceID: fmt.Sprintf("10000000-0000-4000-8000-%012d", index+1),
			Name:      strings.Repeat("✨", 160),
		}
	}
	payload, err := json.Marshal(TessaServiceSearchResult{Items: items})
	if err != nil {
		t.Fatal(err)
	}
	presentation, references := tessaToolMetadata([]tessa.Evidence{{Tool: "search_services", Result: payload}})
	if len(presentation.Actions) != 4 || len(references) != tessaEntityReferenceLimit {
		t.Fatalf("actions=%d references=%d", len(presentation.Actions), len(references))
	}
	encoded, err := json.Marshal(references)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 8192 {
		t.Fatalf("reference bytes = %d", len(encoded))
	}
	for _, reference := range references {
		if len(reference.Label) > 120 {
			t.Fatalf("reference label bytes = %d", len(reference.Label))
		}
	}
}
