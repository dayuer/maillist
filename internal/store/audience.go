package store

import (
	"context"

	"mailcheap/internal/core/model"
)

// audienceOrder is core/audience.Less in SQL: last click, then last open, then the imported
// engagement rates, then oldest subscriber first, then id. SQLite sorts NULL first ascending and
// therefore last descending, which is what Less does for a missing engagement field.
const audienceOrder = `last_click_at DESC, last_open_at DESC, imported_click_rate DESC,
    imported_open_rate DESC, created_at ASC, id ASC`

// SelectAudience is the recipients of a campaign: active contacts of the newsletter that are not
// suppressed, most engaged first, up to limit (0 = everyone). The order matches core/audience.Less,
// so the query and the pure comparator can be tested against each other.
func (db *DB) SelectAudience(ctx context.Context, newsletterID string, limit int) ([]model.Contact, error) {
	query := `SELECT ` + contactColumns + ` FROM contacts
        WHERE newsletter_id = ? AND status = ?
          AND email NOT IN (SELECT email FROM suppressions)
        ORDER BY ` + audienceOrder
	args := []any{newsletterID, string(model.ContactActive)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	return db.queryContacts(ctx, query, args...)
}

// CountActiveContacts is how many contacts a newsletter could send to right now. The pre-send
// checklist shows it ("this step goes to N") and the campaign report uses it for "how many are left".
func (db *DB) CountActiveContacts(ctx context.Context, newsletterID string) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		"SELECT count(*) FROM contacts WHERE newsletter_id = ? AND status = ?",
		newsletterID, string(model.ContactActive)).Scan(&count)
	return count, err
}

// SuppressedEmails is every suppressed address, for callers that need the set in Go.
func (db *DB) SuppressedEmails(ctx context.Context) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT email FROM suppressions ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}
