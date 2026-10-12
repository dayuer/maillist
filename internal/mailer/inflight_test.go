package mailer

import (
	"context"
	"testing"

	"nibwire/internal/core/model"
)

// 关停落在 SES 请求在途时，那一封不能回到 pending。
//
// C1 from the phase 1-3 audit. A request that was in flight when the process was signalled is
// indistinguishable from one SES accepted and answered too late: releasing it means the restart
// sends the same mail again, which is exactly what D14 ("prefer losing to duplicating") forbids. The
// invariant is that every message ends up sent or unknown — never pending, with SES having seen it.
//
// What the test pins is that agreement, not how many requests were in the air when the shutdown
// landed. That number is a race: the dispatcher and the cancellation are two goroutines, and on a
// loaded machine a third — even a fourth — hand-out wins it (CI saw three, where a laptop sees two).
// Both outcomes are correct, and the assertions below hold either way; asserting the number was a
// flake, not a finding (phase-9 follow-up, issue #67).
//
// The fake is in its WithContextCancellation mode, so a request that arrives to a cancelled context
// is recorded and *then* reported as unanswered — the case being guarded. Under the detached send
// context that never fires today, which is the fix itself: if a send were run on the batch's context,
// this fake would report the cancellation for mail SES already had, and the assertions below would
// fail with exactly the arithmetic they exist to catch.
func TestAnInFlightSendIsNeverReleasedToPending(t *testing.T) {
	const messages = 4
	f := newFixtureWith(t, messages, WithContextCancellation())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}

	// The shutdown lands while the second request is in flight: the callback runs as that send
	// arrives, before it is answered.
	f.fake.OnSend = func(sent int) {
		if sent == 1 {
			cancel()
		}
	}
	// The cancellation is issued from inside the second attempt, which is recorded before it is
	// answered, and the first attempt is recorded by then: whatever else happens, SES saw two.
	if _, err := f.engine.SendBatch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	f.fake.OnSend = nil
	// From here the assertions read with a live context: the batch's is cancelled, by design.
	check := context.Background()

	accepted := f.fake.SentCount()
	if accepted < 2 || accepted > messages {
		t.Fatalf("the fake accepted %d sends, want between 2 and %d", accepted, messages)
	}
	// What SES accepted and what the database says must agree: every message the fake recorded is one
	// the database has to call sent, the in-flight one included.
	var sent, pending, unknown int
	for status, target := range map[string]*int{
		string(model.MessageSent): &sent, "pending": &pending, "unknown": &unknown,
	} {
		if err := f.db.Row(check, "SELECT count(*) FROM messages WHERE status = ?", status).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if sent != accepted {
		t.Fatalf("SES accepted %d messages and the database has %d sent: the difference goes out again",
			accepted, sent)
	}
	if unknown != 0 {
		t.Fatalf("unknown = %d, want 0: nothing here was left unresolved", unknown)
	}
	if pending != messages-accepted {
		t.Fatalf("pending = %d, want %d: only the messages that were never sent", pending, messages-accepted)
	}
	var claimed int
	if err := f.db.Row(check, "SELECT count(*) FROM messages WHERE status = 'claimed'").Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("claimed = %d, want 0", claimed)
	}
}

// 请求在途时被强杀（写入没跑完）的那一封留在 claimed，恢复时转 unknown——绝不是 pending。
func TestAnInterruptedSendBecomesUnknownNotPending(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// A crash after the claim and before the write-back is exactly a claimed row with no result.
	claimed, err := f.db.ClaimMessages(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d %v", len(claimed), err)
	}
	// The next start recovers before it sends anything.
	if recovered, err := f.db.RecoverClaimed(ctx); err != nil || recovered != 1 {
		t.Fatalf("recovered = %d %v", recovered, err)
	}
	var status string
	if err := f.db.Row(ctx, "SELECT status FROM messages WHERE id = ?", claimed[0].ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(model.MessageUnknown) {
		t.Fatalf("status = %s, want unknown: a message SES may have taken must not be sent again", status)
	}
}
