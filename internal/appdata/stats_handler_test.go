package appdata

import "testing"

func TestParseAnalyticsRange(t *testing.T) {
	tests := []struct {
		input string
		name  string
		days  int
		ok    bool
	}{
		{input: "", name: "30d", days: 30, ok: true},
		{input: "7D", name: "7d", days: 7, ok: true},
		{input: "30d", name: "30d", days: 30, ok: true},
		{input: "90d", name: "90d", days: 90, ok: true},
		{input: "365d", ok: false},
	}

	for _, test := range tests {
		name, days, ok := parseAnalyticsRange(test.input)
		if name != test.name || days != test.days || ok != test.ok {
			t.Fatalf("parseAnalyticsRange(%q) = (%q, %d, %t)", test.input, name, days, ok)
		}
	}
}
