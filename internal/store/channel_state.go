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

// ChannelDownState is why and until when a channel is out of service after ErrorPause or ErrorQuota
// (design 2026-10-12-multi-channel-sending §4.4). The zero value is a channel that is up.
type ChannelDownState struct {
	Reason string
	Since  *time.Time // when it first went down
	Until  *time.Time // when it comes back on its own; nil = when an operator runs `channel enable`
}

// DownAt says whether the channel is out of service at now. A state whose Until has passed is
// recovered: nothing has to clear it.
func (s ChannelDownState) DownAt(now time.Time) bool {
	return s.Reason != "" && (s.Until == nil || now.Before(*s.Until))
}

// stateQueries read and write a channel's down state where it lives: its row in channels, or the one
// row of default_channel_state, which stands in for the default channel's missing row. args is what
// the WHERE clause binds: the name for a row of channels, nothing for the default channel.
type stateQueries struct {
	mark, reason, clear, read string
	args                      []any
}

func stateQueriesFor(name string) stateQueries {
	if name == model.DefaultChannel {
		return stateQueries{
			mark: `UPDATE default_channel_state SET
                down_since = CASE WHEN down_reason = '' THEN ? ELSE down_since END,
                down_reason = ?, down_until = ?
            WHERE only = 1`,
			reason: "SELECT down_reason FROM default_channel_state WHERE only = 1",
			clear: `UPDATE default_channel_state SET down_reason = '', down_since = NULL, down_until = NULL
            WHERE only = 1`,
			read: "SELECT down_reason, down_since, down_until FROM default_channel_state WHERE only = 1",
		}
	}
	return stateQueries{
		mark: `UPDATE channels SET
                down_since = CASE WHEN down_reason = '' THEN ? ELSE down_since END,
                down_reason = ?, down_until = ?
            WHERE name = ?`,
		reason: "SELECT down_reason FROM channels WHERE name = ?",
		clear: `UPDATE channels SET down_reason = '', down_since = NULL, down_until = NULL
            WHERE name = ?`,
		read: "SELECT down_reason, down_since, down_until FROM channels WHERE name = ?",
		args: []any{name},
	}
}

// MarkChannelDown records that a channel is out of service. Marking a channel that is already down
// updates the reason and until and keeps when it first went down.
func (db *DB) MarkChannelDown(ctx context.Context, name, reason string, until *time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("a channel is marked down with a reason")
	}
	queries := stateQueriesFor(name)
	now := formatTime(time.Now().UTC())
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, queries.mark,
			append([]any{now, reason, formatTimePtr(until)}, queries.args...)...)
		if err != nil {
			return err
		}
		return requireOneRow(result, "channel", name)
	})
}

// ClearChannelDown puts a channel back in service and says whether it had a down state recorded
// (an expired one counts: it is cleared all the same).
func (db *DB) ClearChannelDown(ctx context.Context, name string) (bool, error) {
	queries := stateQueriesFor(name)
	var was bool
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var reason string
		err := tx.QueryRowContext(ctx, queries.reason, queries.args...).Scan(&reason)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("channel %s: %w", name, ErrNotFound)
		}
		if err != nil {
			return err
		}
		was = reason != ""
		_, err = tx.ExecContext(ctx, queries.clear, queries.args...)
		return err
	})
	return was, err
}

// ChannelDown reads a channel's down state. An unknown channel is ErrNotFound.
func (db *DB) ChannelDown(ctx context.Context, name string) (ChannelDownState, error) {
	queries := stateQueriesFor(name)
	state, err := scanDownState(db.sql.QueryRowContext(ctx, queries.read, queries.args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelDownState{}, fmt.Errorf("channel %s: %w", name, ErrNotFound)
	}
	return state, err
}

func scanDownState(row interface{ Scan(...any) error }) (ChannelDownState, error) {
	var (
		reason       string
		since, until sql.NullString
	)
	if err := row.Scan(&reason, &since, &until); err != nil {
		return ChannelDownState{}, err
	}
	return downState(reason, since, until)
}

func downState(reason string, since, until sql.NullString) (ChannelDownState, error) {
	state := ChannelDownState{Reason: reason}
	var err error
	if state.Since, err = parseTimePtr(since); err != nil {
		return ChannelDownState{}, err
	}
	if state.Until, err = parseTimePtr(until); err != nil {
		return ChannelDownState{}, err
	}
	return state, nil
}

// SetChannelFallbacks replaces a channel's fallback chain with fallbacks, in order; an empty list
// clears it. The caller checks the names (channelspec.ValidateFallbacks and that each one exists):
// the store keeps whatever chain it is given.
func (db *DB) SetChannelFallbacks(ctx context.Context, channel string, fallbacks []string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM channel_fallbacks WHERE channel = ?", channel); err != nil {
			return err
		}
		for position, fallback := range fallbacks {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO channel_fallbacks (channel, position, fallback) VALUES (?, ?, ?)",
				channel, position, fallback); err != nil {
				return err
			}
		}
		return nil
	})
}

// ChannelFallbacks is a channel's fallback chain, in order. A channel without one has an empty chain.
func (db *DB) ChannelFallbacks(ctx context.Context, channel string) ([]string, error) {
	all, err := db.readFallbacks(ctx,
		"SELECT channel, fallback FROM channel_fallbacks WHERE channel = ? ORDER BY position", channel)
	if err != nil {
		return nil, err
	}
	return all[channel], nil
}

// AllChannelFallbacks is every channel's fallback chain, by channel. Channels without one are absent.
func (db *DB) AllChannelFallbacks(ctx context.Context) (map[string][]string, error) {
	return db.readFallbacks(ctx, "SELECT channel, fallback FROM channel_fallbacks ORDER BY channel, position")
}

// readFallbacks groups the (channel, fallback) rows query returns by channel, in the order returned.
func (db *DB) readFallbacks(ctx context.Context, query string, args ...any) (map[string][]string, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var channel, fallback string
		if err := rows.Scan(&channel, &fallback); err != nil {
			return nil, err
		}
		out[channel] = append(out[channel], fallback)
	}
	return out, rows.Err()
}
