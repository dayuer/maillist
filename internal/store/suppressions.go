package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"nibwire/internal/core/model"
)

// SuppressedEmail is one row of the global suppression list.
type SuppressedEmail struct {
	Email  string
	Reason model.SuppressionReason
	Detail string
}

// AddSuppression upserts one address.
func (db *DB) AddSuppression(ctx context.Context, email string, reason model.SuppressionReason, detail string) error {
	clean := strings.ToLower(strings.TrimSpace(email))
	now := formatTime(time.Now())
	return db.Write(ctx, func(tx *sql.Tx) error {
		return upsertSuppression(ctx, tx, clean, reason, detail, now)
	})
}

// upsertSuppression is the one place a suppression row is written. A complaint outranks an earlier
// bounce or manual entry, as upstream's suppress() does; anything else keeps the reason already
// recorded, so a bounce never replaces a complaint.
func upsertSuppression(ctx context.Context, tx *sql.Tx, email string, reason model.SuppressionReason,
	detail, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO suppressions (email, reason, detail, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(email) DO UPDATE SET
            reason = CASE WHEN excluded.reason = 'complaint' THEN excluded.reason ELSE suppressions.reason END,
            detail = CASE WHEN excluded.reason = 'complaint' THEN excluded.detail ELSE suppressions.detail END,
            updated_at = excluded.updated_at`,
		strings.ToLower(strings.TrimSpace(email)), string(reason), detail, now, now)
	return err
}

// RemoveSuppression deletes one address and reports whether it was there.
func (db *DB) RemoveSuppression(ctx context.Context, email string) (bool, error) {
	removed := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		removed = false
		result, err := tx.ExecContext(ctx, "DELETE FROM suppressions WHERE email = ?",
			strings.ToLower(strings.TrimSpace(email)))
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		removed = affected > 0
		return nil
	})
	return removed, err
}

// Suppression loads one address; ok is false when it isn't suppressed.
func (db *DB) Suppression(ctx context.Context, email string) (model.SuppressionReason, bool, error) {
	var reason string
	err := db.sql.QueryRowContext(ctx, "SELECT reason FROM suppressions WHERE email = ?",
		strings.ToLower(strings.TrimSpace(email))).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read suppression: %w", err)
	}
	return model.SuppressionReason(reason), true, nil
}

// Suppressions lists every suppressed address, oldest first.
// CountSuppressions is how many addresses are suppressed, without reading them.
//
// The set is a count for the tools that only report it: a suppression list grows with every bounce
// and complaint, and reading all of it to take len() is work that grows with it too (phase 9 #45).
func (db *DB) CountSuppressions(ctx context.Context) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM suppressions").Scan(&count)
	return count, err
}

// SuppressionsPage reads one page of suppressed addresses, oldest first.
//
// The page is cut in SQL rather than in Go: Supressions returns the whole set because the send path
// needs it as a set, but a caller showing twenty rows should not read a hundred thousand to do it.
func (db *DB) SuppressionsPage(ctx context.Context, limit, offset int) ([]SuppressedEmail, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.sql.QueryContext(ctx,
		"SELECT email, reason, detail FROM suppressions ORDER BY created_at, email LIMIT ? OFFSET ?",
		limit, max(offset, 0))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SuppressedEmail
	for rows.Next() {
		var suppression SuppressedEmail
		if err := rows.Scan(&suppression.Email, &suppression.Reason, &suppression.Detail); err != nil {
			return nil, err
		}
		out = append(out, suppression)
	}
	return out, rows.Err()
}

func (db *DB) Suppressions(ctx context.Context) ([]SuppressedEmail, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT email, reason, detail FROM suppressions ORDER BY created_at, email")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SuppressedEmail
	for rows.Next() {
		var entry SuppressedEmail
		var reason string
		if err := rows.Scan(&entry.Email, &reason, &entry.Detail); err != nil {
			return nil, err
		}
		entry.Reason = model.SuppressionReason(reason)
		out = append(out, entry)
	}
	return out, rows.Err()
}
