package stats

import "time"

// ImportSource is the `source` an imported contact carries, and importWindow is how close to its own
// creation an imported row's subscribe date has to be for it to be the import time rather than a
// date the file actually carried (overview-data.js IMPORT_SOURCES / IMPORT_WINDOW_MS).
const (
	ImportSource = "import"
	importWindow = 60 * time.Second
)

// Activity is one contact the subscriber curve is built from. The same shape carries a suppression
// that ended a contact: there, LeftAt is when it happened and the rest describes the contact.
type Activity struct {
	Source         string
	SubscribedAt   *time.Time
	ConfirmedAt    *time.Time
	UnsubscribedAt *time.Time
	CreatedAt      time.Time
	LeftAt         *time.Time
}

// SubscriberInput is what getSubscriberHistory reads, already fetched. The three lists are the three
// queries upstream runs, in its order: the contacts who joined inside the range, the ones who left,
// and the recent bounce and complaint suppressions joined to their contact.
type SubscriberInput struct {
	DayKeys      []string
	RangeStart   time.Time
	Loc          *time.Location
	ActiveBefore int
	Joins        []Activity
	Unsubscribes []Activity
	Suppressed   []Activity
}

// Contribution is the baseline plus the per-day joins and leaves, ready for BuildSubscriberSeries.
type Contribution struct {
	Baseline int
	Joins    map[string]int
	Leaves   map[string]int
}

// SubscriberContributions is the tail of getSubscriberHistory: walk the rows, split each into a join
// day (or the starting count, when it happened before the range or is the import's own timestamp)
// and a leave day.
//
// A contact who left counts until they left, and only when Mailcheap knows when that was. Pending
// and dormant contacts, and the ones imported as unsubscribed or bounced, have no such date and are
// left out of every day — they are in neither list.
func SubscriberContributions(in SubscriberInput) Contribution {
	contribution := Contribution{
		Baseline: in.ActiveBefore,
		Joins:    map[string]int{},
		Leaves:   map[string]int{},
	}
	// A leaver whose own join day is outside the range joins the starting count instead, exactly as
	// an in-range join does not.
	addLeaver := func(joinedKey string, hasJoin bool, leftAt time.Time) {
		if hasJoin {
			contribution.Joins[joinedKey]++
		} else {
			contribution.Baseline++
		}
		contribution.Leaves[DateKey(leftAt, in.Loc)]++
	}

	for _, activity := range in.Joins {
		if key, ok := joinedKey(activity, in.RangeStart, in.Loc); ok {
			contribution.Joins[key]++
		} else {
			contribution.Baseline++
		}
	}
	for _, activity := range in.Unsubscribes {
		if activity.UnsubscribedAt == nil {
			continue
		}
		key, ok := joinedKey(activity, in.RangeStart, in.Loc)
		addLeaver(key, ok, *activity.UnsubscribedAt)
	}
	for _, activity := range in.Suppressed {
		// A suppression the contact had already come back from, or one written at the same moment
		// they were created (an import's own row), is not a leave in this window.
		if HasLeftInMailcheap(activity.UnsubscribedAt, activity.CreatedAt) ||
			!HasLeftInMailcheap(activity.LeftAt, activity.CreatedAt) {
			continue
		}
		key, ok := joinedKey(activity, in.RangeStart, in.Loc)
		addLeaver(key, ok, *activity.LeftAt)
	}
	return contribution
}

// HasLeftInMailcheap is upstream hasLeftInMailcheap: a leave we recorded ourselves, with a date that
// is not the moment the contact was imported. Anything inside the import window happened before
// Mailcheap had the contact, on a day nobody knows.
func HasLeftInMailcheap(leftAt *time.Time, createdAt time.Time) bool {
	return leftAt != nil && leftAt.After(createdAt.Add(importWindow))
}

// joinedKey is the day a contact joined, or ok = false when it was before the range or unknown: they
// belong in the starting count rather than as a spike on some day.
//
// Upstream spells this rule twice — joinedDay inside the aggregation, joinedKey in the JavaScript
// that folds the suppressions in. Their only difference is that the aggregation lets Mongo compare a
// null against a date (null < date, so a contact with no dates is outside the range) and the
// JavaScript substitutes the epoch for it. Both end up outside the range, so one function is enough.
func joinedKey(activity Activity, rangeStart time.Time, loc *time.Location) (string, bool) {
	if isImportTime(activity) {
		return "", false
	}
	joined := laterOf(activity.SubscribedAt, activity.ConfirmedAt)
	if joined == nil || joined.Before(rangeStart) {
		return "", false
	}
	return DateKey(*joined, loc), true
}

// isImportTime is upstream joinedBeforeImport: an imported row whose subscribe date is the moment it
// was created was imported without one, so the file never said when the contact joined.
func isImportTime(activity Activity) bool {
	if activity.Source != ImportSource || activity.SubscribedAt == nil {
		return false
	}
	delta := activity.SubscribedAt.Sub(activity.CreatedAt)
	if delta < 0 {
		delta = -delta
	}
	return delta < importWindow
}

// laterOf is Mongo's $max over two optional dates: the later of the ones that are there.
func laterOf(first, second *time.Time) *time.Time {
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	case second.After(*first):
		return second
	default:
		return first
	}
}
