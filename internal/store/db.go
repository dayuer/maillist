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
	// It outlives every request, so it deliberately runs on a background context.
	go db.writer() //nolint:gosec // G118: the writer lives as long as the database, not as long as a request
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

// Rows is the read side of the connection pool, for a package that owns a table of its own
// (internal/jobs) and must still read with the same settings the rest of the store uses.
func (db *DB) Rows(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.sql.QueryContext(ctx, query, args...)
}

// Row is Rows for a single row.
func (db *DB) Row(ctx context.Context, query string, args ...any) *sql.Row {
	return db.sql.QueryRowContext(ctx, query, args...)
}

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

// ParseTime is parseTime for packages that read a table of their own (internal/jobs).
func ParseTime(value string) (time.Time, error) { return parseTime(value) }

// ParseTimePtr is the nullable form of ParseTime.
func ParseTimePtr(value sql.NullString) (*time.Time, error) { return parseTimePtr(value) }

// parseTimePtr reads back a nullable column.
func parseTimePtr(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// floatPtr reads back a nullable REAL column.
func floatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

// boolInt is how the schema stores booleans.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// requireOneRow turns "no row matched" into ErrNotFound for an UPDATE or DELETE.
func requireOneRow(result sql.Result, kind, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%s %s: %w", kind, id, ErrNotFound)
	}
	return nil
}
