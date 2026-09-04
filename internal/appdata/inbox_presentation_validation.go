package appdata

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

func validateInboxMessagePresentation(presentation *InboxMessagePresentation) error {
	if presentation == nil || presentation.Version != 1 || len(presentation.Data) == 0 {
		return ErrInboxInvalidPresentation
	}
	var valid bool
	switch presentation.Kind {
	case "booking_link":
		valid = validBookingLinkPresentation(presentation.Data)
	case "service_choices":
		valid = validServiceChoicesPresentation(presentation.Data)
	case "availability_choices":
		valid = validAvailabilityChoicesPresentation(presentation.Data)
	case "booking_proposal":
		valid = validBookingProposalPresentation(presentation.Data)
	case "reservation_created":
		valid = validReservationCreatedPresentation(presentation.Data)
	case "booking_next_step":
		valid = validBookingNextStepPresentation(presentation.Data)
	case "reservation_expired":
		valid = validReservationExpiredPresentation(presentation.Data)
	}
	if !valid {
		return ErrInboxInvalidPresentation
	}
	return nil
}

func validBookingLinkPresentation(raw json.RawMessage) bool {
	data, ok := presentationObject(raw,
		"action_id", "provider_id", "provider_handle", "service_id", "service_title", "href", "label",
	)
	if !ok || !presentationUUID(data, "action_id") || !presentationUUID(data, "provider_id") ||
		!presentationString(data, "provider_handle") || !presentationString(data, "label") {
		return false
	}
	href, ok := presentationStringValue(data, "href")
	if !ok || len(href) > 2048 || !safePresentationHref(href) {
		return false
	}
	_, hasServiceID := data["service_id"]
	_, hasServiceTitle := data["service_title"]
	if hasServiceID != hasServiceTitle {
		return false
	}
	return !hasServiceID || (presentationUUID(data, "service_id") && presentationString(data, "service_title"))
}

func validServiceChoicesPresentation(raw json.RawMessage) bool {
	data, ok := presentationObject(raw, "action_id", "session_revision", "choices")
	if !ok || !presentationUUID(data, "action_id") || !presentationPositiveInt(data, "session_revision") {
		return false
	}
	choices, ok := presentationArray(data, "choices", 1, 6)
	if !ok {
		return false
	}
	seenServiceIDs := make(map[string]struct{}, len(choices))
	for _, rawChoice := range choices {
		choice, choiceOK := presentationObject(rawChoice,
			"service_id", "title", "duration_minutes", "price_label", "fulfillment_mode",
		)
		if !choiceOK || !presentationUUID(choice, "service_id") || !presentationString(choice, "title") ||
			!presentationPositiveInt(choice, "duration_minutes") || !presentationString(choice, "price_label") ||
			!presentationEnum(choice, "fulfillment_mode", "provider_location", "customer_location", "virtual") {
			return false
		}
		serviceID, _ := presentationStringValue(choice, "service_id")
		if _, duplicate := seenServiceIDs[serviceID]; duplicate {
			return false
		}
		seenServiceIDs[serviceID] = struct{}{}
	}
	return true
}

func validAvailabilityChoicesPresentation(raw json.RawMessage) bool {
	data, ok := presentationObject(raw,
		"action_id", "session_revision", "service_id", "timezone", "expires_at", "choices",
	)
	if !ok || !presentationUUID(data, "action_id") || !presentationPositiveInt(data, "session_revision") ||
		!presentationUUID(data, "service_id") || !presentationString(data, "timezone") || !presentationTime(data, "expires_at") {
		return false
	}
	choices, ok := presentationArray(data, "choices", 1, 12)
	if !ok {
		return false
	}
	seenSlotIDs := make(map[string]struct{}, len(choices))
	for _, rawChoice := range choices {
		choice, choiceOK := presentationObject(rawChoice, "slot_id", "starts_at", "ends_at", "label")
		if !choiceOK || !presentationString(choice, "slot_id") || !presentationString(choice, "label") ||
			!presentationTimeRange(choice, "starts_at", "ends_at") {
			return false
		}
		slotID, _ := presentationStringValue(choice, "slot_id")
		if _, duplicate := seenSlotIDs[slotID]; duplicate {
			return false
		}
		seenSlotIDs[slotID] = struct{}{}
	}
	return true
}

func validBookingProposalPresentation(raw json.RawMessage) bool {
	data, ok := presentationObject(raw,
		"proposal_id", "proposal_hash", "revision", "expires_at", "service_id", "service_title", "starts_at", "ends_at",
		"timezone", "fulfillment_mode", "location_label", "price_label", "payment_requirement",
		"agreement_requirement", "action_label", "confirmed",
	)
	if !ok || !presentationOptionalBool(data, "confirmed") {
		return false
	}
	return presentationUUID(data, "proposal_id") && presentationSHA256(data, "proposal_hash") && presentationPositiveInt(data, "revision") &&
		presentationTime(data, "expires_at") && presentationUUID(data, "service_id") &&
		presentationString(data, "service_title") && presentationTimeRange(data, "starts_at", "ends_at") &&
		presentationString(data, "timezone") &&
		presentationEnum(data, "fulfillment_mode", "provider_location", "customer_location", "virtual") &&
		presentationString(data, "location_label") && presentationString(data, "price_label") &&
		presentationString(data, "payment_requirement") && presentationString(data, "agreement_requirement") &&
		presentationString(data, "action_label")
}

func presentationOptionalBool(data map[string]json.RawMessage, key string) bool {
	raw, exists := data[key]
	if !exists {
		return true
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil
}

func presentationSHA256(data map[string]json.RawMessage, key string) bool {
	value, ok := presentationStringValue(data, key)
	if !ok || len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validReservationCreatedPresentation(raw json.RawMessage) bool {
	data, ok := presentationObject(raw,
		"booking_id", "status", "status_label", "service_title", "starts_at", "ends_at", "timezone", "expires_at",
	)
	return ok && (data["expires_at"] == nil || presentationTime(data, "expires_at")) &&
		presentationUUID(data, "booking_id") && presentationString(data, "status") &&
		presentationString(data, "status_label") && presentationString(data, "service_title") &&
		presentationTimeRange(data, "starts_at", "ends_at") && presentationString(data, "timezone")
}

func validReservationExpiredPresentation(raw json.RawMessage) bool {
	data, ok := presentationObject(raw, "booking_id", "reason", "label", "expired_at", "href")
	if !ok || !presentationUUID(data, "booking_id") ||
		!presentationEnum(data, "reason", "payment_deadline_elapsed") ||
		!presentationString(data, "label") || !presentationTime(data, "expired_at") {
		return false
	}
	href, hrefOK := presentationStringValue(data, "href")
	return hrefOK && len(href) <= 2048 && safePresentationHref(href)
}

func validBookingNextStepPresentation(raw json.RawMessage) bool {
	data, ok := presentationObject(raw, "booking_id", "next_step", "label", "href", "expires_at")
	if !ok || !presentationUUID(data, "booking_id") ||
		!presentationEnum(data, "next_step", "payment", "agreement", "provider_confirmation", "complete") ||
		!presentationString(data, "label") {
		return false
	}
	if _, present := data["href"]; present {
		href, hrefOK := presentationStringValue(data, "href")
		if !hrefOK || len(href) > 2048 || !safePresentationHref(href) {
			return false
		}
	}
	return data["expires_at"] == nil || presentationTime(data, "expires_at")
}

func presentationObject(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, bool) {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, false
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range value {
		if _, ok := allowedSet[key]; !ok {
			return nil, false
		}
	}
	return value, true
}

func presentationStringValue(data map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := data[key]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func presentationString(data map[string]json.RawMessage, key string) bool {
	_, ok := presentationStringValue(data, key)
	return ok
}

func presentationUUID(data map[string]json.RawMessage, key string) bool {
	value, ok := presentationStringValue(data, key)
	if !ok {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
}

func presentationPositiveInt(data map[string]json.RawMessage, key string) bool {
	raw, ok := data[key]
	if !ok {
		return false
	}
	var value int
	return json.Unmarshal(raw, &value) == nil && value > 0
}

func presentationEnum(data map[string]json.RawMessage, key string, values ...string) bool {
	value, ok := presentationStringValue(data, key)
	if !ok {
		return false
	}
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func presentationTimeValue(data map[string]json.RawMessage, key string) (time.Time, bool) {
	value, ok := presentationStringValue(data, key)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

func presentationTime(data map[string]json.RawMessage, key string) bool {
	_, ok := presentationTimeValue(data, key)
	return ok
}

func presentationTimeRange(data map[string]json.RawMessage, startKey, endKey string) bool {
	start, startOK := presentationTimeValue(data, startKey)
	end, endOK := presentationTimeValue(data, endKey)
	return startOK && endOK && end.After(start)
}

func presentationArray(
	data map[string]json.RawMessage,
	key string,
	minimum, maximum int,
) ([]json.RawMessage, bool) {
	raw, ok := data[key]
	if !ok {
		return nil, false
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) < minimum || len(values) > maximum {
		return nil, false
	}
	return values, true
}

func safePresentationHref(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "" && parsed.Host == "" &&
		strings.HasPrefix(parsed.Path, "/") && !strings.HasPrefix(value, "//")
}
