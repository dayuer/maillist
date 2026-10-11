package schedule

import (
	"strings"
	"testing"
	"time"
)

// Tuesday, Oct 6, 2026, 11:30 UTC.
var testNow = mustTime("2026-10-06T11:30:00Z")

// The WEEKLY fixture of tests/schedule.test.mjs.
var weekly = []time.Time{
	mustTime("2026-09-30T13:00:00Z"), // Wed
	mustTime("2026-09-23T13:00:00Z"), // Wed
	mustTime("2026-08-31T13:00:00Z"), // Mon
	mustTime("2026-08-25T13:00:00Z"), // Tue
	mustTime("2026-08-18T13:00:00Z"), // Tue
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func times(suggestions []Suggestion) []string {
	out := make([]string, len(suggestions))
	for i, s := range suggestions {
		out[i] = s.At.Format("2006-01-02T15:04:05.000Z")
	}
	return out
}

func assertTimes(t *testing.T, suggestions []Suggestion, want ...string) {
	t.Helper()
	got := times(suggestions)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("times = %v, want %v", got, want)
	}
}

// tests/schedule.test.mjs "past send times come back as the next time that weekday and hour come
// around".
func TestPastSendTimesComeBackAsTheNextOccurrence(t *testing.T) {
	suggestions := Suggestions(weekly, nil, testNow)
	assertTimes(t, suggestions,
		"2026-10-06T13:00:00.000Z",
		"2026-10-07T13:00:00.000Z",
		"2026-10-12T13:00:00.000Z",
	)
	var lastIssue *Suggestion
	for i := range suggestions {
		if suggestions[i].IsLastIssue {
			lastIssue = &suggestions[i]
		}
	}
	if lastIssue == nil {
		t.Fatal("no slot marked as the last issue")
	}
	if got := lastIssue.At.Format("2006-01-02T15:04:05.000Z"); got != "2026-10-07T13:00:00.000Z" {
		t.Errorf("last issue at = %s", got)
	}
	if lastIssue.Uses != 2 {
		t.Errorf("uses = %d", lastIssue.Uses)
	}
	if got := lastIssue.LastUsedAt.Format("2006-01-02T15:04:05.000Z"); got != "2026-09-30T13:00:00.000Z" {
		t.Errorf("lastUsedAt = %s", got)
	}
}

// tests/schedule.test.mjs "send-now times round to the quarter hour".
func TestSendNowTimesRoundToTheQuarterHour(t *testing.T) {
	suggestions := Suggestions([]time.Time{
		mustTime("2026-09-08T08:40:12Z"),
		mustTime("2026-09-01T13:14:00Z"),
	}, nil, testNow)
	assertTimes(t, suggestions, "2026-10-06T13:15:00.000Z", "2026-10-13T08:45:00.000Z")
}

// tests/schedule.test.mjs "a slot about to pass, or taken by another scheduled issue, moves a week
// on".
func TestASlotAboutToPassOrTakenMovesAWeekOn(t *testing.T) {
	soon := Suggestions([]time.Time{mustTime("2026-09-29T11:35:00Z")}, nil, testNow)
	assertTimes(t, soon, "2026-10-13T11:30:00.000Z")

	taken := Suggestions(
		[]time.Time{mustTime("2026-09-30T13:00:00Z")},
		[]time.Time{mustTime("2026-10-07T13:00:00Z")},
		testNow,
	)
	assertTimes(t, taken, "2026-10-14T13:00:00.000Z")
}

// tests/schedule.test.mjs "no history means no suggestions".
func TestNoHistoryMeansNoSuggestions(t *testing.T) {
	if got := Suggestions(nil, nil, testNow); len(got) != 0 {
		t.Errorf("got %v", times(got))
	}
	// Upstream passes [null, "not a date"]; a time.Time that failed to parse is the zero value.
	failed := []time.Time{{}, {}}
	if got := Suggestions(failed, nil, testNow); len(got) != 0 {
		t.Errorf("got %v", times(got))
	}
}

// tests/schedule.test.mjs "a schedule must be in the future and within a year".
func TestAScheduleMustBeInTheFutureAndWithinAYear(t *testing.T) {
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{mustTime("2026-10-06T13:00:00Z"), ""},
		{testNow.Add(2 * time.Minute), ""},
		{testNow.Add(30 * time.Second), "Pick a time in the future"},
		{mustTime("2026-10-05T13:00:00Z"), "Pick a time in the future"},
		{mustTime("2027-11-01T13:00:00Z"), "Pick a time within a year"},
		{time.Time{}, "Pick a date and time"},
	} {
		if got := Problem(tc.at, testNow); got != tc.want {
			t.Errorf("Problem(%s) = %q, want %q", tc.at, got, tc.want)
		}
	}
}
