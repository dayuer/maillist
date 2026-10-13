package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// 0005 renames ses_message_id on messages and events, and adds messages.channel. A database written
// before it must come through with every value where it was, and every existing message on the
// default channel — the only channel there was.
func TestMigration0005KeepsMessagesAndPutsThemOnTheDefaultChannel(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := openAt(ctx, path, "0004_campaign_reserved_day.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.sql.ExecContext(ctx, `INSERT INTO messages
        (id, kind, newsletter_id, email, status, ses_message_id, created_at)
        VALUES ('m1', 'campaign', 'n1', 'a@example.com', 'sent', '0100abc', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.sql.ExecContext(ctx, `INSERT INTO events
        (id, type, ses_message_id, occurred_at, created_at)
        VALUES ('e1', 'Delivery', '0100abc', '2026-10-01T00:00:01Z', '2026-10-01T00:00:01Z')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var channel, providerID string
	if err := db.sql.QueryRowContext(ctx,
		"SELECT channel, provider_message_id FROM messages WHERE id = 'm1'").Scan(&channel, &providerID); err != nil {
		t.Fatal(err)
	}
	if channel != "default" || providerID != "0100abc" {
		t.Fatalf("channel = %q, provider_message_id = %q", channel, providerID)
	}
	var eventProviderID string
	if err := db.sql.QueryRowContext(ctx,
		"SELECT provider_message_id FROM events WHERE id = 'e1'").Scan(&eventProviderID); err != nil {
		t.Fatal(err)
	}
	if eventProviderID != "0100abc" {
		t.Fatalf("events.provider_message_id = %q", eventProviderID)
	}
}

// The same provider id may come back from two channels; within one channel it is still unique.
func TestProviderMessageIDIsUniquePerChannel(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Each row is its own contact, so that messages_campaign_contact stays out of the way and only
	// the provider id index can refuse a row.
	insert := func(id, channel string) error {
		_, err := db.sql.ExecContext(ctx, `INSERT INTO messages
            (id, kind, newsletter_id, contact_id, email, status, provider_message_id, channel, created_at)
            VALUES (?, 'campaign', 'n1', ?, 'a@example.com', 'sent', 'same-id', ?, '2026-10-01T00:00:00Z')`,
			id, id, channel)
		return err
	}
	if err := insert("m1", "default"); err != nil {
		t.Fatal(err)
	}
	if err := insert("m2", "resend"); err != nil {
		t.Fatalf("a second channel reusing the id was refused: %v", err)
	}
	if err := insert("m3", "default"); err == nil {
		t.Fatal("a duplicate id on the same channel was accepted")
	}

	// An unsent message has no provider id yet; any number of them share a channel.
	unsent := func(id string) error {
		_, err := db.sql.ExecContext(ctx, `INSERT INTO messages
            (id, kind, newsletter_id, contact_id, email, status, provider_message_id, channel, created_at)
            VALUES (?, 'campaign', 'n1', ?, 'a@example.com', 'pending', '', 'default', '2026-10-01T00:00:00Z')`,
			id, id)
		return err
	}
	if err := unsent("m4"); err != nil {
		t.Fatal(err)
	}
	if err := unsent("m5"); err != nil {
		t.Fatalf("a second unsent message on the same channel was refused: %v", err)
	}
}

// openAt opens a database migrated up to and including `last`, the way a deployment that has not
// upgraded yet would have it. A name that is not a migration is an error, rather than a database
// with every migration applied.
func openAt(ctx context.Context, path, last string) (*DB, error) {
	db, err := openRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := migrateTo(ctx, db, last); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrateTo(ctx context.Context, db *DB, last string) error {
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version    TEXT PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return err
	}
	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := db.applyMigration(ctx, name); err != nil {
			return err
		}
		if name == last {
			return nil
		}
	}
	return fmt.Errorf("migration %s not found", last)
}
