package appdata

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestBookingListWindowUsesBoundedDefaults(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	request := httptest.NewRequest("GET", "/v1/app/bookings", nil)
	input, err := bookingListWindow(request, now)
	if err != nil {
		t.Fatal(err)
	}
	if input.Limit != defaultBookingListLimit {
		t.Fatalf("limit = %d, want %d", input.Limit, defaultBookingListLimit)
	}
	if input.Cursor != "" {
		t.Fatalf("cursor = %q, want empty", input.Cursor)
	}
	if !input.WindowStart.Equal(now.AddDate(0, -3, 0)) || !input.WindowEnd.Equal(now.AddDate(1, 0, 0)) {
		t.Fatalf("unexpected default window: %s to %s", input.WindowStart, input.WindowEnd)
	}
}

func TestBookingListWindowRejectsUnboundedRequest(t *testing.T) {
	request := httptest.NewRequest(
		"GET",
		"/v1/app/bookings?from=2026-01-01T00:00:00Z&to=2029-01-01T00:00:00Z&limit=501",
		nil,
	)
	if _, err := bookingListWindow(request, time.Now()); err == nil {
		t.Fatal("expected an invalid booking window error")
	}
}

func TestBookingListWindowAcceptsBoundedCursorPagination(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/app/bookings?cursor=opaque-cursor&limit=50", nil)
	input, err := bookingListWindow(request, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if input.Cursor != "opaque-cursor" || input.Limit != 50 {
		t.Fatalf("pagination = cursor %q, limit %d", input.Cursor, input.Limit)
	}
}

func TestBookingListWindowRejectsInvalidOffset(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/app/bookings?offset=-1", nil)
	if _, err := bookingListWindow(request, time.Now()); err == nil {
		t.Fatal("expected an invalid booking offset error")
	}
}
