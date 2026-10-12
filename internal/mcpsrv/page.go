package mcpsrv

import (
	"errors"
	"fmt"
)

// Read tools never return an unbounded list (design §9: "forced page size, never pour ten thousand
// rows into the context"). One implementation, used by every list tool, because a limit that is
// enforced in five places is a limit that is missing in the sixth.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// PageArgs is embedded in every list tool's input.
type PageArgs struct {
	Limit  int `json:"limit,omitempty" jsonschema:"how many rows to return (default 50, at most 100)"`
	Offset int `json:"offset,omitempty" jsonschema:"how many rows to skip (default 0)"`
}

// resolve reads the arguments into a page size.
//
// An over-large limit is refused rather than clamped: an agent that asked for a thousand rows and
// silently got a hundred will believe it has seen everything. Negative values are refused for the
// same reason — they mean the caller has a different idea of this field than this server does.
func (a PageArgs) resolve() (limit, offset int, err error) {
	if a.Limit < 0 {
		return 0, 0, errors.New("limit cannot be negative")
	}
	if a.Offset < 0 {
		return 0, 0, errors.New("offset cannot be negative")
	}
	limit = DefaultPageSize
	if a.Limit > 0 {
		limit = a.Limit
	}
	if limit > MaxPageSize {
		return 0, 0, fmt.Errorf(
			"limit is %d; this surface returns at most %d rows per call — ask again with offset %d for the next page",
			a.Limit, MaxPageSize, a.Offset+MaxPageSize)
	}
	return limit, a.Offset, nil
}

// page is what every list tool answers with.
type page[T any] struct {
	Items      []T  `json:"items"`
	Count      int  `json:"count"`
	HasMore    bool `json:"hasMore"`
	NextOffset int  `json:"nextOffset,omitempty"`
}

// paginate trims a batch that was fetched as limit+1 rows into a page.
//
// has_more comes from that extra row, never from "the page came back full": a full page is also what
// the last page of an exact multiple looks like, and an agent that pages until has_more is false
// would then make one pointless call — or, worse, conclude there is more.
func paginate[T any](rows []T, limit, offset int) page[T] {
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []T{}
	}
	out := page[T]{Items: rows, Count: len(rows), HasMore: hasMore}
	if hasMore {
		out.NextOffset = offset + len(rows)
	}
	return out
}
