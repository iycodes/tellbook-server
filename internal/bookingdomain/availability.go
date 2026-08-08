package bookingdomain

import (
	"fmt"
	"sort"
	"time"
)

type AvailabilityWindow struct {
	DayOfWeek           time.Weekday
	StartMinuteOfDay    int
	EndMinuteOfDay      int
	SlotIntervalMinutes int
}

type OccupiedRange struct {
	Start time.Time
	End   time.Time
}

type AvailabilityRequest struct {
	Date                 time.Time
	Now                  time.Time
	Location             *time.Location
	DurationMinutes      int
	PrepTimeMinutes      int
	BufferTimeMinutes    int
	MinimumNoticeMinutes int
	MaxBookingsPerDay    int
	ExistingServiceCount int
	Windows              []AvailabilityWindow
	BusyRanges           []OccupiedRange
}

type AvailableSlot struct {
	Start         time.Time
	End           time.Time
	OccupiedStart time.Time
	OccupiedEnd   time.Time
}

func GenerateAvailableSlots(request AvailabilityRequest) ([]AvailableSlot, error) {
	if request.Location == nil {
		return nil, fmt.Errorf("availability location is required")
	}
	if request.DurationMinutes <= 0 || request.PrepTimeMinutes < 0 ||
		request.BufferTimeMinutes < 0 || request.MinimumNoticeMinutes < 0 ||
		request.MaxBookingsPerDay < 0 || request.ExistingServiceCount < 0 {
		return nil, fmt.Errorf("invalid availability configuration")
	}
	if request.MaxBookingsPerDay > 0 && request.ExistingServiceCount >= request.MaxBookingsPerDay {
		return []AvailableSlot{}, nil
	}

	date := request.Date.In(request.Location)
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, request.Location)
	minimumStart := request.Now.In(request.Location).Add(time.Duration(request.MinimumNoticeMinutes) * time.Minute)
	slots := make([]AvailableSlot, 0)

	for _, window := range request.Windows {
		if window.DayOfWeek != dayStart.Weekday() {
			continue
		}
		if window.StartMinuteOfDay < 0 || window.EndMinuteOfDay > 24*60 ||
			window.EndMinuteOfDay <= window.StartMinuteOfDay || window.SlotIntervalMinutes <= 0 {
			return nil, fmt.Errorf("invalid availability window")
		}

		windowStart := dayStart.Add(time.Duration(window.StartMinuteOfDay) * time.Minute)
		windowEnd := dayStart.Add(time.Duration(window.EndMinuteOfDay) * time.Minute)
		firstAppointmentStart := windowStart.Add(time.Duration(request.PrepTimeMinutes) * time.Minute)
		for start := firstAppointmentStart; ; start = start.Add(time.Duration(window.SlotIntervalMinutes) * time.Minute) {
			end := start.Add(time.Duration(request.DurationMinutes) * time.Minute)
			occupiedStart := start.Add(-time.Duration(request.PrepTimeMinutes) * time.Minute)
			occupiedEnd := end.Add(time.Duration(request.BufferTimeMinutes) * time.Minute)
			if occupiedEnd.After(windowEnd) {
				break
			}
			if start.Before(minimumStart) {
				continue
			}
			if overlapsOccupiedRange(occupiedStart, occupiedEnd, request.BusyRanges) {
				continue
			}
			slots = append(slots, AvailableSlot{
				Start:         start,
				End:           end,
				OccupiedStart: occupiedStart,
				OccupiedEnd:   occupiedEnd,
			})
		}
	}

	sort.Slice(slots, func(left, right int) bool {
		return slots[left].Start.Before(slots[right].Start)
	})
	return slots, nil
}

func SlotIsAvailable(request AvailabilityRequest, wantedStart time.Time) (AvailableSlot, bool, error) {
	slots, err := GenerateAvailableSlots(request)
	if err != nil {
		return AvailableSlot{}, false, err
	}
	for _, slot := range slots {
		if slot.Start.Equal(wantedStart) {
			return slot, true, nil
		}
	}
	return AvailableSlot{}, false, nil
}

func overlapsOccupiedRange(start, end time.Time, ranges []OccupiedRange) bool {
	for _, occupied := range ranges {
		if start.Before(occupied.End) && end.After(occupied.Start) {
			return true
		}
	}
	return false
}
