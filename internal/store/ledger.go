package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"nibwire/internal/core/model"
)

// Day is the UTC day a reservation belongs to, as stored.
func Day(at time.Time) string { return at.UTC().Format("2006-01-02") }

// Charge is how many messages a step sends through each channel. The day's budget is kept per
// channel, so a campaign that splits between two of them reserves against each separately, and one
// channel running out does not spend another's allowance.
type Charge map[string]int

// Total is every message in the charge.
func (c Charge) Total() int {
	total := 0
	for _, n := range c {
		total += n
	}
	return total
}

// Channels lists the channels with something in them, in a stable order: the checks, the SQL and the
// text an operator reads all have to agree, and a map has no order.
func (c Charge) Channels() []string {
	out := make([]string, 0, len(c))
	for channel, n := range c {
		if n > 0 {
			out = append(out, channel)
		}
	}
	slices.Sort(out)
	return out
}

// plus is this charge and another one added together.
func (c Charge) plus(other Charge) Charge {
	out := make(Charge, len(c)+len(other))
	for channel, n := range c {
		out[channel] = n
	}
	for channel, n := range other {
		out[channel] += n
	}
	return out
}

// minus is this charge less what another one accounts for, per channel and never below zero: what is
// left to give back.
func (c Charge) minus(other Charge) Charge {
	out := Charge{}
	for channel, n := range c {
		if left := n - other[channel]; left > 0 {
			out[channel] = left
		}
	}
	return out
}

// beyond is the channels where this charge is larger than the other one, which means the day was
// reserved for fewer messages than were written. Nothing should be able to do that; it is checked
// rather than assumed because the direction it fails in is sending past the ceiling.
func (c Charge) beyond(other Charge) []string {
	var out []string
	for channel, n := range c {
		if n > other[channel] {
			out = append(out, channel)
		}
	}
	slices.Sort(out)
	return out
}

// Limits are the day's ceilings; 0 means no ceiling of our own, as it always has.
type Limits struct {
	Global     int            // the deployment's own day total, every channel counted together
	Credential int            // the credential's own day total, every channel counted together
	Channels   map[string]int // each channel's own day total
}

// Reserve checks the day's budgets and reserves the charge in one transaction (D15). credentialID ""
// is the CLI/system pool, which has no credential budget. ok is false when the reservation does not
// fit; nothing is written then, not even for the channels that had room — a campaign sent halfway is
// the failure this is here to prevent (design §4.3).
//
// The whole check-and-write runs on the writer goroutine's single transaction, and (day,
// credential_id, channel) is the primary key, so two concurrent reservations serialise into an
// accumulate rather than losing an update.
func (db *DB) Reserve(ctx context.Context, day, credentialID string, charge Charge, limits Limits) (bool, error) {
	if charge.Total() <= 0 {
		return true, nil
	}
	fits := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		fits, err = reserveTx(ctx, tx, day, credentialID, charge, limits)
		return err
	})
	return fits, err
}

// reserveTx is Reserve inside the caller's transaction, so that a reservation can be committed
// exactly when the work it pays for is (see ReserveAndInsert).
func reserveTx(ctx context.Context, tx *sql.Tx, day, credentialID string, charge Charge,
	limits Limits) (bool, error) {
	channels := charge.Channels()
	if len(channels) == 0 {
		return true, nil
	}
	var global, credential int
	if err := tx.QueryRowContext(ctx,
		"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ?", day).Scan(&global); err != nil {
		return false, err
	}
	if credentialID != "" {
		if err := tx.QueryRowContext(ctx,
			"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ? AND credential_id = ?",
			day, credentialID).Scan(&credential); err != nil {
			return false, err
		}
	}
	// The deployment's ceiling and the credential's cover every channel; a channel's own ceiling
	// covers only itself. Every check runs before the first write, so a refusal reserves nothing.
	if limits.Global > 0 && global+charge.Total() > limits.Global {
		return false, nil
	}
	if limits.Credential > 0 && credential+charge.Total() > limits.Credential {
		return false, nil
	}
	for _, channel := range channels {
		channelMax := limits.Channels[channel]
		if channelMax <= 0 {
			continue
		}
		var used int
		if err := tx.QueryRowContext(ctx,
			`SELECT coalesce(sum(reserved), 0) FROM send_ledger
                WHERE day = ? AND credential_id = ? AND channel = ?`,
			day, credentialID, channel).Scan(&used); err != nil {
			return false, err
		}
		if used+charge[channel] > channelMax {
			return false, nil
		}
	}
	for _, channel := range channels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO send_ledger (day, credential_id, channel, reserved)
            VALUES (?, ?, ?, ?)
            ON CONFLICT(day, credential_id, channel) DO UPDATE SET reserved = reserved + excluded.reserved`,
			day, credentialID, channel, charge[channel]); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Refund gives a charge back to the day's pools: a step that reserved for messages it then could not
// write has not spent the day's allowance, and leaving it reserved would refuse sends that fit.
func (db *DB) Refund(ctx context.Context, day, credentialID string, charge Charge) error {
	if charge.Total() <= 0 {
		return nil
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		return refundTx(ctx, tx, day, credentialID, charge)
	})
}

func refundTx(ctx context.Context, tx *sql.Tx, day, credentialID string, charge Charge) error {
	for _, channel := range charge.Channels() {
		// The floor at zero keeps a double refund from going negative.
		if _, err := tx.ExecContext(ctx, `UPDATE send_ledger SET reserved = max(0, reserved - ?)
            WHERE day = ? AND credential_id = ? AND channel = ?`,
			charge[channel], day, credentialID, channel); err != nil {
			return err
		}
	}
	return nil
}

// Reserved is how much of a day's pool has been reserved: credentialID "" means the global total,
// and every channel counts toward it. This is what the deployment's own ceiling is compared against.
func (db *DB) Reserved(ctx context.Context, day, credentialID string) (int, error) {
	var reserved int
	var err error
	if credentialID == "" {
		err = db.sql.QueryRowContext(ctx,
			"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ?", day).Scan(&reserved)
	} else {
		err = db.sql.QueryRowContext(ctx,
			"SELECT coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ? AND credential_id = ?",
			day, credentialID).Scan(&reserved)
	}
	if err != nil {
		return 0, fmt.Errorf("read send_ledger: %w", err)
	}
	return reserved, nil
}

// ReservedByChannel is the same day's reservations, one count per channel: what a refusal names, and
// what a channel's own ceiling is compared against.
func (db *DB) ReservedByChannel(ctx context.Context, day, credentialID string) (Charge, error) {
	query := "SELECT channel, coalesce(sum(reserved), 0) FROM send_ledger WHERE day = ?"
	args := []any{day}
	if credentialID != "" {
		query += " AND credential_id = ?"
		args = append(args, credentialID)
	}
	rows, err := db.sql.QueryContext(ctx, query+" GROUP BY channel", args...)
	if err != nil {
		return nil, fmt.Errorf("read send_ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := Charge{}
	for rows.Next() {
		var (
			channel  string
			reserved int
		)
		if err := rows.Scan(&channel, &reserved); err != nil {
			return nil, fmt.Errorf("read send_ledger: %w", err)
		}
		if reserved != 0 {
			out[channel] = reserved
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read send_ledger: %w", err)
	}
	return out, nil
}

// chargeOf is a step's charge: how many of its messages each channel carries.
func chargeOf(messages []model.Message) Charge {
	charge := Charge{}
	for _, message := range messages {
		charge[channelOf(message)]++
	}
	return charge
}
