// Package store is the SQLite implementation of Mailcheap's data layer. Writes go through a single
// goroutine (writer.go); reads use the connection pool.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure Go driver: CGO_ENABLED=0 keeps the single-binary build
)

// ErrNotFound is returned by every single-row lookup that matched nothing.
var ErrNotFound = errors.New("not found")

// TimeLayout is fixed-width so that ORDER BY on the TEXT column is chronological, and so that
// SQLite's strftime() can read it directly.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// DB is one open SQLite file.
type DB struct {
	sql  *sql.DB
	path string

	writes    chan writeRequest
	closed    chan struct{}
	closeOnce func()
}

// Open opens (creating if needed) the database at path, applies the pragmas, and migrates it.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One writer at a time inside this process; reads may use several connections.
	handle.SetMaxOpenConns(4)
	handle.SetMaxIdleConns(4)

	db := &DB{sql: handle, path: path, writes: make(chan writeRequest), closed: make(chan struct{})}
	db.closeOnce = sync.OnceFunc(func() {
		close(db.closed)
		_ = handle.Close()
	})
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	// The writer must be running before Migrate, which goes through it — migrations run at open
	// time, and the phase 4 `db migrate` command needs the same path while a server is up.
	go db.writer()
	if err := db.Migrate(ctx); err != nil {
		db.closeOnce()
		return nil, err
	}
	return db, nil
}

// Close stops the writer and closes the pool.
func (db *DB) Close() error {
	db.closeOnce()
	return nil
}

// Path is the file this DB was opened from.
func (db *DB) Path() string { return db.path }

// formatTime encodes a time the way every TEXT column stores it.
func formatTime(at time.Time) string { return at.UTC().Format(TimeLayout) }

// FormatTime is formatTime for the packages that write rows of their own (internal/jobs).
func FormatTime(at time.Time) string { return formatTime(at) }

// formatTimePtr encodes an optional time: NULL when nil.
func formatTimePtr(at *time.Time) any {
	if at == nil {
		return nil
	}
	return formatTime(*at)
}

// parseTime reads back a NOT NULL column.
func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(TimeLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", value, err)
	}
	return parsed, nil
}
