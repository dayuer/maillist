package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Day is the UTC day a reservation belongs to, as stored.
func Day(at time.Time) string { return at.UTC().Format("2006-01-02") }

// Reserve checks the day's global budget and the credential's own budget and reserves n sends in
// one transaction (D15). credentialID "" is the CLI/system pool, which has no credential budget.
// ok is false when the reservation does not fit; nothing is written then.
//
// The whole check-and-write runs on the writer goroutine's single transaction, and (day,
// credential_id) is the primary key, so two concurrent reservations serialise into an accumulate
// rather than losing an update.
func (db *DB) Reserve(ctx context.Context, day, credentialID string, n, globalMax, credentialMax int) (bool, error) {
	if n <= 0 {
		return true, nil
	}
	fits := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		fits, err = reserveTx(ctx, tx, day, credentialID, n, globalMax, credentialMax)
		return err
	})
	return fits, err
}

// reserveTx is Reserve inside the caller's transaction, so that a reservation can be committed
// exactly when the work it pays for is (see ReserveAndInsert).
func reserveTx(ctx context.Context, tx *sql.Tx, day, credentialID string, n, globalMax, credentialMax int) (bool, error) {
	if n <= 0 {
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
	if globalMax > 0 && global+n > globalMax {
		return false, nil
	}
	if credentialMax > 0 && credential+n > credentialMax {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO send_ledger (day, credential_id, reserved)
        VALUES (?, ?, ?)
        ON CONFLICT(day, credential_id) DO UPDATE SET reserved = reserved + excluded.reserved`,
		day, credentialID, n); err != nil {
		return false, err
	}
	return true, nil
}

// Refund gives n reservations back to a day's pool: a step that reserved for messages it then could
// not write has not spent the day's allowance, and leaving it reserved would refuse sends that fit.
func (db *DB) Refund(ctx context.Context, day, credentialID string, n int) error {
	if n <= 0 {
		return nil
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		return refundTx(ctx, tx, day, credentialID, n)
	})
}

func refundTx(ctx context.Context, tx *sql.Tx, day, credentialID string, n int) error {
	if n <= 0 {
		return nil
	}
	// The floor at zero keeps a double refund from going negative.
	if _, err := tx.ExecContext(ctx, `UPDATE send_ledger SET reserved = max(0, reserved - ?)
        WHERE day = ? AND credential_id = ?`, n, day, credentialID); err != nil {
		return err
	}
	return nil
}

// Reserved is how much of a day's pool has been reserved: credentialID "" means the global total.
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
