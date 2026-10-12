package mailer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/store"
)

// benchMessages builds n pending campaign messages without touching the contacts table: messages do
// not reference contacts, and the measurement is about the messages table.
func benchMessages(tb testing.TB, db *store.DB, n int) []model.Message {
	tb.Helper()
	ctx := context.Background()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "bench-" + ids.New(), Name: "Bench",
		FromEmail: "bench@example.net", CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateNewsletter(ctx, newsletter); err != nil {
		tb.Fatal(err)
	}
	// claimable: a message is only claimable while its campaign is sending.
	campaign := model.NewCampaign(newsletter.ID, "Bench", time.Now().UTC())
	campaign.Status = model.CampaignSending
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		tb.Fatal(err)
	}
	messages := make([]model.Message, 0, n)
	for i := 0; i < n; i++ {
		messages = append(messages, model.Message{
			ID:           ids.New(),
			Kind:         model.KindCampaign,
			CampaignID:   campaign.ID,
			NewsletterID: newsletter.ID,
			ContactID:    ids.New(),
			Email:        fmt.Sprintf("reader%d@example.com", i),
			Status:       model.MessagePending,
			CreatedAt:    time.Now().UTC(),
		})
	}
	return messages
}

// BenchmarkMessageInsert measures the write that decides whether a 50k send is practical: one
// transaction per store.InsertChunkSize rows.
func BenchmarkMessageInsert(b *testing.B) {
	for i := 0; i < b.N; i++ {
		db, err := store.Open(context.Background(), filepath.Join(b.TempDir(), "nibwire.db"))
		if err != nil {
			b.Fatal(err)
		}
		messages := benchMessages(b, db, 50_000)
		b.StartTimer()
		if _, err := db.InsertMessages(context.Background(), messages); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		_ = db.Close()
	}
}

// TestMessageInsertBudget is the plan's gate: creating the messages for a 50,000-recipient send has
// to stay under 10 seconds. Above that, look at the chunk size and the indexes before moving on to
// phase 3 rather than carrying a slow write forward.
func TestMessageInsertBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts 50,000 rows")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "nibwire.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	messages := benchMessages(t, db, 50_000)
	start := time.Now()
	inserted, err := db.InsertMessages(ctx, messages)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != len(messages) {
		t.Fatalf("inserted = %d, want %d", inserted, len(messages))
	}
	t.Logf("inserted %d messages in %s (%.0f rows/s)",
		inserted, elapsed.Round(time.Millisecond), float64(inserted)/elapsed.Seconds())
	if elapsed > 10*time.Second {
		if raceEnabled {
			t.Logf("over the 10s budget, but this is a -race build; the budget is for the production binary")
		} else {
			t.Fatalf("inserting %d messages took %s; the budget is 10s", len(messages), elapsed)
		}
	}

	// And the queue can be drained again: claiming 50k rows must be a single indexed query, not a
	// scan of the whole table per batch.
	start = time.Now()
	claimedTotal := 0
	for {
		claimed, err := db.ClaimMessages(ctx, store.InsertChunkSize)
		if err != nil {
			t.Fatal(err)
		}
		if len(claimed) == 0 {
			break
		}
		claimedTotal += len(claimed)
	}
	claimElapsed := time.Since(start)
	if claimedTotal != len(messages) {
		t.Fatalf("claimed = %d, want %d", claimedTotal, len(messages))
	}
	t.Logf("claimed %d messages in %s", claimedTotal, claimElapsed.Round(time.Millisecond))
	if claimElapsed > 10*time.Second {
		if raceEnabled {
			t.Logf("over the 10s budget, but this is a -race build")
		} else {
			t.Fatalf("claiming %d messages took %s", claimedTotal, claimElapsed)
		}
	}
}
