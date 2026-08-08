package bookingdomain

import (
	"testing"
	"time"
)

func TestGenerateAvailableSlotsFitsPrepAndBufferInsideWindow(t *testing.T) {
	location := time.FixedZone("WAT", 60*60)
	date := time.Date(2026, time.August, 10, 0, 0, 0, 0, location)
	slots, err := GenerateAvailableSlots(AvailabilityRequest{
		Date:              date,
		Now:               date.Add(-24 * time.Hour),
		Location:          location,
		DurationMinutes:   60,
		PrepTimeMinutes:   15,
		BufferTimeMinutes: 15,
		Windows: []AvailabilityWindow{{
			DayOfWeek:           time.Monday,
			StartMinuteOfDay:    9 * 60,
			EndMinuteOfDay:      12 * 60,
			SlotIntervalMinutes: 30,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 4 {
		t.Fatalf("slot count = %d, want 4", len(slots))
	}
	if got := slots[0].Start.Format("15:04"); got != "09:15" {
		t.Fatalf("first appointment = %s, want 09:15", got)
	}
	if got := slots[len(slots)-1].OccupiedEnd.Format("15:04"); got != "12:00" {
		t.Fatalf("last occupied end = %s, want 12:00", got)
	}
}

func TestGenerateAvailableSlotsUsesOccupiedRanges(t *testing.T) {
	location := time.UTC
	date := time.Date(2026, time.August, 10, 0, 0, 0, 0, location)
	slots, err := GenerateAvailableSlots(AvailabilityRequest{
		Date:            date,
		Now:             date.Add(-time.Hour),
		Location:        location,
		DurationMinutes: 60,
		Windows: []AvailabilityWindow{{
			DayOfWeek:           time.Monday,
			StartMinuteOfDay:    9 * 60,
			EndMinuteOfDay:      12 * 60,
			SlotIntervalMinutes: 60,
		}},
		BusyRanges: []OccupiedRange{{
			Start: date.Add(9*time.Hour + 45*time.Minute),
			End:   date.Add(10*time.Hour + 15*time.Minute),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 || slots[0].Start.Hour() != 11 {
		t.Fatalf("slots = %#v, want only 11:00", slots)
	}
}

func TestGenerateAvailableSlotsHonorsMinimumNoticeAndDailyLimit(t *testing.T) {
	date := time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC)
	request := AvailabilityRequest{
		Date:                 date,
		Now:                  date.Add(9 * time.Hour),
		Location:             time.UTC,
		DurationMinutes:      30,
		MinimumNoticeMinutes: 120,
		MaxBookingsPerDay:    2,
		ExistingServiceCount: 1,
		Windows: []AvailabilityWindow{{
			DayOfWeek:           time.Monday,
			StartMinuteOfDay:    9 * 60,
			EndMinuteOfDay:      13 * 60,
			SlotIntervalMinutes: 30,
		}},
	}
	slots, err := GenerateAvailableSlots(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) == 0 || slots[0].Start.Hour() != 11 {
		t.Fatalf("first slot = %#v, want 11:00", slots)
	}

	request.ExistingServiceCount = 2
	slots, err = GenerateAvailableSlots(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("slots = %#v, want none after daily limit", slots)
	}
}
