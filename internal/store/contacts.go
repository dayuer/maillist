package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"nibwire/internal/core/cleanup"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/importer"
	"nibwire/internal/core/model"
	"nibwire/internal/core/sesmap"
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

// UpdateContactStatus moves one contact to status, but only from one of the from statuses. An empty
// from means "from any status" — the operator's edit in `contact update`, where they have already
// seen the current one.
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
		guard := ""
		if len(from) > 0 {
			// The only interpolated text is a run of "?" placeholders; every value is bound.
			guard = "status IN (" + strings.Join(placeholders, ",") + ") AND " //nolint:gosec // G202: placeholders, not values
		}
		query := "UPDATE contacts SET status = ?, updated_at = ? WHERE " + guard + "id = ?"
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

// RecordContactSend stamps a send on the contact: last_sent_at, the sent counter, and — for an
// issue — one more ignored issue for the list cleanup (upstream recordSent). A last-chance email
// also stamps last_chance_at.
func (db *DB) RecordContactSend(ctx context.Context, id string, at time.Time, kind model.MessageKind) error {
	isIssue := kind == model.KindCampaign
	isLastChance := kind == model.KindLastChance
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE contacts SET
            last_sent_at = ?,
            last_chance_at = CASE WHEN ? THEN ? ELSE last_chance_at END,
            sent = sent + 1,
            ignored_issues = ignored_issues + CASE WHEN ? THEN 1 ELSE 0 END,
            updated_at = ?
            WHERE id = ?`,
			formatTime(at), isLastChance, formatTime(at), isIssue, formatTime(time.Now()), id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "contact", id)
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

// RecordEngagement is upstream withEngagement: it advances the engagement timestamps ($max) and
// clears ignored_issues, so the list cleanup stops counting a contact who just proved they read.
//
// The columns are written in SQL rather than read-modify-written, so a click arriving while another
// event is being applied cannot lose either update.
func (db *DB) RecordEngagement(ctx context.Context, id string, engagement *sesmap.Engagement, at time.Time) error {
	var openAt, clickAt any
	if engagement != nil && engagement.LastOpenAt != nil {
		openAt = formatTime(*engagement.LastOpenAt)
	}
	if engagement != nil && engagement.LastClickAt != nil {
		clickAt = formatTime(*engagement.LastClickAt)
	}
	stamp := formatTime(at)
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE contacts SET
            last_open_at = CASE
                WHEN ? IS NULL THEN last_open_at
                WHEN last_open_at IS NULL OR last_open_at < ? THEN ?
                ELSE last_open_at END,
            last_click_at = CASE
                WHEN ? IS NULL THEN last_click_at
                WHEN last_click_at IS NULL OR last_click_at < ? THEN ?
                ELSE last_click_at END,
            last_engaged_at = CASE
                WHEN last_engaged_at IS NULL OR last_engaged_at < ? THEN ?
                ELSE last_engaged_at END,
            ignored_issues = 0,
            updated_at = ?
            WHERE id = ?`,
			openAt, openAt, openAt, clickAt, clickAt, clickAt, stamp, stamp,
			formatTime(time.Now()), id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "contact", id)
	})
}

// SuppressContacts moves every contact with this address that is still in one of the from statuses.
// A complaint reached this way never downgrades a contact who complained already.
func (db *DB) SuppressContacts(ctx context.Context, email string, status model.ContactStatus,
	from []model.ContactStatus) error {
	placeholders := make([]string, len(from))
	args := []any{string(status), formatTime(time.Now()), strings.ToLower(strings.TrimSpace(email))}
	for i, allowed := range from {
		placeholders[i] = "?"
		args = append(args, string(allowed))
	}
	// The only interpolated text is a run of "?" placeholders; every value is bound.
	query := "UPDATE contacts SET status = ?, updated_at = ? WHERE email = ? AND status IN (" + //nolint:gosec // G202: placeholders, not values
		strings.Join(placeholders, ",") + ")"
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// UnsubscribeContact moves one contact of one newsletter out of the subscribed statuses, and reports
// whether it moved. A bounce or complaint stays: those block sending already, and they are not the
// reader's to reverse.
func (db *DB) UnsubscribeContact(ctx context.Context, contactID, newsletterID string,
	from []model.ContactStatus) (bool, error) {
	placeholders := make([]string, len(from))
	now := formatTime(time.Now())
	args := []any{string(model.ContactUnsubscribed), now, now, contactID, newsletterID}
	for i, allowed := range from {
		placeholders[i] = "?"
		args = append(args, string(allowed))
	}
	// The only interpolated text is a run of "?" placeholders; every value is bound.
	query := "UPDATE contacts SET status = ?, unsubscribed_at = ?, updated_at = ? " + //nolint:gosec // G202: placeholders, not values
		"WHERE id = ? AND newsletter_id = ? AND status IN (" + strings.Join(placeholders, ",") + ")"
	moved := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		moved = false
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

// CreditLatestCampaignUnsubscribe counts an unsubscribe against the most recent issue the contact
// got: the link carries no campaign, so this is the best attribution available (upstream
// creditLatestCampaign).
func (db *DB) CreditLatestCampaignUnsubscribe(ctx context.Context, contactID, newsletterID string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		var campaignID string
		err := tx.QueryRowContext(ctx, `SELECT c.id FROM campaigns c
            WHERE c.newsletter_id = ? AND c.started_at IS NOT NULL
              AND EXISTS (SELECT 1 FROM messages m
                          WHERE m.campaign_id = c.id AND m.contact_id = ?
                            AND m.kind = ? AND m.sent_at IS NOT NULL)
            ORDER BY c.started_at DESC LIMIT 1`,
			newsletterID, contactID, string(model.KindCampaign)).Scan(&campaignID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"UPDATE campaigns SET stats_unsubscribed = stats_unsubscribed + 1 WHERE id = ?", campaignID)
		return err
	})
}

// UpsertContact is upstream upsertContact: joining is idempotent, a suppressed address joins with the
// status its suppression implies, and only an explicit resubscribe brings a contact back.
func (db *DB) UpsertContact(ctx context.Context, newsletter model.Newsletter, email, source string,
	resubscribe bool) (model.Contact, bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	joinStatus := model.ContactActive
	if newsletter.RequireConfirmation {
		joinStatus = model.ContactPending
	}
	now := time.Now().UTC()
	var (
		contact model.Contact
		created bool
	)
	err := db.Write(ctx, func(tx *sql.Tx) error {
		reason, suppressed, err := suppressionInTx(ctx, tx, email)
		if err != nil {
			return err
		}
		status := joinStatus
		if suppressed {
			status = suppressedStatus(reason)
		}

		result, err := tx.ExecContext(ctx, `INSERT INTO contacts (
                id, newsletter_id, email, status, source, subscribed_at, created_at, updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(newsletter_id, email) DO NOTHING`,
			ids.New(), newsletter.ID, email, string(status), source,
			formatTime(now), formatTime(now), formatTime(now))
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		created = affected > 0

		if !created && resubscribe && !suppressed {
			// Signing up again is a sign of life, and brings back a contact who left or went dormant.
			if _, err := tx.ExecContext(ctx, `UPDATE contacts SET
                    status = CASE WHEN status IN (?, ?) THEN ? ELSE status END,
                    subscribed_at = CASE WHEN status IN (?, ?) THEN ? ELSE subscribed_at END,
                    unsubscribed_at = CASE WHEN status IN (?, ?) THEN NULL ELSE unsubscribed_at END,
                    last_engaged_at = CASE
                        WHEN last_engaged_at IS NULL OR last_engaged_at < ? THEN ?
                        ELSE last_engaged_at END,
                    ignored_issues = 0,
                    updated_at = ?
                    WHERE newsletter_id = ? AND email = ?`,
				string(model.ContactUnsubscribed), string(model.ContactDormant), string(joinStatus),
				string(model.ContactUnsubscribed), string(model.ContactDormant), formatTime(now),
				string(model.ContactUnsubscribed), string(model.ContactDormant),
				formatTime(now), formatTime(now), formatTime(now), newsletter.ID, email); err != nil {
				return err
			}
		}
		contact, err = scanContact(tx.QueryRowContext(ctx,
			"SELECT "+contactColumns+" FROM contacts WHERE newsletter_id = ? AND email = ?",
			newsletter.ID, email))
		return err
	})
	if err != nil {
		return model.Contact{}, false, err
	}
	return contact, created, nil
}

// ConfirmContact is the click on the confirmation link. A contact that already confirmed stays as
// it is, and confirmed reports whether this click is what did it.
func (db *DB) ConfirmContact(ctx context.Context, contactID, newsletterID string) (model.Contact, bool, error) {
	now := time.Now().UTC()
	confirmed := false
	var contact model.Contact
	err := db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE contacts SET
                status = ?, confirmed_at = ?,
                last_engaged_at = CASE
                    WHEN last_engaged_at IS NULL OR last_engaged_at < ? THEN ?
                    ELSE last_engaged_at END,
                ignored_issues = 0,
                updated_at = ?
            WHERE id = ? AND newsletter_id = ? AND status = ?`,
			string(model.ContactActive), formatTime(now), formatTime(now), formatTime(now),
			formatTime(now), contactID, newsletterID, string(model.ContactPending))
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		confirmed = affected > 0
		contact, err = scanContact(tx.QueryRowContext(ctx,
			"SELECT "+contactColumns+" FROM contacts WHERE id = ? AND newsletter_id = ?",
			contactID, newsletterID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return contact, confirmed, err
}

// KeepContact is the "Yes, keep sending it" click in a last-chance email. It brings back a dormant
// contact; unsubscribes, bounces and complaints stay as they are.
func (db *DB) KeepContact(ctx context.Context, contactID, newsletterID string) (model.Contact, bool, error) {
	var (
		contact model.Contact
		kept    bool
	)
	err := db.Write(ctx, func(tx *sql.Tx) error {
		existing, err := scanContact(tx.QueryRowContext(ctx,
			"SELECT "+contactColumns+" FROM contacts WHERE id = ? AND newsletter_id = ?",
			contactID, newsletterID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if existing.Status != model.ContactActive && existing.Status != model.ContactDormant {
			return nil
		}
		// A dormant contact who is also suppressed stays dormant: the suppression outranks a click.
		if existing.Status == model.ContactDormant {
			_, suppressed, err := suppressionInTx(ctx, tx, existing.Email)
			if err != nil {
				return err
			}
			if suppressed {
				return nil
			}
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE contacts SET status = ?,
                last_engaged_at = CASE
                    WHEN last_engaged_at IS NULL OR last_engaged_at < ? THEN ?
                    ELSE last_engaged_at END,
                ignored_issues = 0, updated_at = ?
                WHERE id = ? AND newsletter_id = ? AND status IN (?, ?)`,
			string(model.ContactActive), formatTime(now), formatTime(now), formatTime(now),
			contactID, newsletterID, string(model.ContactActive), string(model.ContactDormant)); err != nil {
			return err
		}
		contact, err = scanContact(tx.QueryRowContext(ctx,
			"SELECT "+contactColumns+" FROM contacts WHERE id = ? AND newsletter_id = ?",
			contactID, newsletterID))
		kept = err == nil
		return err
	})
	return contact, kept, err
}

// suppressionInTx reads the global suppression list inside a transaction.
func suppressionInTx(ctx context.Context, tx *sql.Tx, email string) (model.SuppressionReason, bool, error) {
	var reason string
	err := tx.QueryRowContext(ctx, "SELECT reason FROM suppressions WHERE email = ?",
		strings.ToLower(strings.TrimSpace(email))).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return model.SuppressionReason(reason), true, nil
}

// suppressedStatus is what a suppressed address joins as (upstream SUPPRESSED_STATUSES).
func suppressedStatus(reason model.SuppressionReason) model.ContactStatus {
	switch reason {
	case model.SuppressBounce:
		return model.ContactBounced
	case model.SuppressComplaint:
		return model.ContactComplained
	default:
		return model.ContactUnsubscribed
	}
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

// MakeDormant puts contacts to sleep: they stop receiving issues, and they are never unsubscribed and
// never deleted (upstream makeDormant).
//
// Only a contact who is still active and still counted as ignoring issues moves, so a sign of life
// that arrived between the list being read and this write leaves them alone. The number that actually
// moved comes back, because that is the number the campaign's report should show.
func (db *DB) MakeDormant(ctx context.Context, contactIDs []string) (int, error) {
	if len(contactIDs) == 0 {
		return 0, nil
	}
	moved := 0
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(contactIDs)), ",")
	args := make([]any, 0, len(contactIDs)+3)
	args = append(args, string(model.ContactDormant), formatTime(time.Now()), string(model.ContactActive))
	for _, id := range contactIDs {
		args = append(args, id)
	}
	// The only interpolated text is a run of "?" placeholders; every value is bound.
	query := "UPDATE contacts SET status = ?, updated_at = ? WHERE status = ? AND ignored_issues > 0 AND id IN (" + //nolint:gosec // G202: placeholders, not values
		placeholders + ")"
	err := db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		moved = int(affected)
		return nil
	})
	return moved, err
}

// CleanupTotals is upstream cleanupTotalsPipeline: the counts the settings screen shows, over the
// contacts of one newsletter who ever got a last-chance email.
//
// waiting, kept and dormant are the three answers that email can have produced so far: no sign of
// life since it (waiting), one (kept), or the list has moved on without them (dormant).
func (db *DB) CleanupTotals(ctx context.Context, newsletterID string) (cleanup.Totals, error) {
	var out cleanup.Totals
	// coalesce on every sum: an empty result set sums to NULL, not to zero.
	err := db.sql.QueryRowContext(ctx, `SELECT
            coalesce(sum(CASE WHEN status = 'active' AND NOT (last_chance_at < max(
                coalesce(last_engaged_at, ''), coalesce(last_open_at, ''), coalesce(last_click_at, ''),
                coalesce(confirmed_at, ''), coalesce(subscribed_at, created_at))) THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN status = 'active' AND last_chance_at < max(
                coalesce(last_engaged_at, ''), coalesce(last_open_at, ''), coalesce(last_click_at, ''),
                coalesce(confirmed_at, ''), coalesce(subscribed_at, created_at)) THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN status = 'dormant' THEN 1 ELSE 0 END), 0)
        FROM contacts WHERE newsletter_id = ? AND last_chance_at IS NOT NULL`, newsletterID).Scan(
		&out.Waiting, &out.Kept, &out.Dormant)
	if err != nil {
		return cleanup.Totals{}, fmt.Errorf("read the cleanup totals: %w", err)
	}
	return out, nil
}
