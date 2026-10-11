// Package schedule checks a proposed send time and suggests ones that fit the newsletter's habit.
// Slots are UTC throughout (upstream uses Date.UTC / getUTCDay), so readers keep getting issues at
// their usual hour wherever the sender is.
package schedule

import (
	"sort"
	"time"
)

const (
	slot           = 15 * time.Minute
	minLead        = time.Minute
	maxAhead       = 365 * 24 * time.Hour
	week           = 7 * 24 * time.Hour
	suggestionLead = 10 * time.Minute
	// Another issue going out this close takes the slot, so the suggestion moves a week on.
	takenWindow = 12 * time.Hour
	// Suggestions leave time to look the issue over before it goes out.
	maxSuggestions = 3
)

// Problem is "" when at is at least 1 minute ahead of now and within a year, else upstream's
// message. A zero at is what an unparseable date becomes, and reads as "Pick a date and time".
func Problem(at, now time.Time) string {
	if at.IsZero() {
		return "Pick a date and time"
	}
	if at.Before(now.Add(minLead)) {
		return "Pick a time in the future"
	}
	if at.After(now.Add(maxAhead)) {
		return "Pick a time within a year"
	}
	return ""
}

// Suggestion is one upcoming slot, the way the newsletter has used it before.
type Suggestion struct {
	At         time.Time
	Uses       int
	LastUsedAt time.Time
	// IsLastIssue marks the slot of the most recent past issue, which the sender usually wants.
	IsLastIssue bool
}

// Suggestions turns past send times into upcoming dates: the same weekday and time-of-day, the next
// time it comes around, at least 10 minutes ahead, moved a week on when another issue is scheduled
// close to it or the slot is about to pass. history is newest first; a zero time is skipped.
func Suggestions(history, taken []time.Time, now time.Time) []Suggestion {
	takenTimes := make([]time.Time, 0, len(taken))
	for _, t := range taken {
		if !t.IsZero() {
			takenTimes = append(takenTimes, t)
		}
	}

	type slotUse struct {
		offset     time.Duration
		suggestion Suggestion
	}
	var slots []slotUse
	byOffset := map[time.Duration]int{}
	for _, past := range history {
		if past.IsZero() {
			continue
		}
		offset := roundToSlot(past).Sub(startOfUTCWeek(past))
		if i, seen := byOffset[offset]; seen {
			slots[i].suggestion.Uses++
			continue
		}
		byOffset[offset] = len(slots)
		slots = append(slots, slotUse{offset: offset, suggestion: Suggestion{
			Uses: 1, LastUsedAt: past, IsLastIssue: len(slots) == 0,
		}})
	}
	if len(slots) > maxSuggestions {
		slots = slots[:maxSuggestions]
	}

	suggestions := make([]Suggestion, 0, len(slots))
	for _, used := range slots {
		suggestion := used.suggestion
		suggestion.At = nextFreeTime(used.offset, now, takenTimes)
		suggestions = append(suggestions, suggestion)
	}
	sort.SliceStable(suggestions, func(i, j int) bool { return suggestions[i].At.Before(suggestions[j].At) })
	return suggestions
}

// roundToSlot is upstream's Math.round(time / SLOT_MS) * SLOT_MS: the nearest quarter hour.
func roundToSlot(t time.Time) time.Time {
	const slotMs = int64(slot / time.Millisecond)
	ms := t.UnixMilli()
	return time.UnixMilli(((ms + slotMs/2) / slotMs) * slotMs)
}

// startOfUTCWeek is the Sunday midnight UTC of t's week, as upstream's Date.UTC(...getUTCDay()).
func startOfUTCWeek(t time.Time) time.Time {
	utc := t.UTC()
	midnight := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	return midnight.AddDate(0, 0, -int(utc.Weekday()))
}

func nextFreeTime(offset time.Duration, now time.Time, taken []time.Time) time.Time {
	earliest := now.Add(suggestionLead)
	at := startOfUTCWeek(earliest).Add(offset)
	if at.Before(earliest) {
		at = at.Add(week)
	}
	for takenWithin(at, taken) {
		at = at.Add(week)
	}
	return at
}

func takenWithin(at time.Time, taken []time.Time) bool {
	for _, other := range taken {
		gap := other.Sub(at)
		if gap < 0 {
			gap = -gap
		}
		if gap < takenWindow {
			return true
		}
	}
	return false
}
