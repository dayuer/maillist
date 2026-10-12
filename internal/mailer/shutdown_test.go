package mailer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nibwire/internal/store"
)

// claimedCount is how many messages are claimed right now; the graceful-shutdown invariant is that
// it is zero, and that nothing became unknown.
func claimedCount(t *testing.T, db *store.DB) (claimed, unknown, pending int) {
	t.Helper()
	for status, target := range map[string]*int{
		"claimed": &claimed, "unknown": &unknown, "pending": &pending,
	} {
		if err := db.Row(context.Background(),
			"SELECT count(*) FROM messages WHERE status = ?", status).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	return claimed, unknown, pending
}

// 关停中的 ctx 交给 SendBatch 时是「停」，不是「失败」：不领任何消息，也不返回错。
//
// This is the contract that made TestGracefulShutdownLeavesNothingClaimed flake: Run's select picks
// at random between a done context and a ready ticker, so a shutdown can reach SendBatch through the
// ticker case. Returning an error there means a graceful stop is reported as a broken send.
func TestSendBatchOnACancelledContextStopsCleanly(t *testing.T) {
	f := newFixture(t, 4)
	if _, err := f.engine.Queue(context.Background(), f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatalf("SendBatch on a cancelled context = %v, want no error", err)
	}
	if !result.Retry || result.Done() != 0 {
		t.Fatalf("result = %+v, want Retry with nothing claimed", result)
	}
	claimed, unknown, pending := claimedCount(t, f.db)
	if claimed != 0 || unknown != 0 || pending != 4 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d, want 0/0/4", claimed, unknown, pending)
	}
	if f.fake.SentCount() != 0 {
		t.Fatalf("sent = %d, want 0", f.fake.SentCount())
	}
}

// 同一个关停场景跑多轮：Run 的 select 可能在 ctx 结束时选中 ticker，那就撞上上面那条契约。
// 这是概率性的，所以跑若干轮；旧代码在第 0–1 轮就会翻车。
func TestGracefulShutdownRepeatedly(t *testing.T) {
	for round := 0; round < 15; round++ {
		f := newFixture(t, 10)
		ctx, cancel := context.WithCancel(context.Background())
		if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		f.fake.OnSend = func(n int) {
			if n == 3 {
				cancel()
			}
		}
		go func() { done <- f.engine.Run(ctx) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("round %d: Run = %v, want a clean stop", round, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: the engine did not stop", round)
		}
		f.fake.OnSend = nil
		cancel()
		if claimed, unknown, _ := claimedCount(t, f.db); claimed != 0 || unknown != 0 {
			t.Fatalf("round %d: claimed = %d, unknown = %d, want 0/0", round, claimed, unknown)
		}
	}
}

// 取消 ctx 打断一个批次时，没轮到（或还没发出去）的已领取消息回到 pending——不是 unknown。
//
// The stop is not a failure and not a loss. How many messages got out before the cancel landed
// depends on how many workers were in flight, so the test counts rather than assumes: whatever was
// not sent is back in the queue, nothing is left claimed, and the restart finishes the campaign
// without sending anyone a second copy.
func TestCancelledBatchReleasesClaimedMessages(t *testing.T) {
	f := newFixture(t, 20)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}

	// The batch stops on the first send: the sender cancels as it is called.
	f.fake.OnSend = func(int) { cancel() }
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatalf("a cancelled batch = %v, want a stop, not an error", err)
	}
	f.fake.OnSend = nil

	if !result.Retry {
		t.Fatalf("result = %+v, want Retry", result)
	}
	sent := f.fake.SentCount()
	if sent != result.Sent {
		t.Fatalf("%d messages reached the sender but %d were recorded", sent, result.Sent)
	}
	if sent == 0 || sent > 20 {
		t.Fatalf("sent = %d", sent)
	}
	claimed, unknown, pending := claimedCount(t, f.db)
	if claimed != 0 {
		t.Fatalf("claimed = %d after a cancelled batch, want 0", claimed)
	}
	if unknown != 0 {
		t.Fatalf("unknown = %d, want 0: nothing was left in doubt", unknown)
	}
	if pending != 20-sent {
		t.Fatalf("pending = %d, want %d (everything that was not sent is back in the queue)", pending, 20-sent)
	}

	// Everything still goes out afterwards, and nobody gets a second copy.
	if again := f.drain(t); again.Sent != 20-sent {
		t.Fatalf("after the restart = %+v, want %d more", again, 20-sent)
	}
	if total := f.fake.SentCount(); total != 20 {
		t.Fatalf("total sent = %d, want 20", total)
	}
	for email, count := range f.fake.SentPerRecipient() {
		if count != 1 {
			t.Errorf("%s received %d copies", email, count)
		}
	}
}

// 关停之后库里没有 claimed：它们要么是 sent，要么回到 pending。
func TestGracefulShutdownLeavesNothingClaimed(t *testing.T) {
	f := newFixture(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// The engine runs in the background and is cancelled mid-flight.
	done := make(chan error, 1)
	f.fake.OnSend = func(n int) {
		if n == 3 {
			cancel()
		}
	}
	go func() { done <- f.engine.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the engine did not stop within ten seconds of cancellation")
	}
	f.fake.OnSend = nil

	claimed, unknown, pending := claimedCount(t, f.db)
	if claimed != 0 {
		t.Fatalf("claimed = %d, want 0", claimed)
	}
	if unknown != 0 {
		t.Fatalf("unknown = %d, want 0 (a graceful stop must not lose messages)", unknown)
	}
	sentBefore := f.fake.SentCount()
	if sentBefore+pending != 10 {
		t.Fatalf("sent %d + pending %d != 10", sentBefore, pending)
	}

	// A second run finishes the campaign.
	if result := f.drain(t); result.Sent != 10-sentBefore {
		t.Fatalf("second run = %+v, want %d more", result, 10-sentBefore)
	}
	if sent := f.fake.SentCount(); sent != 10 {
		t.Fatalf("total sent = %d, want 10", sent)
	}
}

// 启动时 RecoverClaimed 把上次崩溃留下的 claimed 变成 unknown，且那些行不会再被领取（D14）。
func TestRecoverClaimedOnStartupNeverResends(t *testing.T) {
	f := newFixture(t, 6)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// Claim three without writing anything back, as a crash would leave them.
	claimed, err := f.db.ClaimMessages(ctx, 3)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("claimed = %d %v", len(claimed), err)
	}

	// A fresh process starts: recover first, then send.
	if recovered, err := f.db.RecoverClaimed(ctx); err != nil || recovered != 3 {
		t.Fatalf("recovered = %d %v", recovered, err)
	}
	if result := f.drain(t); result.Sent != 3 {
		t.Fatalf("after recovery = %+v, want the three that were still pending", result)
	}
	if f.fake.SentCount() != 3 {
		t.Fatalf("sent = %d, want 3", f.fake.SentCount())
	}
	claimedNow, unknown, _ := claimedCount(t, f.db)
	if claimedNow != 0 || unknown != 3 {
		t.Fatalf("claimed = %d, unknown = %d, want 0 and 3", claimedNow, unknown)
	}
}

// claimedCount 也要能在别的库里用：这里确认它读的就是 messages 表。
func TestClaimedCountReadsTheMessagesTable(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "nibwire.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	claimed, unknown, pending := claimedCount(t, db)
	if claimed != 0 || unknown != 0 || pending != 0 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d", claimed, unknown, pending)
	}
}
