package agent

import (
	"testing"
	"time"
)

func TestOverTurnBudget(t *testing.T) {
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		budget time.Duration
		later  time.Duration
		want   bool
	}{
		{name: "disabled after two hours", budget: 0, later: 2 * time.Hour, want: false},
		{name: "disabled after 45 minutes", budget: 0, later: 45 * time.Minute, want: false},
		{name: "negative budget ignored", budget: -time.Minute, later: time.Hour, want: false},
		{name: "just under 45 minutes", budget: 45 * time.Minute, later: 45*time.Minute - time.Second, want: false},
		{name: "exactly 45 minutes", budget: 45 * time.Minute, later: 45 * time.Minute, want: true},
		{name: "past 45 minutes", budget: 45 * time.Minute, later: 48 * time.Minute, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := overTurnBudget(start, tc.budget, start.Add(tc.later))
			if got != tc.want {
				t.Fatalf("overTurnBudget(...)=%v, want %v", got, tc.want)
			}
		})
	}
}
