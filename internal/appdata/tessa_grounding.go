package appdata

import (
	"booking/go-server/internal/tessa"
	"time"
)

func tessaUpcomingStart(request tessa.ToolRequest, checkedAt time.Time) *time.Time {
	if request.TimeScope != "upcoming" {
		return nil
	}
	return &checkedAt
}

func tessaRequestBookingFilter(request tessa.ToolRequest, checkedAt time.Time) TessaBookingFilter {
	return TessaBookingFilter{Query: request.Query, Statuses: request.Statuses, ExcludedStatuses: request.ExcludedStatuses,
		PaymentState: request.PaymentState, StartsNotBefore: tessaUpcomingStart(request, checkedAt)}
}

// Navigation uses the same evidence and coverage as the validated answer, not
// the number of actions that happened to fit in the UI's four-action limit.
func tessaAnswerMetadata(tools []tessa.ToolRequest, execution tessaToolExecution, answer tessa.Answer) (TessaMessagePresentation, []TessaEntityReference, error) {
	coverage, err := tessa.ResultCoverage(tessa.SynthesisInput{Tools: tools, Evidence: execution.Evidence})
	if err != nil {
		return TessaMessagePresentation{}, nil, err
	}
	mentioned := map[string]bool{}
	for _, mention := range answer.Mentions {
		mentioned[mention.EntityID] = true
	}
	bookingResult, bookingPartial := false, false
	for _, tool := range tools {
		if tool.Name == "get_booking_metrics" && tool.Metric == "count" {
			// An aggregate has no individual booking ID, even when the total is one.
			bookingResult, bookingPartial = true, true
		}
	}
	for _, group := range coverage {
		if group.Tool == "search_bookings" || group.Tool == "get_schedule" || group.Tool == "get_booking" || group.Tool == "get_booking_payment_status" {
			bookingResult = true
			bookingPartial = bookingPartial || group.Partial
			for _, id := range group.EntityIDs {
				bookingPartial = bookingPartial || !mentioned[id]
			}
		}
		if group.Tool == "get_booking_attention_summary" {
			bookingResult, bookingPartial = true, true // Aggregate, not a single example.
		}
	}
	presentation, selectedReferences := tessaSelectedToolMetadata(execution.Evidence, mentioned, tessa.MaxToolCalls*tessa.MaxSearchResults)
	references := make([]TessaEntityReference, 0, tessaEntityReferenceLimit)
	bookings := []TessaEntityReference{}
	for _, reference := range selectedReferences {
		if len(references) < tessaEntityReferenceLimit {
			references = append(references, reference)
		}
		if reference.Kind == "booking" {
			bookings = append(bookings, reference)
		}
	}
	actions := []TessaNavigationAction{}
	if bookingResult || len(bookings) > 0 {
		if len(bookings) == 1 && !bookingPartial {
			actions = append(actions, TessaNavigationAction{RouteID: "booking_details", EntityID: bookings[0].ID, Label: "View booking"})
		} else {
			actions = append(actions, TessaNavigationAction{RouteID: "bookings", Label: "View bookings"})
		}
	}
	for _, action := range presentation.Actions {
		if action.RouteID == "bookings" || action.RouteID == "booking_details" {
			continue
		}
		if len(actions) < 4 {
			actions = append(actions, action)
		}
	}
	if len(actions) == 0 {
		return TessaMessagePresentation{}, references, nil
	}
	return TessaMessagePresentation{Kind: "navigation_actions", Version: 1, Actions: actions}, references, nil
}
