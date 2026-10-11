package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ServeHeartbeat is the name the serve process stamps.
const ServeHeartbeat = "serve"

// HeartbeatInterval is how often serve stamps it, and HeartbeatWindow how old a stamp may be before
// the process counts as gone: three missed beats, so a slow moment is not mistaken for a crash.
const (
	HeartbeatInterval = 5 * time.Second
	HeartbeatWindow   = 3 * HeartbeatInterval
)

// TouchHeartbeat records that the named process is alive right now.
func (db *DB) TouchHeartbeat(ctx context.Context, name string, at time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO serve_heartbeat (name, seen_at) VALUES (?, ?)
            ON CONFLICT(name) DO UPDATE SET seen_at = excluded.seen_at`,
			name, formatTime(at))
		return err
	})
}

// ClearHeartbeat removes the stamp, so a clean shutdown is visible at once rather than after the
// window. Failing to clear it is not worth an error: the window covers it.
func (db *DB) ClearHeartbeat(ctx context.Context, name string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM serve_heartbeat WHERE name = ?", name)
		return err
	})
}

// Heartbeat reads the last stamp. ok is false when the process never wrote one.
func (db *DB) Heartbeat(ctx context.Context, name string) (time.Time, bool, error) {
	var seenAt string
	err := db.sql.QueryRowContext(ctx, "SELECT seen_at FROM serve_heartbeat WHERE name = ?", name).Scan(&seenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	parsed, err := parseTime(seenAt)
	if err != nil {
		return time.Time{}, false, err
	}
	return parsed, true, nil
}

// ServeIsRunning reports whether a serve process stamped the heartbeat recently enough to count as
// alive. A command that is about to send asks this first: with a server up, the messages are queued
// for it rather than drained here.
func (db *DB) ServeIsRunning(ctx context.Context, now time.Time) (bool, error) {
	seenAt, ok, err := db.Heartbeat(ctx, ServeHeartbeat)
	if err != nil || !ok {
		return false, err
	}
	return now.Sub(seenAt) < HeartbeatWindow, nil
}
