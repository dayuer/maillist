package store

import (
	"context"
	"database/sql"
)

// writeRequest is one unit of work for the writer goroutine.
type writeRequest struct {
	fn   func(*sql.Tx) error
	done chan error
}

// Write runs fn inside a transaction on the single writer goroutine, so this process has one writer
// at a time and transactions stay short.
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
			req.done <- db.runWrite(req.fn)
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
