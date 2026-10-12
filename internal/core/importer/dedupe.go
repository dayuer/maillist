package importer

import "nibwire/internal/core/model"

// Dedupe collapses rows that name the same address.
//
// A file exported from another provider can list an address twice, and upstream applies such rows
// one after another: the last write of the imported history wins, and the status only ever moves
// stricter (lib/contact-import.js). Merge sees one row at a time and cannot carry that, and two
// inserts of the same address would collide on the (newsletter_id, email) index with the second
// row's history dropped. So the file is collapsed before anything is written.
//
// The result keeps each address once, in the position of its first appearance, with that row's
// imported history replaced by the last row's and the strictest of the statuses.
func Dedupe(rows []Row) []Row {
	position := make(map[string]int, len(rows))
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		at, seen := position[row.Email]
		if !seen {
			position[row.Email] = len(out)
			out = append(out, row)
			continue
		}
		merged := out[at]
		// The last row that carries a value is the one the provider meant.
		if row.SubscribedAt != nil {
			merged.SubscribedAt = row.SubscribedAt
		}
		if row.EmailsReceived != nil {
			merged.EmailsReceived = row.EmailsReceived
		}
		if row.OpenRate != nil {
			merged.OpenRate = row.OpenRate
		}
		if row.ClickRate != nil {
			merged.ClickRate = row.ClickRate
		}
		// The strictest status wins, the same rule Merge applies against the database: an address
		// listed as active after a bounced row must not be resurrected by the import.
		if writesStricter(row.Status, merged.Status) {
			merged.Status = row.Status
		}
		out[at] = merged
	}
	return out
}

// writesStricter reports whether moving from old to next is a move forward. A status with no rank
// (pending, which is waiting to confirm) never moves, exactly as in Merge.
func writesStricter(next, old model.ContactStatus) bool {
	nextRank, ranked := statusRank[next]
	if !ranked {
		return false
	}
	oldRank, ranked := statusRank[old]
	return !ranked || nextRank > oldRank
}
