package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
)

// CampaignCounters are the issue's own counters (upstream campaign.stats).
type CampaignCounters struct {
	Recipients, Sent, Delivered, Bounced, Complained, Opened, Clicked, Unsubscribed, Failed int
}

// LastChanceCounters are the last-chance emails a campaign sent instead of the issue (upstream
// campaign.lastChance). They are counted apart so open and click rates stay the issue's own.
type LastChanceCounters struct {
	Recipients, Sent, Delivered, Bounced, Complained, Failed, Dormant int
}

// PauseStats is what rules.CampaignPauseReason looks at: last-chance emails go to the least engaged
// addresses, so their bounces and complaints count toward the limits too (upstream
// getCampaignPauseReason(stats, lastChance)).
func (c CampaignCounters) PauseStats(lastChance LastChanceCounters) rules.CampaignStats {
	return rules.CampaignStats{
		Sent:       c.Sent + lastChance.Sent,
		Bounced:    c.Bounced + lastChance.Bounced,
		Complained: c.Complained + lastChance.Complained,
	}
}

// campaignColumns is the SELECT list every campaign query shares, in scanCampaign order.
const campaignColumns = `id, newsletter_id, subject, preview_text, format, body, status,
    audience_limit, source, scheduled_at, started_at, sent_at, test_sent_at, archive_slug,
    thumbnail_url, archive_listed, archive_pinned, web_views, stats_recipients, stats_sent,
    stats_delivered, stats_bounced, stats_complained, stats_opened, stats_clicked,
    stats_unsubscribed, stats_failed, last_chance_recipients, last_chance_sent,
    last_chance_delivered, last_chance_bounced, last_chance_complained, last_chance_failed,
    last_chance_dormant, created_at, updated_at`

// CreateCampaign inserts one campaign. archive_listed is written explicitly: the model's
// model.NewCampaign sets it, and the column default covers rows inserted without it.
func (db *DB) CreateCampaign(ctx context.Context, c model.Campaign) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO campaigns (`+campaignColumns+`) VALUES (
            ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.NewsletterID, c.Subject, c.PreviewText, string(c.Format), c.Body, string(c.Status),
			c.AudienceLimit, c.Source, formatTimePtr(c.ScheduledAt), formatTimePtr(c.StartedAt),
			formatTimePtr(c.SentAt), formatTimePtr(c.TestSentAt), c.ArchiveSlug, c.ThumbnailURL,
			boolInt(c.ArchiveListed), boolInt(c.ArchivePinned), c.WebViews,
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			formatTime(nonZero(c.CreatedAt)), formatTime(time.Now()))
		return err
	})
}

func nonZero(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now()
	}
	return at
}

func scanCampaign(row interface{ Scan(...any) error }) (model.Campaign, error) {
	var (
		c                              model.Campaign
		format, status                 string
		scheduledAt, startedAt, sentAt sql.NullString
		testSentAt                     sql.NullString
		archiveListed, archivePinned   int
		createdAt, updatedAt           string
	)
	err := row.Scan(&c.ID, &c.NewsletterID, &c.Subject, &c.PreviewText, &format, &c.Body, &status,
		&c.AudienceLimit, &c.Source, &scheduledAt, &startedAt, &sentAt, &testSentAt, &c.ArchiveSlug,
		&c.ThumbnailURL, &archiveListed, &archivePinned, &c.WebViews,
		new(int), new(int), new(int), new(int), new(int), new(int), new(int), new(int), new(int),
		new(int), new(int), new(int), new(int), new(int), new(int), new(int),
		&createdAt, &updatedAt)
	if err != nil {
		return model.Campaign{}, err
	}
	c.Format = model.Format(format)
	c.Status = model.CampaignStatus(status)
	c.ArchiveListed = archiveListed != 0
	c.ArchivePinned = archivePinned != 0
	for _, field := range []struct {
		source sql.NullString
		target **time.Time
	}{
		{scheduledAt, &c.ScheduledAt}, {startedAt, &c.StartedAt},
		{sentAt, &c.SentAt}, {testSentAt, &c.TestSentAt},
	} {
		parsed, err := parseTimePtr(field.source)
		if err != nil {
			return model.Campaign{}, err
		}
		*field.target = parsed
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Campaign{}, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return model.Campaign{}, err
	}
	return c, nil
}

// Campaign loads one by id.
func (db *DB) Campaign(ctx context.Context, id string) (model.Campaign, error) {
	c, err := scanCampaign(db.sql.QueryRowContext(ctx,
		"SELECT "+campaignColumns+" FROM campaigns WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Campaign{}, fmt.Errorf("campaign %s: %w", id, ErrNotFound)
	}
	return c, err
}

// Campaigns lists a newsletter's campaigns, most recently active first. An empty newsletterID lists
// every newsletter's. offset skips that many rows, for a caller that pages through them.
func (db *DB) Campaigns(ctx context.Context, newsletterID string, limit, offset int) ([]model.Campaign, error) {
	if limit <= 0 {
		limit = 50
	}
	query := "SELECT " + campaignColumns + " FROM campaigns"
	args := []any{}
	if newsletterID != "" {
		query += " WHERE newsletter_id = ?"
		args = append(args, newsletterID)
	}
	query += ` ORDER BY coalesce(sent_at, started_at, scheduled_at, updated_at) DESC, id DESC LIMIT ?`
	args = append(args, limit)
	if offset > 0 {
		query += " OFFSET ?"
		args = append(args, offset)
	}

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCampaign writes the columns an editor owns — the ones an operator changes by hand or through
// an agent — and created_at is never rewritten.
//
// What is left out is the point of this function. status, the four sending timestamps and web_views
// belong to something else (SetCampaignStatus, SetCampaignSchedule, SetCampaignStartedAt/SentAt,
// SetCampaignTestSentAt, AddWebViews), and editing a campaign is a read-modify-write: the operator
// loads it, changes a field, and writes it back. Writing the sender's own columns from a copy read
// before the sender finished would undo work that had already happened — a campaign the sender had
// just marked sent would go back to sending, and an issue's view count would be reset to whatever it
// was when the editor opened the page. Each of those columns has a setter that only moves it as part
// of a decision, which is where the guard against a wrong transition lives.
func (db *DB) UpdateCampaign(ctx context.Context, c model.Campaign) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE campaigns SET
            newsletter_id = ?, subject = ?, preview_text = ?, format = ?, body = ?,
            audience_limit = ?, source = ?, archive_slug = ?, thumbnail_url = ?,
            archive_listed = ?, archive_pinned = ?, updated_at = ? WHERE id = ?`,
			c.NewsletterID, c.Subject, c.PreviewText, string(c.Format), c.Body,
			c.AudienceLimit, c.Source, c.ArchiveSlug, c.ThumbnailURL,
			boolInt(c.ArchiveListed), boolInt(c.ArchivePinned),
			formatTime(time.Now()), c.ID)
		if err != nil {
			return err
		}
		return requireOneRow(result, "campaign", c.ID)
	})
}

// SetCampaignStatus moves a campaign from one of the from statuses to to and reports whether it
// moved. It is how "only draft/scheduled/paused may start sending" is enforced at the database
// (design D7, invariant 3).
func (db *DB) SetCampaignStatus(ctx context.Context, id string, from []model.CampaignStatus, to model.CampaignStatus) (bool, error) {
	moved := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		moved = false
		placeholders := make([]string, len(from))
		args := []any{string(to), formatTime(time.Now())}
		for i, status := range from {
			placeholders[i] = "?"
			args = append(args, string(status))
		}
		args = append(args, id)
		// The only interpolated text is a run of "?" placeholders; every value is bound.
		query := "UPDATE campaigns SET status = ?, updated_at = ? WHERE status IN (" + //nolint:gosec // G202: placeholders, not values
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

// AddCampaignStats bumps the issue's counters; it is the only writer of the stats_* columns.
func (db *DB) AddCampaignStats(ctx context.Context, id string, delta CampaignCounters) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE campaigns SET
            stats_recipients = stats_recipients + ?, stats_sent = stats_sent + ?,
            stats_delivered = stats_delivered + ?, stats_bounced = stats_bounced + ?,
            stats_complained = stats_complained + ?, stats_opened = stats_opened + ?,
            stats_clicked = stats_clicked + ?, stats_unsubscribed = stats_unsubscribed + ?,
            stats_failed = stats_failed + ?, updated_at = ?
            WHERE id = ?`,
			delta.Recipients, delta.Sent, delta.Delivered, delta.Bounced, delta.Complained,
			delta.Opened, delta.Clicked, delta.Unsubscribed, delta.Failed, formatTime(time.Now()), id)
		return err
	})
}

// AddLastChanceStats bumps the last-chance counters.
func (db *DB) AddLastChanceStats(ctx context.Context, id string, delta LastChanceCounters) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE campaigns SET
            last_chance_recipients = last_chance_recipients + ?,
            last_chance_sent = last_chance_sent + ?, last_chance_delivered = last_chance_delivered + ?,
            last_chance_bounced = last_chance_bounced + ?,
            last_chance_complained = last_chance_complained + ?,
            last_chance_failed = last_chance_failed + ?, last_chance_dormant = last_chance_dormant + ?,
            updated_at = ? WHERE id = ?`,
			delta.Recipients, delta.Sent, delta.Delivered, delta.Bounced, delta.Complained,
			delta.Failed, delta.Dormant, formatTime(time.Now()), id)
		return err
	})
}

// CampaignCountersOf reads both counter groups.
func (db *DB) CampaignCountersOf(ctx context.Context, id string) (CampaignCounters, LastChanceCounters, error) {
	var (
		stats      CampaignCounters
		lastChance LastChanceCounters
	)
	err := db.sql.QueryRowContext(ctx, `SELECT
            stats_recipients, stats_sent, stats_delivered, stats_bounced, stats_complained,
            stats_opened, stats_clicked, stats_unsubscribed, stats_failed,
            last_chance_recipients, last_chance_sent, last_chance_delivered, last_chance_bounced,
            last_chance_complained, last_chance_failed, last_chance_dormant
        FROM campaigns WHERE id = ?`, id).Scan(
		&stats.Recipients, &stats.Sent, &stats.Delivered, &stats.Bounced, &stats.Complained,
		&stats.Opened, &stats.Clicked, &stats.Unsubscribed, &stats.Failed,
		&lastChance.Recipients, &lastChance.Sent, &lastChance.Delivered, &lastChance.Bounced,
		&lastChance.Complained, &lastChance.Failed, &lastChance.Dormant)
	if errors.Is(err, sql.ErrNoRows) {
		return CampaignCounters{}, LastChanceCounters{}, fmt.Errorf("campaign %s: %w", id, ErrNotFound)
	}
	return stats, lastChance, err
}

// SetCampaignStartedAt records the first time a campaign started sending; a campaign resumed or
// sent in steps keeps the earliest (upstream $min: {startedAt}).
func (db *DB) SetCampaignStartedAt(ctx context.Context, id string, at time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE campaigns SET started_at = ?, updated_at = ?
            WHERE id = ? AND (started_at IS NULL OR started_at > ?)`,
			formatTime(at), formatTime(time.Now()), id, formatTime(at))
		return err
	})
}

// SetCampaignRecipients records how many messages the campaign's audience turned out to be.
//
// Upstream sets these two numbers ($set: {"stats.recipients": n, "lastChance.recipients": m}) rather
// than incrementing them, and the difference shows on resume: a paused campaign that resumes
// re-selects the audience and counts it again, and an increment would double it.
func (db *DB) SetCampaignRecipients(ctx context.Context, id string, recipients, lastChance int) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE campaigns SET stats_recipients = ?,
            last_chance_recipients = ?, updated_at = ? WHERE id = ?`,
			recipients, lastChance, formatTime(time.Now()), id)
		return err
	})
}

// SetCampaignSchedule records the time a scheduled campaign goes out.
func (db *DB) SetCampaignSchedule(ctx context.Context, id string, at *time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE campaigns SET scheduled_at = ?, updated_at = ? WHERE id = ?",
			formatTimePtr(at), formatTime(time.Now()), id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "campaign", id)
	})
}

// SetCampaignAudienceLimit records the cap a stepped send uses; 0 clears it.
func (db *DB) SetCampaignAudienceLimit(ctx context.Context, id string, limit int) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE campaigns SET audience_limit = ?, updated_at = ? WHERE id = ?",
			limit, formatTime(time.Now()), id)
		return err
	})
}

// SetCampaignTestSentAt records when a test email of this campaign went out.
func (db *DB) SetCampaignTestSentAt(ctx context.Context, id string, at time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE campaigns SET test_sent_at = ?, updated_at = ? WHERE id = ?",
			formatTime(at), formatTime(time.Now()), id)
		return err
	})
}

// SetCampaignSentAt records the first send's time; a campaign sent in steps keeps the earliest.
func (db *DB) SetCampaignSentAt(ctx context.Context, id string, at time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE campaigns SET sent_at = ?, updated_at = ?
            WHERE id = ? AND (sent_at IS NULL OR sent_at > ?)`,
			formatTime(at), formatTime(time.Now()), id, formatTime(at))
		return err
	})
}

// CampaignByArchiveSlug is the issue behind an archive page: the slug is unique per deployment (the
// store enforces it), and only the three published states have a page. An unlisted issue still
// resolves — it is not linked, but the address works, which is what upstream does.
func (db *DB) CampaignByArchiveSlug(ctx context.Context, newsletterID, slug string) (model.Campaign, error) {
	campaign, err := scanCampaign(db.sql.QueryRowContext(ctx,
		"SELECT "+campaignColumns+` FROM campaigns
         WHERE newsletter_id = ? AND archive_slug = ?
           AND status IN ('sending', 'paused', 'sent')`, newsletterID, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Campaign{}, fmt.Errorf("issue %s: %w", slug, ErrNotFound)
	}
	return campaign, err
}

// NewsletterForArchiveSlug finds the newsletter an archive slug belongs to, for the application
// domain's /p/{slug}: that path does not name the newsletter, so the slug has to.
func (db *DB) NewsletterForArchiveSlug(ctx context.Context, slug string) (model.Newsletter, error) {
	var id string
	err := db.sql.QueryRowContext(ctx,
		`SELECT newsletter_id FROM campaigns
          WHERE archive_slug = ? AND status IN ('sending', 'paused', 'sent') ORDER BY id LIMIT 1`, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Newsletter{}, fmt.Errorf("issue %s: %w", slug, ErrNotFound)
	}
	if err != nil {
		return model.Newsletter{}, err
	}
	newsletter, err := db.Newsletter(ctx, id)
	if err != nil {
		return model.Newsletter{}, err
	}
	if !newsletter.ArchiveEnabled {
		// The archive switch is off: the issue has a slug from when it was on, and no page now.
		return model.Newsletter{}, fmt.Errorf("newsletter %s: %w", newsletter.Slug, ErrNotFound)
	}
	return newsletter, nil
}

// AddWebViews counts readers who looked at an issue's page.
//
// The count is incremented here rather than by a beacon, because there is no beacon any more: the
// page is rendered per request when there is no ISR layer to serve it from a cache (design §10, D11).
func (db *DB) AddWebViews(ctx context.Context, campaignID string, n int) error {
	if n <= 0 {
		return nil
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE campaigns SET web_views = web_views + ? WHERE id = ?",
			n, campaignID)
		return err
	})
}

// CampaignMessageKinds is what kind of message this campaign wrote for each contact, by contact id.
// It is ReachedContacts under a name that says what the list cleanup asks it.
func (db *DB) CampaignMessageKinds(ctx context.Context, campaignID string) (map[string]model.MessageKind, error) {
	return db.ReachedContacts(ctx, campaignID)
}

// SetCampaignArchiveSlug stores the address an issue's page will live at, once it is going out.
//
// It is a setter of its own rather than an UpdateCampaign because the caller is the send: it has the
// id and the slug, and rewriting every column of a campaign from a partial copy is how a concurrent
// edit gets lost.
func (db *DB) SetCampaignArchiveSlug(ctx context.Context, id, slug string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE campaigns SET archive_slug = ?, updated_at = ? WHERE id = ?",
			slug, formatTime(time.Now()), id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "campaign", id)
	})
}
