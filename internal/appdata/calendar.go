package appdata

import (
	"fmt"
	"mime"
	"regexp"
	"strings"
	"time"
)

var calendarFilenameSeparator = regexp.MustCompile(`[^a-z0-9]+`)

func buildPublicBookingCalendar(booking PublicBookingSummaryResponse, generatedAt time.Time) (string, error) {
	startAt, err := time.Parse(time.RFC3339, booking.StartsAt)
	if err != nil {
		return "", fmt.Errorf("parse booking start time: %w", err)
	}
	endAt, err := time.Parse(time.RFC3339, booking.EndsAt)
	if err != nil {
		return "", fmt.Errorf("parse booking end time: %w", err)
	}
	if !endAt.After(startAt) {
		return "", fmt.Errorf("booking end time must be after start time")
	}
	if strings.TrimSpace(booking.BookingToken) == "" || strings.TrimSpace(booking.ServiceTitle) == "" {
		return "", fmt.Errorf("booking token and service title are required")
	}
	description := booking.ServiceTitle + " booking confirmed with TellBook."
	if booking.FulfillmentMode == "virtual" {
		if instructions := strings.TrimSpace(booking.VirtualInstructions); instructions != "" {
			description += "\n" + instructions
		}
		if joinURL := strings.TrimSpace(booking.VirtualJoinURL); joinURL != "" {
			description += "\nJoin: " + joinURL
		}
	}

	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//TellBook//Booking//EN",
		"CALSCALE:GREGORIAN",
		"METHOD:PUBLISH",
		"BEGIN:VEVENT",
		"UID:" + escapeCalendarText(strings.TrimSpace(booking.BookingToken)) + "@tellbook",
		"DTSTAMP:" + formatCalendarTime(generatedAt),
		"DTSTART:" + formatCalendarTime(startAt),
		"DTEND:" + formatCalendarTime(endAt),
		"SUMMARY:" + escapeCalendarText(strings.TrimSpace(booking.ServiceTitle)),
		"LOCATION:" + escapeCalendarText(strings.TrimSpace(booking.LocationLabel)),
		"DESCRIPTION:" + escapeCalendarText(description),
		"END:VEVENT",
		"END:VCALENDAR",
	}

	return strings.Join(lines, "\r\n") + "\r\n", nil
}

func publicBookingCalendarAvailable(booking PublicBookingSummaryResponse) bool {
	switch booking.PaymentStatus {
	case "deposit_paid_balance_due", "paid_in_full":
		return true
	default:
		return false
	}
}

func publicBookingCalendarDisposition(serviceTitle string) string {
	base := strings.Trim(calendarFilenameSeparator.ReplaceAllString(strings.ToLower(strings.TrimSpace(serviceTitle)), "-"), "-")
	if base == "" {
		base = "tellbook-booking"
	}
	if len(base) > 60 {
		base = strings.TrimRight(base[:60], "-")
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": base + ".ics"})
}

func formatCalendarTime(value time.Time) string {
	return value.UTC().Format("20060102T150405Z")
}

func escapeCalendarText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, ";", "\\;")
	return strings.ReplaceAll(value, ",", "\\,")
}
