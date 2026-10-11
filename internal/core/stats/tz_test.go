package stats

import (
	"testing"
	"time"
)

// Translated from tests/overview.test.mjs:
//
//	test("days and hours follow the timezone, DST included", …)
//	test("an unknown or missing timezone cookie falls back to UTC", …)
//
// Upstream reads the zone from a cookie and formats with Intl; this port takes it from --tz and
// formats with time.Location. The expectations are upstream's own, including the two Paris days
// either side of the DST change on 2026-10-25.
func TestDaysAndHoursFollowTheTimezoneDSTIncluded(t *testing.T) {
	lateEvening := time.Date(2026, 10, 7, 21, 30, 0, 0, time.UTC)
	nicosia := ResolveTimeZone("Asia/Nicosia")
	paris := ResolveTimeZone("Europe/Paris")

	if got := DateKey(lateEvening, time.UTC); got != "2026-10-07" {
		t.Errorf("DateKey(UTC) = %q", got)
	}
	if got := DateKey(lateEvening, nicosia); got != "2026-10-08" {
		t.Errorf("DateKey(Asia/Nicosia) = %q, want 2026-10-08", got)
	}
	if got := HourIn(lateEvening, nicosia); got != 0 {
		t.Errorf("HourIn(Asia/Nicosia) = %d, want 0", got)
	}
	if got := StartOfDateKey("2026-10-08", nicosia).UTC(); got.Format(time.RFC3339) != "2026-10-07T21:00:00Z" {
		t.Errorf("StartOfDateKey(2026-10-08, Asia/Nicosia) = %s, want 2026-10-07T21:00:00Z", got)
	}
	// Paris leaves summer time on Oct 25, 2026: midnight that day is still CEST, the next one is CET.
	if got := StartOfDateKey("2026-10-25", paris).UTC(); got.Format(time.RFC3339) != "2026-10-24T22:00:00Z" {
		t.Errorf("StartOfDateKey(2026-10-25, Europe/Paris) = %s, want 2026-10-24T22:00:00Z", got)
	}
	if got := StartOfDateKey("2026-10-26", paris).UTC(); got.Format(time.RFC3339) != "2026-10-25T23:00:00Z" {
		t.Errorf("StartOfDateKey(2026-10-26, Europe/Paris) = %s, want 2026-10-25T23:00:00Z", got)
	}
	if got := ShiftDateKey("2026-03-01", -1); got != "2026-02-28" {
		t.Errorf("ShiftDateKey(2026-03-01, -1) = %q", got)
	}
	if got := TimeZoneLabel(nicosia, lateEvening); got != "GMT+3" {
		t.Errorf("TimeZoneLabel(Asia/Nicosia) = %q, want GMT+3", got)
	}
	if got := TimeZoneLabel(time.UTC, lateEvening); got != "UTC" {
		t.Errorf("TimeZoneLabel(UTC) = %q", got)
	}
}

func TestAnUnknownOrMissingTimezoneFallsBackToUTC(t *testing.T) {
	if got := ResolveTimeZone("Asia/Nicosia").String(); got != "Asia/Nicosia" {
		t.Errorf("ResolveTimeZone(Asia/Nicosia) = %q", got)
	}
	for _, name := range []string{"Not/A_Zone", "", "   ", "../../etc/passwd", "UTC\x00x"} {
		if got := ResolveTimeZone(name); got != time.UTC {
			t.Errorf("ResolveTimeZone(%q) = %q, want UTC", name, got)
		}
	}
	// Longer than upstream's 64-character cap for the cookie.
	long := ""
	for range 65 {
		long += "a"
	}
	if got := ResolveTimeZone(long); got != time.UTC {
		t.Errorf("ResolveTimeZone(a 65-character name) = %q, want UTC", got)
	}
}

// The half-hour zones are the ones a naive "+N" label gets wrong.
func TestTimeZoneLabelForHalfHourOffsets(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if got := TimeZoneLabel(ResolveTimeZone("Asia/Kolkata"), at); got != "GMT+5:30" {
		t.Errorf("TimeZoneLabel(Asia/Kolkata) = %q, want GMT+5:30", got)
	}
	if got := TimeZoneLabel(ResolveTimeZone("America/New_York"), at); got != "GMT-4" {
		t.Errorf("TimeZoneLabel(America/New_York) = %q, want GMT-4", got)
	}
}
