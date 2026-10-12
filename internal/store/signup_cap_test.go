package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// 上限与插入是一条语句：并发的注册不能都读到「还没到上限」然后都写。
//
// ST-I6 from the phase 1-3 audit. The cap was a count followed by an insert, so a burst arriving
// between the two went through — exactly the moment the cap exists for (a signup flood is what
// burns a sending domain's reputation).
func TestSignupCapIsEnforcedByTheInsert(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	since := time.Now().UTC().Add(-time.Minute)
	kinds := []model.MessageKind{model.KindWelcome, model.KindConfirmation}
	const cap = 5

	// Twelve signups race for five slots.
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		written int
	)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			message := model.Message{
				ID: ids.New(), Kind: model.KindWelcome, NewsletterID: newsletter.ID,
				ContactID: ids.New(), Email: fmt.Sprintf("reader%d@example.com", i),
				Status: model.MessagePending, CreatedAt: time.Now().UTC(),
			}
			inserted, err := db.InsertSignupMessage(ctx, message, newsletter.ID, kinds, since, cap)
			if err != nil {
				t.Error(err)
				return
			}
			if inserted {
				mu.Lock()
				written++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if written != cap {
		t.Fatalf("%d signup messages were written, want exactly %d", written, cap)
	}
	var stored int
	if err := db.Row(ctx, "SELECT count(*) FROM messages WHERE newsletter_id = ?", newsletter.ID).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != cap {
		t.Fatalf("stored = %d, want %d", stored, cap)
	}
}

// 窗口之外的旧消息不占名额，另一个 newsletter 也不占。
func TestSignupCapCountsOneNewsletterInsideTheWindow(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	mine, other := seedNewsletter(t, db), seedNewsletter(t, db)
	kinds := []model.MessageKind{model.KindWelcome, model.KindConfirmation}

	// One message from long ago, and one belonging to the other newsletter.
	insertOld := func(newsletterID string, createdAt time.Time) {
		message := model.Message{
			ID: ids.New(), Kind: model.KindWelcome, NewsletterID: newsletterID,
			ContactID: ids.New(), Email: "old@example.com", Status: model.MessagePending,
			CreatedAt: createdAt,
		}
		if _, err := db.InsertMessages(ctx, []model.Message{message}); err != nil {
			t.Fatal(err)
		}
	}
	insertOld(mine.ID, time.Now().UTC().Add(-time.Hour))
	insertOld(other.ID, time.Now().UTC())

	now := time.Now().UTC()
	since := now.Add(-10 * time.Minute)
	fresh := func() model.Message {
		return model.Message{
			ID: ids.New(), Kind: model.KindWelcome, NewsletterID: mine.ID, ContactID: ids.New(),
			Email: "new@example.com", Status: model.MessagePending, CreatedAt: time.Now().UTC(),
		}
	}
	// A cap of one inside the window: the old message and the other newsletter's do not count.
	if inserted, err := db.InsertSignupMessage(ctx, fresh(), mine.ID, kinds, since, 1); err != nil || !inserted {
		t.Fatalf("first = %v %v, want it written", inserted, err)
	}
	if inserted, err := db.InsertSignupMessage(ctx, fresh(), mine.ID, kinds, since, 1); err != nil || inserted {
		t.Fatalf("second = %v %v, want it refused", inserted, err)
	}
}

// A signup message written without a channel is on the default channel; one written with a channel
// keeps it.
func TestSignupMessageChannelRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	since := time.Now().UTC().Add(-time.Minute)
	kinds := []model.MessageKind{model.KindWelcome, model.KindConfirmation}

	want := map[string]string{}
	for i, channel := range []string{"", "resend"} {
		message := model.Message{
			ID: ids.New(), Kind: model.KindWelcome, NewsletterID: newsletter.ID, ContactID: ids.New(),
			Email: fmt.Sprintf("reader%d@example.com", i), Status: model.MessagePending,
			CreatedAt: time.Now().UTC(), Channel: channel,
		}
		inserted, err := db.InsertSignupMessage(ctx, message, newsletter.ID, kinds, since, 100)
		if err != nil || !inserted {
			t.Fatalf("channel %q: inserted = %v, err = %v, want it written", channel, inserted, err)
		}
		want[message.ID] = map[string]string{"": model.DefaultChannel, "resend": "resend"}[channel]
	}
	for id, channel := range want {
		got, err := db.Message(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Channel != channel {
			t.Errorf("message %s: channel = %q, want %q", id, got.Channel, channel)
		}
	}
}
