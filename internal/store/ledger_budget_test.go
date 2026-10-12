package store

import (
	"context"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// 预留与建消息同事务：预算花不出去时一条消息也不写。
//
// ST-I1 from the phase 1-3 audit: Reserve and InsertMessages were separate transactions, so a
// process that reserved and then died had spent the day's allowance on mail that does not exist, and
// nothing gave it back.
func TestReserveAndInsertWritesNothingWhenTheBudgetIsSpent(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	day := Day(time.Now().UTC())

	messages := campaignMessages(t, db, campaign, 3)
	fits, inserted, err := db.ReserveAndInsert(ctx, day, "", 3, 2, 0, messages)
	if err != nil {
		t.Fatal(err)
	}
	if fits || inserted != 0 {
		t.Fatalf("fits = %v, inserted = %d, want neither", fits, inserted)
	}
	reserved, err := db.Reserved(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("reserved = %d after a refused step, want 0", reserved)
	}
	recipients, lastChance, err := db.CountCampaignMessages(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipients+lastChance != 0 {
		t.Fatalf("%d messages were written for a step that did not fit", recipients+lastChance)
	}
}

// 已经存在的行不计费：重跑一次 job 不会把当天的预算算两遍。
func TestReserveAndInsertChargesOnlyForWhatItWrote(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	day := Day(time.Now().UTC())

	messages := campaignMessages(t, db, campaign, 3)
	fits, inserted, err := db.ReserveAndInsert(ctx, day, "", 3, 100, 0, messages)
	if err != nil || !fits || inserted != 3 {
		t.Fatalf("first pass: fits = %v, inserted = %d, %v", fits, inserted, err)
	}
	if reserved, err := db.Reserved(ctx, day, ""); err != nil || reserved != 3 {
		t.Fatalf("reserved = %d %v, want 3", reserved, err)
	}

	// The same rows again — a job retried after inserting and before finishing. New ids, same
	// (campaign, contact) pairs, so the unique index turns them away and nothing is charged.
	retry := campaignMessagesFor(t, db, campaign, messages)
	fits, inserted, err = db.ReserveAndInsert(ctx, day, "", 3, 100, 0, retry)
	if err != nil || !fits {
		t.Fatalf("retry: fits = %v %v", fits, err)
	}
	if inserted != 0 {
		t.Fatalf("retry inserted %d, want 0", inserted)
	}
	if reserved, err := db.Reserved(ctx, day, ""); err != nil || reserved != 3 {
		t.Fatalf("reserved = %d after a retry, want 3: the day must not be charged twice", reserved)
	}
}

// 多于一块的步骤由 ReserveAndInsert 一次写完，账本上正好是新增的那些（#38）。
//
// The leak this replaces: the first chunk went in with the reservation and the rest was the caller's
// job, so a process that died in between had charged the day for messages that were never written —
// and the ledger, a count per day and pool with nothing saying which messages it paid for, cannot be
// reconciled afterwards. The assertion that would have failed then is the second one: inserted is the
// whole step, not one chunk of it.
func TestAStepWritesEveryChunkAndIsChargedExactlyOnce(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	day := Day(time.Now().UTC())

	const messages = 2500 // more than two chunks of InsertChunkSize
	step := campaignMessages(t, db, campaign, messages)
	fits, inserted, err := db.ReserveAndInsert(ctx, day, "", len(step), 100_000, 0, step)
	if err != nil {
		t.Fatal(err)
	}
	if !fits || inserted != messages {
		t.Fatalf("fits = %v, inserted = %d, want %d written by the reservation itself",
			fits, inserted, messages)
	}
	recipients, _, err := db.CountCampaignMessages(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recipients != messages {
		t.Fatalf("%d messages in the database, want %d", recipients, messages)
	}
	if reserved, err := db.Reserved(ctx, day, ""); err != nil || reserved != messages {
		t.Fatalf("reserved = %d %v, want exactly the step", reserved, err)
	}

	// The same step again — new ids, the same addresses — writes nothing and charges nothing: the
	// charge follows the rows, not the attempt.
	fits, inserted, err = db.ReserveAndInsert(ctx, day, "", messages, 100_000, 0,
		campaignMessagesFor(t, db, campaign, step))
	if err != nil || !fits || inserted != 0 {
		t.Fatalf("second pass: fits = %v, inserted = %d, %v", fits, inserted, err)
	}
	if reserved, err := db.Reserved(ctx, day, ""); err != nil || reserved != messages {
		t.Fatalf("reserved = %d after the same step twice, want %d", reserved, messages)
	}
}

// 一步的插入不能把单写入方占死：一边在写 20 万封，另一边记一条单行写入要在 200 ms 内完成（#61，设计 §5.2
// 第 2 条）。
//
// The shape this replaces was one transaction for the whole step, sized by the audience: at about
// sixty-one thousand rows a second a million messages held the writer for sixteen seconds, and
// everything a reader does — an unsubscribe, an open, a click, an SNS callback — queued behind it.
// The CLI's own busy_timeout is ten seconds, so its writes failed outright.
func TestAStepBeingWrittenDoesNotBlockASingleRow(t *testing.T) {
	if testing.Short() {
		t.Skip("writes two hundred thousand messages")
	}
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	step := campaignMessages(t, db, campaign, 200_000)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The error is not the subject: the rows either go in or the count below says they did not,
		// and a t.Fatal from this goroutine would be from the wrong one.
		_, _, _ = db.ReserveAndInsert(ctx, Day(time.Now().UTC()), "", len(step), 1_000_000, 0, step)
	}()

	// Wait until the step is being written. Its first chunk is committed with the reservation, so
	// rows appearing is the signal — and a read, which WAL does not block on.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var written int
		if err := db.Row(ctx, "SELECT count(*) FROM messages WHERE campaign_id = ?",
			campaign.ID).Scan(&written); err != nil {
			t.Fatal(err)
		}
		if written > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the step never started writing")
		}
		time.Sleep(time.Millisecond)
	}

	// Now, while it is still writing the rest, the single row an unsubscribe writes.
	start := time.Now()
	if err := db.AddSuppression(ctx, "left@example.com", model.SuppressManual, "unsubscribed"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	<-done
	if elapsed > 200*time.Millisecond {
		t.Fatalf("a single write took %s while a step was being inserted", elapsed)
	}
}

// 退还要把当天的额度还回去，且不会退成负数。
func TestRefund(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	day := Day(time.Now().UTC())

	if fits, err := db.Reserve(ctx, day, "", 10, 100, 0); err != nil || !fits {
		t.Fatalf("reserve = %v %v", fits, err)
	}
	if err := db.Refund(ctx, day, "", 4); err != nil {
		t.Fatal(err)
	}
	if reserved, err := db.Reserved(ctx, day, ""); err != nil || reserved != 6 {
		t.Fatalf("reserved = %d %v, want 6", reserved, err)
	}
	// A double refund cannot drive the pool negative.
	if err := db.Refund(ctx, day, "", 100); err != nil {
		t.Fatal(err)
	}
	if reserved, err := db.Reserved(ctx, day, ""); err != nil || reserved != 0 {
		t.Fatalf("reserved = %d %v, want 0", reserved, err)
	}
}

// campaignMessagesFor rebuilds the same recipients with fresh ids, which is what a retry does.
func campaignMessagesFor(t *testing.T, db *DB, campaign model.Campaign,
	original []model.Message) []model.Message {
	t.Helper()
	out := make([]model.Message, 0, len(original))
	for _, message := range original {
		out = append(out, model.Message{
			ID: ids.New(), Kind: message.Kind, CampaignID: message.CampaignID,
			NewsletterID: message.NewsletterID, ContactID: message.ContactID, Email: message.Email,
			Provider: message.Provider, Status: model.MessagePending, CreatedAt: time.Now().UTC(),
		})
	}
	return out
}
