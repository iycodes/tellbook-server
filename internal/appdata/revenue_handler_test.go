package appdata

import "testing"

func TestParseRevenueRange(t *testing.T) {
	tests := []struct {
		input string
		name  string
		days  int
		ok    bool
	}{
		{input: "", name: "30d", days: 30, ok: true},
		{input: "90d", name: "90d", days: 90, ok: true},
		{input: "180D", name: "180d", days: 180, ok: true},
		{input: "365d", name: "365d", days: 365, ok: true},
		{input: "730d", ok: false},
	}

	for _, test := range tests {
		name, days, ok := parseRevenueRange(test.input)
		if name != test.name || days != test.days || ok != test.ok {
			t.Fatalf("parseRevenueRange(%q) = (%q, %d, %t)", test.input, name, days, ok)
		}
	}
}
