// Package importsvc applies normalized import rows to a newsletter in chunks. It is the shared half
// of `mailcheap contact import` and of the MCP `import_contacts` tool: the CLI reads a CSV from a
// path, the tool reads rows out of its arguments, and from there the two do exactly the same thing —
// the same chunk size, the same dry-run arithmetic, the same suppression count.
//
// The decisions themselves are core/importer's (Merge, Dedupe); this package is the part that talks
// to the database and reports what happened.
package importsvc

import (
	"context"

	"mailcheap/internal/core/importer"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/store"
)

// ChunkSize is one transaction's worth of rows. Small enough that a killed import leaves most of its
// work committed, large enough that 100k rows are a hundred transactions rather than a hundred
// thousand (design §5.2).
const ChunkSize = 1000

// Result is what an import did, and what both callers report.
type Result struct {
	Rows       int  `json:"rows"`
	Created    int  `json:"created"`
	Updated    int  `json:"updated"`
	Skipped    int  `json:"skipped"`
	Suppressed int  `json:"suppressed"`
	DryRun     bool `json:"dryRun,omitempty"`
}

// Apply writes the rows in chunks.
//
// The contact map is read once and reused: Dedupe already collapsed repeated addresses, so a later
// chunk can never name a contact an earlier chunk created, and the map can only be wrong about rows
// this import has not reached yet. Re-reading it per chunk would make a 100k-row import quadratic.
func Apply(ctx context.Context, db *store.DB, newsletterID, source string, rows []importer.Row,
	dryRun bool, skipped int, progress func(done, total int)) (Result, error) {
	result := Result{Rows: len(rows), Skipped: skipped, DryRun: dryRun}
	existing, err := Contacts(ctx, db, newsletterID)
	if err != nil {
		return result, err
	}

	for _, chunk := range rules.Chunk(rows, ChunkSize) {
		// Merge is pure and cheap; running it here is what makes the suppression count and the dry
		// run possible without a second contract with the store.
		for _, write := range importer.Merge(chunk, existing) {
			if write.Suppress != "" {
				result.Suppressed++
			}
		}
		if dryRun {
			created, updated := countMerge(chunk, existing)
			result.Created += created
			result.Updated += updated
		} else {
			created, updated, err := db.ApplyImportRows(ctx, newsletterID, source, chunk, existing)
			if err != nil {
				return result, err
			}
			result.Created += created
			result.Updated += updated
		}
		if progress != nil {
			progress(result.Created+result.Updated, result.Rows)
		}
	}
	return result, nil
}

// countMerge is the dry run's arithmetic: the same decisions, none of the writes.
func countMerge(rows []importer.Row, existing map[string]model.Contact) (created, updated int) {
	for _, write := range importer.Merge(rows, existing) {
		switch {
		case write.Insert != nil:
			created++
		case write.UpdateID != "":
			updated++
		}
	}
	return created, updated
}

// Contacts is every contact of the newsletter by address, which is what Merge looks rows up in.
func Contacts(ctx context.Context, db *store.DB, newsletterID string) (map[string]model.Contact, error) {
	total, err := db.CountContacts(ctx, store.ContactFilter{NewsletterID: newsletterID})
	if err != nil {
		return nil, err
	}
	contacts, err := db.Contacts(ctx, store.ContactFilter{NewsletterID: newsletterID, Limit: total})
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.Contact, len(contacts))
	for _, contact := range contacts {
		out[contact.Email] = contact
	}
	return out, nil
}
