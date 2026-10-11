package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/importer"
	"mailcheap/internal/core/model"
)

// contactColumns is the SELECT list every contact query shares, in scanContact order.
const contactColumns = `id, newsletter_id, email, status, source, signup_ip, subscribed_at,
    confirmed_at, unsubscribed_at, last_sent_at, last_open_at, last_click_at, last_engaged_at,
    last_chance_at, ignored_issues, sent, opens, clicks, imported_emails_received,
    imported_open_rate, imported_click_rate, created_at, updated_at`

// CreateContact inserts one contact. The unique index on (newsletter_id, email) rejects a duplicate.
func (db *DB) CreateContact(ctx context.Context, c model.Contact) error {
	now := formatTime(time.Now())
	createdAt := c.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO contacts (
            id, newsletter_id, email, status, source, signup_ip, subscribed_at, confirmed_at,
            unsubscribed_at, last_sent_at, last_open_at, last_click_at, last_engaged_at,
            last_chance_at, ignored_issues, sent, opens, clicks, imported_emails_received,
            imported_open_rate, imported_click_rate, created_at, updated_at
        ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.NewsletterID, c.Email, string(c.Status), c.Source, c.SignupIP,
			formatTimePtr(c.SubscribedAt), formatTimePtr(c.ConfirmedAt),
			formatTimePtr(c.UnsubscribedAt), formatTimePtr(c.LastSentAt), formatTimePtr(c.LastOpenAt),
			formatTimePtr(c.LastClickAt), formatTimePtr(c.LastEngagedAt), formatTimePtr(c.LastChanceAt),
			c.IgnoredIssues, c.Sent, c.Opens, c.Clicks, c.EmailsReceived,
			c.OpenRate, c.ClickRate, formatTime(createdAt), now)
		return err
	})
}

func scanContact(row interface{ Scan(...any) error }) (model.Contact, error) {
	var (
		c                                         model.Contact
		status                                    string
		subscribedAt, confirmedAt, unsubscribedAt sql.NullString
		lastSentAt, lastOpenAt, lastClickAt       sql.NullString
		lastEngagedAt, lastChanceAt               sql.NullString
		openRate, clickRate                       sql.NullFloat64
		createdAt, updatedAt                      string
	)
	err := row.Scan(&c.ID, &c.NewsletterID, &c.Email, &status, &c.Source, &c.SignupIP,
		&subscribedAt, &confirmedAt, &unsubscribedAt, &lastSentAt, &lastOpenAt, &lastClickAt,
		&lastEngagedAt, &lastChanceAt, &c.IgnoredIssues, &c.Sent, &c.Opens, &c.Clicks,
		&c.EmailsReceived, &openRate, &clickRate, &createdAt, &updatedAt)
	if err != nil {
		return model.Contact{}, err
	}
	c.Status = model.ContactStatus(status)
	c.OpenRate = floatPtr(openRate)
	c.ClickRate = floatPtr(clickRate)
	for _, field := range []struct {
		source sql.NullString
		target **time.Time
	}{
		{subscribedAt, &c.SubscribedAt}, {confirmedAt, &c.ConfirmedAt},
		{unsubscribedAt, &c.UnsubscribedAt}, {lastSentAt, &c.LastSentAt},
		{lastOpenAt, &c.LastOpenAt}, {lastClickAt, &c.LastClickAt},
		{lastEngagedAt, &c.LastEngagedAt}, {lastChanceAt, &c.LastChanceAt},
	} {
		parsed, err := parseTimePtr(field.source)
		if err != nil {
			return model.Contact{}, err
		}
		*field.target = parsed
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Contact{}, err
	}
	return c, nil
}

// Contact loads one by id.
func (db *DB) Contact(ctx context.Context, id string) (model.Contact, error) {
	c, err := scanContact(db.sql.QueryRowContext(ctx,
		"SELECT "+contactColumns+" FROM contacts WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Contact{}, fmt.Errorf("contact %s: %w", id, ErrNotFound)
	}
	return c, err
}

// ContactByEmail loads one address of one newsletter.
func (db *DB) ContactByEmail(ctx context.Context, newsletterID, email string) (model.Contact, error) {
	c, err := scanContact(db.sql.QueryRowContext(ctx,
		"SELECT "+contactColumns+" FROM contacts WHERE newsletter_id = ? AND email = ?",
		newsletterID, strings.ToLower(strings.TrimSpace(email))))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Contact{}, fmt.Errorf("contact %s: %w", email, ErrNotFound)
	}
	return c, err
}

// UpdateContact writes every mutable column of c back. created_at is never rewritten.
func (db *DB) UpdateContact(ctx context.Context, c model.Contact) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE contacts SET
            email = ?, status = ?, source = ?, signup_ip = ?, subscribed_at = ?, confirmed_at = ?,
            unsubscribed_at = ?, last_sent_at = ?, last_open_at = ?, last_click_at = ?,
            last_engaged_at = ?, last_chance_at = ?, ignored_issues = ?, sent = ?, opens = ?,
            clicks = ?, imported_emails_received = ?, imported_open_rate = ?, imported_click_rate = ?,
            updated_at = ? WHERE id = ?`,
			c.Email, string(c.Status), c.Source, c.SignupIP, formatTimePtr(c.SubscribedAt),
			formatTimePtr(c.ConfirmedAt), formatTimePtr(c.UnsubscribedAt), formatTimePtr(c.LastSentAt),
			formatTimePtr(c.LastOpenAt), formatTimePtr(c.LastClickAt), formatTimePtr(c.LastEngagedAt),
			formatTimePtr(c.LastChanceAt), c.IgnoredIssues, c.Sent, c.Opens, c.Clicks,
			c.EmailsReceived, c.OpenRate, c.ClickRate, formatTime(time.Now()), c.ID)
		if err != nil {
			return err
		}
		return requireOneRow(result, "contact", c.ID)
	})
}

// UpdateContactStatus moves one contact to status, but only from one of the from statuses.
func (db *DB) UpdateContactStatus(ctx context.Context, id string, status model.ContactStatus, from []model.ContactStatus) (bool, error) {
	moved := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		moved = false
		placeholders := make([]string, len(from))
		args := []any{string(status), formatTime(time.Now())}
		for i, allowed := range from {
			placeholders[i] = "?"
			args = append(args, string(allowed))
		}
		args = append(args, id)
		// The only interpolated text is a run of "?" placeholders; every value is bound.
		query := "UPDATE contacts SET status = ?, updated_at = ? WHERE status IN (" + //nolint:gosec // G202: placeholders, not values
			strings.Join(placeholders, ",") + ") AND id = ?"
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		moved = affected > 0
		return nil
	})
	return moved, err
}

// AddContactStats bumps the contact counters every send and every event moves.
func (db *DB) AddContactStats(ctx context.Context, id string, sent, opens, clicks int) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE contacts SET sent = sent + ?, opens = opens + ?, clicks = clicks + ?, updated_at = ? WHERE id = ?",
			sent, opens, clicks, formatTime(time.Now()), id)
		return err
	})
}

// MarkContactEngaged records a sign of life: last_engaged_at, and ignored_issues back to 0 so the
// list cleanup stops counting them toward a last-chance email.
func (db *DB) MarkContactEngaged(ctx context.Context, id string, at time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE contacts SET last_engaged_at = ?, ignored_issues = 0, updated_at = ? WHERE id = ?",
			formatTime(at), formatTime(time.Now()), id)
		return err
	})
}

// ContactFilter is the contacts list query: newsletter, status and an email prefix, paged.
type ContactFilter struct {
	NewsletterID string
	Status       model.ContactStatus
	EmailPrefix  string
	Limit        int // 0 = 100
	Offset       int
}

// Contacts lists contacts newest-subscribed first, as upstream's list query does.
func (db *DB) Contacts(ctx context.Context, filter ContactFilter) ([]model.Contact, error) {
	where, args := filter.where()
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit, filter.Offset)
	return db.queryContacts(ctx, "SELECT "+contactColumns+" FROM contacts"+where+
		" ORDER BY subscribed_at DESC, id DESC LIMIT ? OFFSET ?", args...)
}

// CountContacts is the total behind the same filter.
func (db *DB) CountContacts(ctx context.Context, filter ContactFilter) (int, error) {
	where, args := filter.where()
	var count int
	err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM contacts"+where, args...).Scan(&count)
	return count, err
}

func (f ContactFilter) where() (string, []any) {
	var clauses []string
	var args []any
	if f.NewsletterID != "" {
		clauses = append(clauses, "newsletter_id = ?")
		args = append(args, f.NewsletterID)
	}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.EmailPrefix != "" {
		// COLLATE NOCASE so "Reader" finds "reader@…"; ESCAPE '\' so a literal % or _ stays literal
		// (design §5.3).
		clauses = append(clauses, `email LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, escapeLike(f.EmailPrefix)+"%")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (db *DB) queryContacts(ctx context.Context, query string, args ...any) ([]model.Contact, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// escapeLike makes a user's prefix literal inside a LIKE pattern.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// ApplyImportRows writes what core/importer.Merge decided for one chunk of rows.
//
// A row that names an existing contact always refreshes that contact's imported engagement history
// — upstream's $set: {"imported.*"} — and its status only ever moves stricter. Merge cannot express
// the imported half (its Write has no field for it, see the phase-1 revisions), so the row travels
// alongside the write here.
func (db *DB) ApplyImportRows(ctx context.Context, newsletterID, source string,
	rows []importer.Row, existing map[string]model.Contact) (created, updated int, err error) {
	writes := importer.Merge(rows, existing)
	now := formatTime(time.Now())
	err = db.Write(ctx, func(tx *sql.Tx) error {
		created, updated = 0, 0
		for i, write := range writes {
			row := rows[i]
			// Merge decides the suppression; the caller writes it, as its contract says.
			if write.Suppress != "" {
				if err := upsertSuppression(ctx, tx, row.Email, write.Suppress, source, now); err != nil {
					return err
				}
			}
			if write.Insert != nil {
				subscribedAt := now
				if row.SubscribedAt != nil {
					subscribedAt = formatTime(*row.SubscribedAt)
				}
				var unsubscribedAt any
				if row.Status == model.ContactUnsubscribed {
					unsubscribedAt = now
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO contacts (
                        id, newsletter_id, email, status, source, subscribed_at, unsubscribed_at,
                        imported_emails_received, imported_open_rate, imported_click_rate,
                        created_at, updated_at
                    ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
                    ON CONFLICT(newsletter_id, email) DO NOTHING`,
					ids.New(), newsletterID, row.Email, string(row.Status), source, subscribedAt,
					unsubscribedAt, intOrZero(row.EmailsReceived), derefFloat(row.OpenRate),
					derefFloat(row.ClickRate), now, now)
				if err != nil {
					return err
				}
				created++
				continue
			}
			target := existing[row.Email]
			if target.ID == "" {
				continue
			}
			var unsubscribedAt any
			if write.NewStatus == model.ContactUnsubscribed {
				unsubscribedAt = now
			}
			// coalesce keeps a column the row didn't carry; the status CASE keeps a status the
			// import may not move.
			result, err := tx.ExecContext(ctx, `UPDATE contacts SET
                    imported_emails_received = coalesce(?, imported_emails_received),
                    imported_open_rate = coalesce(?, imported_open_rate),
                    imported_click_rate = coalesce(?, imported_click_rate),
                    status = CASE WHEN ? <> '' THEN ? ELSE status END,
                    unsubscribed_at = CASE WHEN ? THEN ? ELSE unsubscribed_at END,
                    updated_at = ?
                WHERE id = ?`,
				derefInt(row.EmailsReceived), derefFloat(row.OpenRate), derefFloat(row.ClickRate),
				string(write.NewStatus), string(write.NewStatus),
				write.NewStatus == model.ContactUnsubscribed, unsubscribedAt,
				now, target.ID)
			if err != nil {
				return err
			}
			if affected, err := result.RowsAffected(); err != nil {
				return err
			} else if affected > 0 {
				updated++
			}
		}
		return nil
	})
	return created, updated, err
}

func derefInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// intOrZero is derefInt for a NOT NULL column: the count in the model is a plain int, so a row that
// didn't carry one means zero, not "leave whatever is there".
func intOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func derefFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
