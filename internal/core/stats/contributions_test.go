package stats

import (
	"testing"
	"time"
)

// There is no upstream test for this half: getSubscriberHistory is MongoDB aggregation and the
// original suite covered only the pure series helpers. The cases below are the branches of
// overview-data.js, read off that file: the import window, a join the range starts in the middle of,
// a confirmed-only contact, the three ways a contact leaves, and the suppression that is skipped
// because the contact's own unsubscription already counted.
var rangeStart = time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)

func at(day string) time.Time {
	parsed, err := time.Parse("2006-01-02T15:04:05Z", day)
	if err != nil {
		panic(err)
	}
	return parsed
}

func ptr(value time.Time) *time.Time { return &value }

func contributions(in SubscriberInput) Contribution {
	in.DayKeys = PeriodDayKeys("2026-10-12", 2)
	in.RangeStart = rangeStart
	in.Loc = time.UTC
	return SubscriberContributions(in)
}

func TestAContactThatJoinedBeforeTheRangeIsInTheStartingCount(t *testing.T) {
	got := contributions(SubscriberInput{
		ActiveBefore: 100,
		Joins: []Activity{
			{Source: "cli", SubscribedAt: ptr(at("2026-09-01T10:00:00Z")), CreatedAt: at("2026-09-01T10:00:00Z")},
			{Source: "cli", SubscribedAt: ptr(at("2026-09-20T10:00:00Z")), CreatedAt: at("2026-09-20T10:00:00Z")},
		},
	})
	if got.Baseline != 101 {
		t.Fatalf("baseline = %d, want 101", got.Baseline)
	}
	if len(got.Joins) != 1 || got.Joins["2026-09-20"] != 1 {
		t.Fatalf("joins = %v, want one on 2026-09-20", got.Joins)
	}
}

// 导入时没有日期的那一批：subscribed_at 就是 created_at，属于「不知道什么时候加入的」，进起始计数。
func TestAnImportedRowWithoutItsOwnDateCountsFromTheStart(t *testing.T) {
	created := at("2026-10-01T09:00:00Z")
	got := contributions(SubscriberInput{
		Joins: []Activity{
			{Source: ImportSource, SubscribedAt: ptr(created), CreatedAt: created},
		},
	})
	if got.Baseline != 1 || len(got.Joins) != 0 {
		t.Fatalf("baseline = %d, joins = %v", got.Baseline, got.Joins)
	}

	// The same contact with a date the file actually carried is a real join.
	got = contributions(SubscriberInput{
		Joins: []Activity{
			{Source: ImportSource, SubscribedAt: ptr(created), CreatedAt: created.Add(48 * time.Hour)},
		},
	})
	if got.Baseline != 0 || got.Joins["2026-10-01"] != 1 {
		t.Fatalf("baseline = %d, joins = %v", got.Baseline, got.Joins)
	}
}

// A confirmed opt-in contact may have no subscribe date: the confirmation is when they joined.
func TestAConfirmedContactJoinsOnTheConfirmationDay(t *testing.T) {
	got := contributions(SubscriberInput{
		Joins: []Activity{
			{Source: "web", ConfirmedAt: ptr(at("2026-09-25T08:00:00Z")), CreatedAt: at("2026-09-24T08:00:00Z")},
		},
	})
	if got.Joins["2026-09-25"] != 1 {
		t.Fatalf("joins = %v, want one on 2026-09-25", got.Joins)
	}

	// Neither date at all: nothing to place on the curve.
	got = contributions(SubscriberInput{
		Joins: []Activity{{Source: "web", CreatedAt: at("2026-09-25T08:00:00Z")}},
	})
	if got.Baseline != 1 {
		t.Fatalf("baseline = %d, want 1", got.Baseline)
	}
}

// 离开的人：从加入那天算到离开那天。
func TestALeaverCountsUntilTheyLeft(t *testing.T) {
	got := contributions(SubscriberInput{
		Unsubscribes: []Activity{{
			Source: "cli", SubscribedAt: ptr(at("2026-09-20T10:00:00Z")),
			UnsubscribedAt: ptr(at("2026-10-05T10:00:00Z")), CreatedAt: at("2026-09-20T10:00:00Z"),
		}},
	})
	if got.Joins["2026-09-20"] != 1 {
		t.Fatalf("joins = %v", got.Joins)
	}
	if got.Leaves["2026-10-05"] != 1 {
		t.Fatalf("leaves = %v", got.Leaves)
	}

	// A leaver who joined before the range takes them out of the starting count instead.
	got = contributions(SubscriberInput{
		ActiveBefore: 50,
		Unsubscribes: []Activity{{
			Source: "cli", SubscribedAt: ptr(at("2026-08-01T10:00:00Z")),
			UnsubscribedAt: ptr(at("2026-10-05T10:00:00Z")), CreatedAt: at("2026-08-01T10:00:00Z"),
		}},
	})
	if got.Baseline != 51 || len(got.Joins) != 0 {
		t.Fatalf("baseline = %d, joins = %v", got.Baseline, got.Joins)
	}
}

// 退信/投诉写进 suppression 时：只有 Mailcheap 自己知道离开时间的那条才算。
func TestSuppressionLeavers(t *testing.T) {
	contact := Activity{
		Source: "cli", SubscribedAt: ptr(at("2026-09-20T10:00:00Z")), CreatedAt: at("2026-09-20T10:00:00Z"),
	}

	// The suppression is the only date we have: it counts.
	counted := contact
	counted.LeftAt = ptr(at("2026-10-06T10:00:00Z"))
	got := contributions(SubscriberInput{Suppressed: []Activity{counted}})
	if got.Leaves["2026-10-06"] != 1 || got.Joins["2026-09-20"] != 1 {
		t.Fatalf("joins = %v, leaves = %v", got.Joins, got.Leaves)
	}

	// The contact had already unsubscribed itself, so this bounce is not a second leave.
	alreadyLeft := contact
	alreadyLeft.UnsubscribedAt = ptr(at("2026-10-02T10:00:00Z"))
	alreadyLeft.LeftAt = ptr(at("2026-10-06T10:00:00Z"))
	got = contributions(SubscriberInput{Suppressed: []Activity{alreadyLeft}})
	if len(got.Leaves) != 0 {
		t.Fatalf("leaves = %v, want none", got.Leaves)
	}

	// The suppression was written as the import created the contact: it happened before Mailcheap.
	imported := Activity{
		Source: ImportSource, SubscribedAt: ptr(at("2026-10-01T10:00:00Z")),
		CreatedAt: at("2026-10-01T10:00:00Z"), LeftAt: ptr(at("2026-10-01T10:00:30Z")),
	}
	got = contributions(SubscriberInput{Suppressed: []Activity{imported}})
	if len(got.Leaves) != 0 {
		t.Fatalf("leaves = %v, want none", got.Leaves)
	}
}

func TestHasLeftInMailcheap(t *testing.T) {
	created := at("2026-10-01T10:00:00Z")
	if HasLeftInMailcheap(nil, created) {
		t.Error("no date is not a leave")
	}
	if HasLeftInMailcheap(ptr(created), created) {
		t.Error("a leave at the moment of creation is the import's own row")
	}
	if HasLeftInMailcheap(ptr(created.Add(30*time.Second)), created) {
		t.Error("a leave inside the import window is not ours")
	}
	if !HasLeftInMailcheap(ptr(created.Add(61*time.Second)), created) {
		t.Error("a leave after the import window is ours")
	}
}

func TestContributionFlowsIntoTheSeries(t *testing.T) {
	contribution := contributions(SubscriberInput{
		ActiveBefore: 10,
		Joins: []Activity{
			{Source: "web", SubscribedAt: ptr(at("2026-10-10T10:00:00Z")), CreatedAt: at("2026-10-10T10:00:00Z")},
		},
		Unsubscribes: []Activity{{
			Source: "web", SubscribedAt: ptr(at("2026-09-14T10:00:00Z")),
			UnsubscribedAt: ptr(at("2026-10-11T10:00:00Z")), CreatedAt: at("2026-09-14T10:00:00Z"),
		}},
	})
	series := BuildSubscriberSeries(PeriodDayKeys("2026-10-12", 2), contribution.Baseline,
		contribution.Joins, contribution.Leaves)
	periods := SplitPeriods(series, 2)

	// Ten before the range. The leaver joined before the first day key (2026-09-14 is outside the
	// four days in view, since this curve covers 2026-10-09 to 2026-10-12), so they are part of the
	// starting count and only their leave is a point. One contact joined on the 10th, which lands in
	// the previous period, and the leave on the 11th is in the current one: the count goes 11, 12,
	// 11, 11 and the current period is one down on the one before.
	if periods.Total != 11 {
		t.Fatalf("total = %d, want 11", periods.Total)
	}
	if periods.Growth != -1 {
		t.Fatalf("growth = %d, want -1", periods.Growth)
	}
	if series.Start != 11 {
		t.Fatalf("start = %d, want 11", series.Start)
	}
}
