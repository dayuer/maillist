package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/stats"
)

// The subscriber curve and the hourly send series are bucketed in Go, not in SQL: SQLite's date
// functions know fixed offsets ("+3 hours") and not IANA zones, so a `--tz` of Asia/Nicosia, or a
// day that is 23 or 25 hours long, cannot be expressed in a query. What SQL can do — the counts, the
// sums per period, the grouping by provider — is done in SQL. The rows these return are bounded by
// the range being shown: 60 days of activity, two days of sends.

// ActiveBefore is the starting count of the subscriber curve: the active contacts who were already
// there when the range opened.
func (db *DB) ActiveBefore(ctx context.Context, newsletterID string, before time.Time) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM contacts
        WHERE newsletter_id = ? AND status = ? AND subscribed_at IS NOT NULL AND subscribed_at < ?`,
		newsletterID, string(model.ContactActive), formatTime(before)).Scan(&count)
	return count, err
}

// JoiningContactsSince is the contacts who joined inside the range: active, with a subscribe date in
// it. The day they count on is the later of that date and the confirmation, which is why both
// travel.
func (db *DB) JoiningContactsSince(ctx context.Context, newsletterID string, since time.Time) ([]stats.Activity, error) {
	return db.activities(ctx, `SELECT source, subscribed_at, confirmed_at, created_at
        FROM contacts
        WHERE newsletter_id = ? AND status = 'active'
          AND subscribed_at IS NOT NULL AND subscribed_at >= ?
        ORDER BY created_at, id`, newsletterID, formatTime(since))
}

// UnsubscribingContactsSince is the contacts who left inside the range, whatever the reason.
//
// Upstream's query also drops a leave recorded within a minute of the contact being created — an
// import's own row, whose unsubscribe happened before Nibwire knew the address. That comparison
// needs arithmetic on two dates, which the fixed-width TEXT columns cannot do in a comparison, so
// the caller applies stats.HasLeftInNibwire to these rows.
func (db *DB) UnsubscribingContactsSince(ctx context.Context, newsletterID string, since time.Time) ([]stats.Activity, error) {
	return db.activities(ctx, `SELECT source, subscribed_at, confirmed_at, unsubscribed_at, created_at
        FROM contacts
        WHERE newsletter_id = ? AND status IN ('unsubscribed', 'bounced', 'complained')
          AND unsubscribed_at IS NOT NULL AND unsubscribed_at >= ?
        ORDER BY created_at, id`, newsletterID, formatTime(since))
}

// activities runs one of the contact queries above. The three selects differ only in their column
// list, so the scan is shared; a row without the last column reads as NULL.
func (db *DB) activities(ctx context.Context, query string, args ...any) ([]stats.Activity, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []stats.Activity
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		activity, err := activityFrom(values, len(values) > 4)
		if err != nil {
			return nil, err
		}
		out = append(out, activity)
	}
	return out, rows.Err()
}

// SuppressionLeavesSince is the third query upstream runs: recent bounce and complaint suppressions,
// each joined to the contact it belongs to in this newsletter. A suppression an import wrote carries
// the import as its detail and is left out — those contacts never received anything here.
func (db *DB) SuppressionLeavesSince(ctx context.Context, newsletterID string, since time.Time) ([]stats.Activity, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT s.created_at, c.source, c.subscribed_at,
            c.confirmed_at, c.unsubscribed_at, c.created_at
        FROM suppressions s
        JOIN contacts c ON c.email = s.email AND c.newsletter_id = ?
        WHERE s.reason IN ('bounce', 'complaint')
          AND s.detail <> ?
          AND s.created_at >= ?
        ORDER BY s.created_at, c.id`,
		newsletterID, stats.ImportSource, formatTime(since))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []stats.Activity
	for rows.Next() {
		var values [6]sql.NullString
		targets := make([]any, len(values))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		leftAt, err := parseTime(values[0].String)
		if err != nil {
			return nil, err
		}
		activity, err := activityFrom(values[1:], true)
		if err != nil {
			return nil, err
		}
		activity.LeftAt = &leftAt
		out = append(out, activity)
	}
	return out, rows.Err()
}

// SentTimes is every message the newsletter handed to SES since a moment — issues, welcome,
// confirmation and test emails alike, which is upstream's definition of the hourly series.
func (db *DB) SentTimes(ctx context.Context, newsletterID string, since time.Time) ([]time.Time, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT sent_at FROM messages
        WHERE newsletter_id = ? AND sent_at IS NOT NULL AND sent_at >= ?
        ORDER BY sent_at`,
		newsletterID, formatTime(since))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []time.Time
	for rows.Next() {
		var sentAt string
		if err := rows.Scan(&sentAt); err != nil {
			return nil, err
		}
		parsed, err := parseTime(sentAt)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, rows.Err()
}

// EmailTotals is one period's figures for the newsletter's emails. The fields are the ones upstream
// counts: every message with the timestamp set (lib/message-stats.js countIf).
type EmailTotals struct {
	Sent, Delivered, Opened, Clicked, Bounced, Complained int
}

// EmailTotalsSince adds the two halves upstream adds: the issues that started in each period, from
// the campaigns' own counters, and the welcome emails created in it, from their messages.
//
// Test emails reach neither half — they are neither an issue nor a welcome — which is what keeps a
// test send out of the report (design §6.2).
func (db *DB) EmailTotalsSince(ctx context.Context, newsletterID string,
	currentSince, previousSince time.Time) (current, previous EmailTotals, err error) {
	// The boundary between the two periods is currentSince on both sides: the six ">=" arms and the
	// six "<" arms are compared against the same instant, and previousSince only bounds the rows the
	// query looks at.
	args := make([]any, 0, 14)
	for range 12 {
		args = append(args, formatTime(currentSince))
	}
	args = append(args, newsletterID, formatTime(previousSince))
	err = db.sql.QueryRowContext(ctx, `SELECT
            coalesce(sum(CASE WHEN at >= ? THEN stats_sent          ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at >= ? THEN stats_delivered     ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at >= ? THEN stats_opened        ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at >= ? THEN stats_clicked       ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at >= ? THEN stats_bounced       ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at >= ? THEN stats_complained    ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at <  ? THEN stats_sent          ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at <  ? THEN stats_delivered     ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at <  ? THEN stats_opened        ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at <  ? THEN stats_clicked       ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at <  ? THEN stats_bounced       ELSE 0 END), 0),
            coalesce(sum(CASE WHEN at <  ? THEN stats_complained    ELSE 0 END), 0)
        FROM (SELECT coalesce(started_at, sent_at) AS at, stats_sent, stats_delivered,
                     stats_opened, stats_clicked, stats_bounced, stats_complained
              FROM campaigns
              WHERE newsletter_id = ? AND coalesce(started_at, sent_at) >= ?)`,
		args...).Scan(
		&current.Sent, &current.Delivered, &current.Opened, &current.Clicked,
		&current.Bounced, &current.Complained,
		&previous.Sent, &previous.Delivered, &previous.Opened, &previous.Clicked,
		&previous.Bounced, &previous.Complained)
	if err != nil {
		return EmailTotals{}, EmailTotals{}, err
	}

	welcome, err := db.messageTotalsSince(ctx, newsletterID, currentSince, previousSince)
	if err != nil {
		return EmailTotals{}, EmailTotals{}, err
	}
	add := func(into *EmailTotals, from EmailTotals) {
		into.Sent += from.Sent
		into.Delivered += from.Delivered
		into.Opened += from.Opened
		into.Clicked += from.Clicked
		into.Bounced += from.Bounced
		into.Complained += from.Complained
	}
	add(&current, welcome.current)
	add(&previous, welcome.previous)
	return current, previous, nil
}

type messageTotals struct{ current, previous EmailTotals }

// messageTotalsSince counts the welcome emails created in each period, by the same non-null rule the
// campaigns use.
func (db *DB) messageTotalsSince(ctx context.Context, newsletterID string,
	currentSince, previousSince time.Time) (messageTotals, error) {
	var out messageTotals
	err := db.sql.QueryRowContext(ctx, `SELECT
            coalesce(sum(CASE WHEN created_at >= ? AND sent_at       IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at >= ? AND delivered_at  IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at >= ? AND opened_at     IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at >= ? AND clicked_at    IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at >= ? AND bounced_at    IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at >= ? AND complained_at IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at <  ? AND sent_at       IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at <  ? AND delivered_at  IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at <  ? AND opened_at     IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at <  ? AND clicked_at    IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at <  ? AND bounced_at    IS NOT NULL THEN 1 ELSE 0 END), 0),
            coalesce(sum(CASE WHEN created_at <  ? AND complained_at IS NOT NULL THEN 1 ELSE 0 END), 0)
        FROM messages
        WHERE newsletter_id = ? AND kind = 'welcome' AND created_at >= ?`,
		formatTime(currentSince), formatTime(currentSince), formatTime(currentSince),
		formatTime(currentSince), formatTime(currentSince), formatTime(currentSince),
		formatTime(currentSince), formatTime(currentSince), formatTime(currentSince),
		formatTime(currentSince), formatTime(currentSince), formatTime(currentSince),
		newsletterID, formatTime(previousSince)).Scan(
		&out.current.Sent, &out.current.Delivered, &out.current.Opened, &out.current.Clicked,
		&out.current.Bounced, &out.current.Complained,
		&out.previous.Sent, &out.previous.Delivered, &out.previous.Opened, &out.previous.Clicked,
		&out.previous.Bounced, &out.previous.Complained)
	return out, err
}

// ProviderRow is one line of `campaign report`: the issue's own messages, by mailbox provider.
type ProviderRow struct {
	Provider string
	EmailTotals
}

// ProviderBreakdown is upstream getProviderBreakdown: the campaign's own messages grouped by
// provider, most sent first, with the groups that never went out left off.
//
// Only kind = 'campaign' is counted, so a test send (kind = 'test', filed under the sentinel
// contact) can never appear here — that is exactly what the sentinel exists for (design §6.2).
func (db *DB) ProviderBreakdown(ctx context.Context, campaignID string) ([]ProviderRow, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT
            CASE WHEN provider = '' THEN ? ELSE provider END AS provider,
            count(sent_at), count(delivered_at), count(opened_at), count(clicked_at),
            count(bounced_at), count(complained_at), count(*)
        FROM messages
        WHERE campaign_id = ? AND kind = 'campaign'
        GROUP BY provider
        HAVING count(sent_at) > 0
        ORDER BY count(sent_at) DESC, provider`,
		"other", campaignID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ProviderRow
	for rows.Next() {
		var row ProviderRow
		var total int
		if err := rows.Scan(&row.Provider, &row.Sent, &row.Delivered, &row.Opened, &row.Clicked,
			&row.Bounced, &row.Complained, &total); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PublishedIssues is upstream listPublishedIssues: the issues that went out and are listed on the
// archive, newest first. The v1 API lists them for another site to render.
func (db *DB) PublishedIssues(ctx context.Context, newsletterID string) ([]model.Campaign, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT "+campaignColumns+` FROM campaigns
        WHERE newsletter_id = ? AND archive_slug <> '' AND archive_listed <> 0
          AND status IN ('sending', 'paused', 'sent')
        ORDER BY sent_at DESC, started_at DESC`, newsletterID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []model.Campaign
	for rows.Next() {
		campaign, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, campaign)
	}
	return out, rows.Err()
}

// UniqueArchiveSlug is upstream uniqueArchiveSlug: base, then base-2, base-3 … The suffix keeps a
// second issue with the same subject from colliding on the archive slug's unique index.
func (db *DB) UniqueArchiveSlug(ctx context.Context, base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		base = "issue"
	}
	for suffix := 1; suffix < 100; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		var exists int
		if err := db.sql.QueryRowContext(ctx,
			"SELECT count(*) FROM campaigns WHERE archive_slug = ?", candidate).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return candidate, nil
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano()/1e6), nil
}

// activityFrom reads one row of `source, subscribed_at, confirmed_at, [unsubscribed_at,] created_at`
// — the shape the contact queries and the suppression query all end with. A missing date arrives as
// an invalid NullString and reads as nil.
func activityFrom(values []sql.NullString, hasUnsubscribed bool) (stats.Activity, error) {
	if len(values) < 4 {
		return stats.Activity{}, fmt.Errorf("activity row has %d columns, want at least 4", len(values))
	}
	activity := stats.Activity{Source: values[0].String}
	var err error
	if activity.SubscribedAt, err = parseTimePtr(values[1]); err != nil {
		return stats.Activity{}, err
	}
	if activity.ConfirmedAt, err = parseTimePtr(values[2]); err != nil {
		return stats.Activity{}, err
	}
	if hasUnsubscribed {
		if activity.UnsubscribedAt, err = parseTimePtr(values[3]); err != nil {
			return stats.Activity{}, err
		}
	}
	if activity.CreatedAt, err = parseTime(values[len(values)-1].String); err != nil {
		return stats.Activity{}, err
	}
	return activity, nil
}
