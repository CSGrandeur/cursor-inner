package agent

import "testing"

func TestEffectiveWindowUsesTheSmallerLimit(t *testing.T) {
	cases := []struct {
		configured, requested, want int
	}{
		{4000, 200000, 4000},
		{1000000, 200000, 200000},
		{0, 200000, 200000},
		{4000, 0, 4000},
		{0, 0, 128000},
	}
	for _, tc := range cases {
		if got := effectiveWindow(tc.configured, tc.requested); got != tc.want {
			t.Fatalf("configured=%d requested=%d got=%d want=%d", tc.configured, tc.requested, got, tc.want)
		}
	}
}
