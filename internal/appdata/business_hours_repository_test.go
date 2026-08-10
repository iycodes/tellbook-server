package appdata

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeBusinessHours(t *testing.T) {
	input := []BusinessHoursWindow{
		{DayOfWeek: 1, StartTime: "09:00", EndTime: "17:00"},
		{DayOfWeek: 6, StartTime: "10:30", EndTime: "14:00"},
	}

	got, err := normalizeBusinessHours(input)
	if err != nil {
		t.Fatalf("normalizeBusinessHours() error = %v", err)
	}
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("normalizeBusinessHours() = %#v, want %#v", got, input)
	}
}

func TestNormalizeBusinessHoursRejectsInvalidSchedules(t *testing.T) {
	tests := []struct {
		name  string
		items []BusinessHoursWindow
		want  error
	}{
		{name: "no open days", want: ErrBusinessHoursRequired},
		{
			name: "duplicate day",
			items: []BusinessHoursWindow{
				{DayOfWeek: 1, StartTime: "09:00", EndTime: "17:00"},
				{DayOfWeek: 1, StartTime: "10:00", EndTime: "16:00"},
			},
			want: ErrDuplicateBusinessDay,
		},
		{
			name:  "end before start",
			items: []BusinessHoursWindow{{DayOfWeek: 2, StartTime: "17:00", EndTime: "09:00"}},
			want:  ErrInvalidBusinessHours,
		},
		{
			name:  "invalid day",
			items: []BusinessHoursWindow{{DayOfWeek: 7, StartTime: "09:00", EndTime: "17:00"}},
			want:  ErrInvalidBusinessHours,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeBusinessHours(test.items)
			if !errors.Is(err, test.want) {
				t.Fatalf("normalizeBusinessHours() error = %v, want %v", err, test.want)
			}
		})
	}
}
