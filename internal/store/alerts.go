package store

import (
	"context"
	"database/sql"
	"time"
)

// ClaimAlert takes the right to send one alert, if its cooldown has passed (upstream claimAlert).
//
// It is one statement, and that is the point: two processes reaching the same condition at the same
// moment cannot both send, because the conditional update is what decides. The first one passes; the
// second finds last_sent_at still inside the window and gets false.
func (db *DB) ClaimAlert(ctx context.Context, key, title string, cooldown time.Duration, now time.Time) (bool, error) {
	claimed := false
	cutoff := formatTime(now.Add(-cooldown))
	err := db.Write(ctx, func(tx *sql.Tx) error {
		// The table already exists: phase 1 created the whole schema. A row that has never been sent
		// has a NULL last_sent_at, which the comparison has to allow for explicitly — NULL <= x is
		// NULL, not true.
		result, err := tx.ExecContext(ctx, `INSERT INTO alerts
                (key, title, last_sent_at, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(key) DO UPDATE SET title = excluded.title, last_sent_at = excluded.last_sent_at,
                updated_at = excluded.updated_at
            WHERE alerts.last_sent_at IS NULL OR alerts.last_sent_at <= ?`,
			key, title, formatTime(now), formatTime(now), formatTime(now), cutoff)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		claimed = affected > 0
		return nil
	})
	return claimed, err
}

// AlertRow is one alert key's last send.
type AlertRow struct {
	Key        string
	Title      string
	LastSentAt time.Time
}

// Alerts lists what has been alerted, most recent first: the operator's view of what the deployment
// has been complaining about.
func (db *DB) Alerts(ctx context.Context) ([]AlertRow, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT key, title, last_sent_at FROM alerts ORDER BY last_sent_at DESC, key")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AlertRow
	for rows.Next() {
		var (
			row      AlertRow
			lastSent string
		)
		if err := rows.Scan(&row.Key, &row.Title, &lastSent); err != nil {
			return nil, err
		}
		if row.LastSentAt, err = parseTime(lastSent); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
