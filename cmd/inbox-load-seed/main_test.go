package main

import "testing"

func TestTargetIdentityLimitBoundsConcurrentStreams(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		available int
		want      int
	}{
		{available: 100, want: 100},
		{available: 1_000, want: 1_000},
		{available: 25_000, want: 1_000},
	} {
		if got := targetIdentityLimit(test.available); got != test.want {
			t.Fatalf("targetIdentityLimit(%d) = %d, want %d", test.available, got, test.want)
		}
	}
}
