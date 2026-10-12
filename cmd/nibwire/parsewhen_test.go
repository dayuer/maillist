package main

import (
	"testing"
	"time"
)

// --at takes either shape an operator has to hand: an instant they copied, or "in 2h". The
// relative form is truncated to the minute, because schedule.Suggestions works in 15-minute slots
// and a schedule that lands at 14:37:22.918 is noise.
func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 30, 15, 0, time.UTC)

	cases := []struct {
		name string
		in   string
		want time.Time
		fail bool
	}{
		{"an RFC3339 instant", "2026-10-13T14:00:00Z", time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC), false},
		{"an instant with an offset", "2026-10-13T16:00:00+02:00", time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC), false},
		{"in 30m", "in 30m", time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), false},
		{"in 2h", "in 2h", time.Date(2026, 10, 12, 11, 30, 0, 0, time.UTC), false},
		{"in 3d", "in 72h", time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC), false},
		{"a date without a zone", "2026-10-13", time.Time{}, true},
		{"a duration without 'in'", "2h", time.Time{}, true},
		{"zero", "in 0s", time.Time{}, true},
		{"negative", "in -5m", time.Time{}, true},
		{"garbage", "tomorrow", time.Time{}, true},
		{"empty", "", time.Time{}, true},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseWhen(test.in, now)
			if test.fail {
				if err == nil {
					t.Fatalf("parseWhen(%q) = %s, want an error", test.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWhen(%q): %v", test.in, err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("parseWhen(%q) = %s, want %s", test.in, got, test.want)
			}
		})
	}
}
