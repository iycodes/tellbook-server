package appdata

import (
	"strings"
	"testing"
	"time"
)

func TestBuildPublicBookingCalendar(t *testing.T) {
	booking := PublicBookingSummaryResponse{
		BookingToken:  "booking-token",
		ServiceTitle:  "Lashes, refill; premium",
		StartsAt:      "2026-08-10T09:00:00Z",
		EndsAt:        "2026-08-10T10:30:00Z",
		LocationLabel: "12 Palm Street, Lagos",
		PaymentStatus: "paid_in_full",
	}

	calendar, err := buildPublicBookingCalendar(booking, time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build calendar: %v", err)
	}

	for _, expected := range []string{
		"DTSTAMP:20260806T120000Z\r\n",
		"DTSTART:20260810T090000Z\r\n",
		"DTEND:20260810T103000Z\r\n",
		"SUMMARY:Lashes\\, refill\\; premium\r\n",
		"LOCATION:12 Palm Street\\, Lagos\r\n",
	} {
		if !strings.Contains(calendar, expected) {
			t.Fatalf("calendar does not contain %q:\n%s", expected, calendar)
		}
	}
}

func TestBuildPublicBookingCalendarRejectsInvalidRange(t *testing.T) {
	_, err := buildPublicBookingCalendar(PublicBookingSummaryResponse{
		BookingToken: "booking-token",
		ServiceTitle: "Classic Lash Set",
		StartsAt:     "2026-08-10T09:00:00Z",
		EndsAt:       "2026-08-10T09:00:00Z",
	}, time.Now())
	if err == nil {
		t.Fatal("expected invalid date range error")
	}
}

func TestBuildPublicBookingCalendarIncludesVirtualAccessDetails(t *testing.T) {
	calendar, err := buildPublicBookingCalendar(PublicBookingSummaryResponse{
		BookingToken:        "virtual-booking",
		ServiceTitle:        "Online consultation",
		StartsAt:            "2026-08-10T09:00:00Z",
		EndsAt:              "2026-08-10T10:00:00Z",
		LocationLabel:       "Video call",
		FulfillmentMode:     "virtual",
		VirtualInstructions: "Join five minutes early.",
		VirtualJoinURL:      "https://meet.example.com/session",
	}, time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calendar, `Join five minutes early.\nJoin: https://meet.example.com/session`) {
		t.Fatalf("calendar is missing virtual access details:\n%s", calendar)
	}
}

func TestPublicBookingCalendarAvailability(t *testing.T) {
	for _, status := range []string{"deposit_paid_balance_due", "paid_in_full"} {
		if !publicBookingCalendarAvailable(PublicBookingSummaryResponse{PaymentStatus: status}) {
			t.Fatalf("expected %q to allow calendar export", status)
		}
	}
	if publicBookingCalendarAvailable(PublicBookingSummaryResponse{PaymentStatus: "full_payment_pending"}) {
		t.Fatal("pending payment must not allow calendar export")
	}
}

func TestPublicBookingCalendarDisposition(t *testing.T) {
	got := publicBookingCalendarDisposition(" Classic Lash Set / Deluxe ")
	if got != `attachment; filename=classic-lash-set-deluxe.ics` {
		t.Fatalf("unexpected content disposition: %q", got)
	}
}
