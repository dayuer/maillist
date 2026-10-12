// Package audience picks which contacts a campaign goes to, and in what order.
package audience

import (
	"sort"
	"time"

	"nibwire/internal/core/model"
)

// The upstream sort (AUDIENCE_SORT) walks the same keys as the Mongo query it replaced:
// lastClickAt desc, lastOpenAt desc, imported.clickRate desc, imported.openRate desc, then
// createdAt asc, then id asc for a stable order. A missing engagement field sorts last under a
// descending key, exactly as MongoDB orders nulls.
//
// The upstream "imported.*" sub-document is flattened onto the contact (model.Contact.ClickRate and
// OpenRate), so the two sorts still agree.

// Less is the two-element comparator behind that order.
func Less(a, b model.Contact) bool {
	if c := compareTimeDesc(a.LastClickAt, b.LastClickAt); c != 0 {
		return c < 0
	}
	if c := compareTimeDesc(a.LastOpenAt, b.LastOpenAt); c != 0 {
		return c < 0
	}
	if c := compareFloatDesc(a.ClickRate, b.ClickRate); c != 0 {
		return c < 0
	}
	if c := compareFloatDesc(a.OpenRate, b.OpenRate); c != 0 {
		return c < 0
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// Select keeps active contacts whose email isn't suppressed, most engaged first, up to limit
// (0 or less = everyone). It does not reorder the caller's slice.
func Select(contacts []model.Contact, suppressed map[string]bool, limit int) []model.Contact {
	audience := make([]model.Contact, 0, len(contacts))
	for _, c := range contacts {
		if c.Status == model.ContactActive && !suppressed[c.Email] {
			audience = append(audience, c)
		}
	}
	sort.SliceStable(audience, func(i, j int) bool { return Less(audience[i], audience[j]) })
	if limit > 0 && limit < len(audience) {
		audience = audience[:limit]
	}
	return audience
}

// compareTimeDesc orders the later time first and a missing value last.
func compareTimeDesc(a, b *time.Time) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return 1
		default:
			return -1
		}
	}
	return -a.Compare(*b)
}

// compareFloatDesc orders the larger value first and a missing value last.
func compareFloatDesc(a, b *float64) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return 1
		default:
			return -1
		}
	}
	switch {
	case *a > *b:
		return -1
	case *a < *b:
		return 1
	default:
		return 0
	}
}
