package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ChannelRow is one configured sending channel (design 2026-10-12-multi-channel-sending §3). The
// default channel is not a row: it is the SES_* settings.
type ChannelRow struct {
	Name         string
	Type         string            // ses; resend and sendgrid arrive with later steps
	SecretEnv    string            // prefix of the environment variables that hold its secrets
	Params       map[string]string // non-secret settings, e.g. region and configuration_set
	MaxPerSecond int               // 0 = no ceiling of our own
	MaxPerDay    int               // 0 = no ceiling of our own
	Enabled      bool
	Down         ChannelDownState // read only: MarkChannelDown and ClearChannelDown write it
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrChannelExists is CreateChannel refusing a name that is taken.
var ErrChannelExists = errors.New("channel already exists")

const channelColumns = `name, type, secret_env, params, max_per_second, max_per_day, enabled,
    created_at, updated_at`

// channelReadColumns is what scanChannel reads: the written columns and the down state.
const channelReadColumns = channelColumns + ", down_reason, down_since, down_until"

// CreateChannel writes a new channel. The name must be free.
func (db *DB) CreateChannel(ctx context.Context, c ChannelRow) error {
	params := c.Params
	if params == nil {
		params = map[string]string{}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("channel %s params: %w", c.Name, err)
	}
	created := nonZero(c.CreatedAt).UTC()
	return db.Write(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx,
			"SELECT count(*) FROM channels WHERE name = ?", c.Name).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("channel %s: %w", c.Name, ErrChannelExists)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO channels (`+channelColumns+`)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Type, c.SecretEnv, string(encoded), c.MaxPerSecond, c.MaxPerDay,
			boolInt(c.Enabled), formatTime(created), formatTime(created))
		return err
	})
}

// Channel loads one channel by name.
func (db *DB) Channel(ctx context.Context, name string) (ChannelRow, error) {
	row, err := scanChannel(db.sql.QueryRowContext(ctx,
		"SELECT "+channelReadColumns+" FROM channels WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelRow{}, fmt.Errorf("channel %s: %w", name, ErrNotFound)
	}
	return row, err
}

// Channels lists every configured channel by name. The default channel is not among them.
func (db *DB) Channels(ctx context.Context) ([]ChannelRow, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT "+channelReadColumns+" FROM channels ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ChannelRow
	for rows.Next() {
		row, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetChannelEnabled turns a channel on or off. Turning it on also clears its down state: that is how
// an operator puts back a channel that failed with ErrorPause (design §4.4). An unknown name is
// ErrNotFound.
func (db *DB) SetChannelEnabled(ctx context.Context, name string, enabled bool) error {
	query := "UPDATE channels SET enabled = ?, updated_at = ? WHERE name = ?"
	if enabled {
		query = `UPDATE channels SET enabled = ?, updated_at = ?, down_reason = '', down_since = NULL,
            down_until = NULL WHERE name = ?`
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, boolInt(enabled), formatTime(time.Now().UTC()), name)
		if err != nil {
			return err
		}
		return requireOneRow(result, "channel", name)
	})
}

func scanChannel(row interface{ Scan(...any) error }) (ChannelRow, error) {
	var (
		c                  ChannelRow
		params             string
		enabled            int
		created, updatedAt string
		downReason         string
		downSince, downTo  sql.NullString
	)
	if err := row.Scan(&c.Name, &c.Type, &c.SecretEnv, &params, &c.MaxPerSecond, &c.MaxPerDay,
		&enabled, &created, &updatedAt, &downReason, &downSince, &downTo); err != nil {
		return ChannelRow{}, err
	}
	c.Params = map[string]string{}
	if err := json.Unmarshal([]byte(params), &c.Params); err != nil {
		return ChannelRow{}, fmt.Errorf("channel %s params: %w", c.Name, err)
	}
	c.Enabled = enabled != 0
	var err error
	if c.Down, err = downState(downReason, downSince, downTo); err != nil {
		return ChannelRow{}, err
	}
	if c.CreatedAt, err = parseTime(created); err != nil {
		return ChannelRow{}, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return ChannelRow{}, err
	}
	return c, nil
}
