package store

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"time"

	"modernc.org/sqlite"
)

// ErrBusy is returned when SQLITE_BUSY outlived the retry budget: another process is holding the
// writer lock, or a transaction is not finishing. Only writes can return it.
var ErrBusy = errors.New("database is busy")

const (
	busyBaseDelay = 10 * time.Millisecond
	busyMaxDelay  = time.Second
	busyBudget    = 10 * time.Second
)

// writeRequest is one unit of work for the writer goroutine.
type writeRequest struct {
	fn   func(*sql.Tx) error
	done chan error
}

// Write runs fn inside a transaction on the single writer goroutine, retrying SQLITE_BUSY with
// jittered backoff. fn may run more than once, so it must not depend on state it changed itself.
func (db *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	req := writeRequest{fn: fn, done: make(chan error, 1)}
	select {
	case db.writes <- req:
	case <-db.closed:
		return sql.ErrConnDone
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (db *DB) writer() {
	for {
		select {
		case req := <-db.writes:
			req.done <- retryBusy(context.Background(), func() error {
				return db.runWrite(req.fn)
			}, busyBudget)
		case <-db.closed:
			return
		}
	}
}

func (db *DB) runWrite(fn func(*sql.Tx) error) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// retryBusy runs attempt until it stops returning SQLITE_BUSY or the budget runs out. The delay
// doubles from 10 ms to 1 s, with jitter so two processes don't retry in lockstep.
func retryBusy(ctx context.Context, attempt func() error, budget time.Duration) error {
	return retry(ctx, attempt, isBusy, budget)
}

// retry is retryBusy with the retryable predicate injected: the driver's error type cannot be built
// from outside its package, so the give-up path is only testable this way.
func retry(ctx context.Context, attempt func() error, retryable func(error) bool, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	delay := busyBaseDelay
	for {
		err := attempt()
		if err == nil || !retryable(err) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrBusy
		}
		// Jitter only has to decorrelate two processes' retries; it is not a secret.
		jittered := delay/2 + time.Duration(rand.Int63n(int64(delay/2+1))) //nolint:gosec // G404: backoff jitter, not a security value
		select {
		case <-time.After(jittered):
		case <-ctx.Done():
			return ctx.Err()
		}
		if delay < busyMaxDelay {
			delay *= 2
			if delay > busyMaxDelay {
				delay = busyMaxDelay
			}
		}
	}
}

// isBusy matches the driver's SQLITE_BUSY and SQLITE_LOCKED: a cross-process writer lock reports the
// locked variant.
func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() {
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	return false
}
