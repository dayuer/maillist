package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// 新建草稿的 ArchiveListed 必须是 true：上游 schema 的默认值，Go 零值不是。
func TestNewCampaignIsListedByDefault(t *testing.T) {
	if !model.NewCampaign(ids.New(), "Draft", time.Now().UTC()).ArchiveListed {
		t.Fatal("model.NewCampaign must list the issue on the archive, like upstream's default")
	}
}

// 建表列本身也要默认 1：有人绕过 model.NewCampaign 手搓 model.Campaign 直接 INSERT 时，
// 省略该列也要落成 listed。
func TestArchiveListedColumnDefaultsToListed(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	id := ids.New()
	now := formatTime(time.Now())
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO campaigns (id, newsletter_id, subject, created_at, updated_at) VALUES (?,?,?,?,?)`,
			id, newsletter.ID, "Draft", now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Campaign(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ArchiveListed {
		t.Fatal("a row that omits archive_listed must be listed")
	}
}

// 结算是一条语句："没有任何未完成的东西"与"置为 sent"分开做，中间就有窗口——另一个进程 release 或
// claim 一条消息，就留下 sent 底下还有 pending 的 campaign，而那些消息永远不会被领取，因为 sent 的
// campaign 不再 sending（#35，阶段 1-3 审计的 C2）。
func TestSettlingRefusesWhileAnythingIsUnfinished(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	messages := campaignMessages(t, db, campaign, 2)
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	// Waiting is not finished.
	if moved, err := db.SettleCampaign(ctx, campaign.ID); err != nil || moved {
		t.Fatalf("moved = %v %v, want a campaign with a waiting message to stay sending", moved, err)
	}
	// Neither is claimed: in flight is not done.
	claimed, err := db.ClaimMessages(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d %v", len(claimed), err)
	}
	if moved, err := db.SettleCampaign(ctx, campaign.ID); err != nil || moved {
		t.Fatalf("moved = %v %v, want a claimed message to count as unfinished", moved, err)
	}

	// With every message in its final state it settles, and settling again does nothing.
	for _, message := range messages {
		if err := db.MarkMessageSent(ctx, message.ID, "ses-"+message.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if moved, err := db.SettleCampaign(ctx, campaign.ID); err != nil || !moved {
		t.Fatalf("moved = %v %v, want the campaign to settle", moved, err)
	}
	loaded, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != model.CampaignSent {
		t.Fatalf("status = %s, want sent", loaded.Status)
	}
	if moved, err := db.SettleCampaign(ctx, campaign.ID); err != nil || moved {
		t.Fatalf("moved = %v, want nothing to happen to a campaign that is already sent", moved)
	}
}

// 编辑是读—改—写：写回的列里不能再带上状态机拥有的那些，否则并发做过的状态迁移会被安静地回滚。
//
// The hazard is an interleaving, not parallel goroutines, so the test is the interleaving: the editor
// reads the row, the sender finishes the campaign and a reader's page view lands, and then the editor
// writes the copy it has been holding. What the sender and the reader did has to survive — the
// database is not the editor's to overwrite, it is only the editor's to partly change.
func TestEditingACampaignCannotUndoAConcurrentChange(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue 12", time.Now().UTC())
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	// The editor's copy, read now.
	open, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The campaign goes out while the operator is typing.
	if moved, err := db.SetCampaignStatus(ctx, campaign.ID,
		[]model.CampaignStatus{model.CampaignDraft}, model.CampaignSending); err != nil || !moved {
		t.Fatalf("start sending: %v", err)
	}
	if moved, err := db.SetCampaignStatus(ctx, campaign.ID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignSent); err != nil || !moved {
		t.Fatalf("finish: %v", err)
	}
	if err := db.AddWebViews(ctx, campaign.ID, 7); err != nil {
		t.Fatal(err)
	}

	// The editor writes back.
	open.Subject = "Issue 12 (renamed)"
	if err := db.UpdateCampaign(ctx, open); err != nil {
		t.Fatal(err)
	}

	after, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Subject != "Issue 12 (renamed)" {
		t.Errorf("subject = %q, want the edit to have happened", after.Subject)
	}
	if after.Status != model.CampaignSent {
		t.Errorf("status = %s, want sent: the edit rolled back a state change it never saw",
			after.Status)
	}
	if after.WebViews != 7 {
		t.Errorf("web views = %d, want 7: the edit overwrote a count it never saw", after.WebViews)
	}
}

// 端到端：走 model.NewCampaign 建草稿再读回来，仍然 listed。
func TestCreatedCampaignRoundTripsAsListed(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Draft", time.Now().UTC())
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ArchiveListed {
		t.Fatal("a new draft must be listed on the archive")
	}
	// Unlisting stays possible.
	loaded.ArchiveListed = false
	if err := db.UpdateCampaign(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ArchiveListed {
		t.Fatal("an explicit unlist must stick")
	}
}

func TestCampaignRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue 12", time.Now().UTC())
	campaign.PreviewText = "This week"
	campaign.Body = "# Hello"
	campaign.AudienceLimit = 500
	scheduled := time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)
	campaign.ScheduledAt = &scheduled
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}

	got, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Issue 12" || got.PreviewText != "This week" || got.Body != "# Hello" ||
		got.Format != model.FormatMarkdown || got.Status != model.CampaignDraft ||
		got.AudienceLimit != 500 || got.ScheduledAt == nil || !got.ScheduledAt.Equal(scheduled) {
		t.Fatalf("got %+v", got)
	}
	if _, err := db.Campaign(ctx, ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if list, err := db.Campaigns(ctx, newsletter.ID, 10, 0); err != nil || len(list) != 1 {
		t.Fatalf("list = %d %v", len(list), err)
	}
}

// 只有 draft/scheduled/paused 能进入发送（设计 D7 第 3 条）。
func TestSetCampaignStatusOnlyFromQueueableStatuses(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue", time.Now().UTC())
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}

	queueable := []model.CampaignStatus{model.CampaignDraft, model.CampaignScheduled, model.CampaignPaused}
	moved, err := db.SetCampaignStatus(ctx, campaign.ID, queueable, model.CampaignSending)
	if err != nil || !moved {
		t.Fatalf("moved = %v %v", moved, err)
	}
	// Already sending: a second start is not a move.
	again, err := db.SetCampaignStatus(ctx, campaign.ID, queueable, model.CampaignSending)
	if err != nil || again {
		t.Fatalf("again = %v %v", again, err)
	}
	// A sent campaign never goes back to sending.
	if _, err := db.SetCampaignStatus(ctx, campaign.ID, []model.CampaignStatus{model.CampaignSending}, model.CampaignSent); err != nil {
		t.Fatal(err)
	}
	restarted, err := db.SetCampaignStatus(ctx, campaign.ID, queueable, model.CampaignSending)
	if err != nil || restarted {
		t.Fatalf("a sent campaign must not restart: %v %v", restarted, err)
	}
}

func TestCampaignCounters(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue", time.Now().UTC())
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCampaignStats(ctx, campaign.ID, CampaignCounters{Sent: 100, Bounced: 3}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCampaignStats(ctx, campaign.ID, CampaignCounters{Sent: 50, Opened: 10}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddLastChanceStats(ctx, campaign.ID, LastChanceCounters{Sent: 20, Bounced: 1}); err != nil {
		t.Fatal(err)
	}

	stats, lastChance, err := db.CampaignCountersOf(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sent != 150 || stats.Bounced != 3 || stats.Opened != 10 {
		t.Fatalf("stats = %+v", stats)
	}
	if lastChance.Sent != 20 || lastChance.Bounced != 1 {
		t.Fatalf("last chance = %+v", lastChance)
	}
	// The pause decision counts both, so last-chance bounces reach the limit too.
	pause := stats.PauseStats(lastChance)
	if pause.Sent != 170 || pause.Bounced != 4 {
		t.Fatalf("pause stats = %+v", pause)
	}
}

// 分步发送保留最早一次的时间（上游 $min: {sentAt}）。
func TestSetCampaignSentAtKeepsTheEarliest(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue", time.Now().UTC())
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	later := first.Add(time.Hour)
	if err := db.SetCampaignSentAt(ctx, campaign.ID, later); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCampaignSentAt(ctx, campaign.ID, first); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.Campaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SentAt == nil || !loaded.SentAt.Equal(first) {
		t.Fatalf("sentAt = %v, want %s", loaded.SentAt, first)
	}
}
