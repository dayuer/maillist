package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "nibwire.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := parseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestOpenConfiguresSQLite(t *testing.T) {
	db := openTemp(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"foreign_keys": "1",
	} {
		var got string
		if err := db.sql.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
}

// migrationsAtThisRevision is how many files internal/store/migrations holds. It is spelled out so
// that adding a migration is a deliberate edit here too, rather than a test that quietly follows.
const migrationsAtThisRevision = 5

// Opening twice must not re-apply a migration: 0001 creates tables without IF NOT EXISTS, so a
// second run would fail loudly.
func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nibwire.db")
	ctx := context.Background()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := first.sql.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != migrationsAtThisRevision {
		t.Fatalf("applied = %d, want %d", applied, migrationsAtThisRevision)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = second.Close() }()
	var after int
	if err := second.sql.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != migrationsAtThisRevision {
		t.Fatalf("applied after reopening = %d, want %d", after, migrationsAtThisRevision)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 11, 17, 54, 52, 0, time.UTC)
	encoded := formatTime(at)
	if encoded != "2026-10-11T17:54:52.000Z" {
		t.Fatalf("encoded = %q", encoded)
	}
	if decoded := mustParseTime(t, encoded); !decoded.Equal(at) {
		t.Fatalf("decoded = %s", decoded)
	}
	if formatTimePtr(nil) != nil {
		t.Fatal("a nil time must store as NULL")
	}
}
