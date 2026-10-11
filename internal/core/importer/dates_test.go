package importer

import (
	"testing"
	"time"
)

// 订阅日只认三种写法：ISO 瞬时（带或不带秒的小数）和纯日期。
//
// FI-B from the phase 1-3 audit: upstream parses the cell with `new Date(value)`, which accepts far
// more than this — "March 5, 2026", "03/05/2026", "Mar 5 2026" all parse there and land on a date
// this port drops (the field stays nil, so the contact imports without a subscribe date and counts
// from the chart's start rather than on a day). The narrower set is deliberate: a provider's export
// is a hint, and guessing at an ambiguous "03/05/2026" would put contacts on the wrong day in the
// audience ranking. The consequence is recorded in the plan's execution notes. Pinned here so the
// boundary is a decision rather than a surprise.
func TestWhichSubscribeDatesAreUnderstood(t *testing.T) {
	parsed := map[string]time.Time{
		"2026-03-05T10:00:00Z":     time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
		"2026-03-05T10:00:00.123Z": time.Date(2026, 3, 5, 10, 0, 0, 123_000_000, time.UTC),
		"2026-03-05":               time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
	}
	for value, want := range parsed {
		columns := Columns{Email: 0, Status: -1, SubscribedAt: 1, EmailsReceived: -1, OpenRate: -1, ClickRate: -1}
		row, ok := NormalizeRow([]string{"reader@example.com", value}, columns, "")
		if !ok {
			t.Fatalf("%q was refused as a row", value)
		}
		if row.SubscribedAt == nil || !row.SubscribedAt.Equal(want) {
			t.Errorf("%q → %v, want %v", value, row.SubscribedAt, want)
		}
	}

	// Forms upstream's Date constructor accepts and this port does not: the field comes back nil,
	// and the row is still usable.
	for _, value := range []string{"March 5, 2026", "03/05/2026", "Mar 5 2026", "2026-03", "yesterday"} {
		columns := Columns{Email: 0, Status: -1, SubscribedAt: 1, EmailsReceived: -1, OpenRate: -1, ClickRate: -1}
		row, ok := NormalizeRow([]string{"reader@example.com", value}, columns, "")
		if !ok {
			t.Fatalf("%q made the whole row unusable", value)
		}
		if row.SubscribedAt != nil {
			t.Errorf("%q parsed as %v; this port treats it as no date", value, row.SubscribedAt)
		}
	}
}
