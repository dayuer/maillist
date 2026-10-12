package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/store"
)

const testAppURL = "https://mail.example.com"

// fixture is a whole send ready to go: a newsletter, some contacts and a draft campaign.
type fixture struct {
	db         *store.DB
	engine     *Engine
	fake       *Fake
	signer     *sign.Signer
	newsletter model.Newsletter
	campaign   model.Campaign
	contacts   []model.Contact
}

func newFixture(t *testing.T, contacts int) *fixture {
	t.Helper()
	return newFixtureWith(t, contacts)
}

// newFixtureWith is newFixture with options for the fake, for the tests that need it to behave in a
// particular way (see WithContextCancellation).
func newFixtureWith(t *testing.T, contacts int, options ...FakeOption) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	signer, err := sign.New("test-session-secret-with-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "field-notes", Name: "Field notes",
		FromName: "Jane Doe", FromEmail: "jane@example.net", PostalAddress: "1 Main St, Paris",
		ArchiveEnabled: true, WelcomeEnabled: true,
		Welcome: model.Content{Subject: "Welcome to Field notes", Markdown: "Hello"},
	}
	if err := db.CreateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}

	audience := make([]model.Contact, 0, contacts)
	for i := 0; i < contacts; i++ {
		contact := model.Contact{
			ID: ids.New(), NewsletterID: newsletter.ID,
			Email: fmt.Sprintf("reader%d@example.com", i), Status: model.ContactActive,
			CreatedAt: time.Now().UTC(),
		}
		if err := db.CreateContact(ctx, contact); err != nil {
			t.Fatal(err)
		}
		audience = append(audience, contact)
	}

	campaign := model.NewCampaign(newsletter.ID, "Issue 12", time.Now().UTC())
	campaign.PreviewText = "This week"
	campaign.Body = "# Hello\n\nRead [this](https://example.com)."
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}

	fake := NewFake(options...)
	engine := NewEngine(db, fake, signer, Config{
		AppURL: testAppURL, SendingOn: true, SendRatePerSec: 1000,
		Limits: model.Limits{MaxEmailBytes: 100_000, BounceRate: 0.02, ComplaintRate: 0.0008,
			MaxSendsPerDay: 50_000, WelcomeEmailsPer10Minute: 100},
		PollInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(&testLogWriter{t}, nil)))
	t.Cleanup(engine.Close)

	return &fixture{db: db, engine: engine, fake: fake, signer: signer,
		newsletter: newsletter, campaign: campaign, contacts: audience}
}

type testLogWriter struct{ t *testing.T }

func (w *testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// drain sends everything that is queued.
func (f *fixture) drain(t *testing.T) BatchResult {
	t.Helper()
	total := BatchResult{}
	for {
		result, err := f.engine.SendBatch(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		total.Sent += result.Sent
		total.Failed += result.Failed
		total.Suppressed += result.Suppressed
		total.Skipped += result.Skipped
		if result.Done() == 0 || result.Retry {
			return total
		}
	}
}

// The whole path: queue a campaign, send it, and every message lands.
func TestQueueThenSendDeliversToEveryone(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()

	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.Recipients != 3 {
		t.Fatalf("queued = %+v", queued)
	}
	loaded, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != model.CampaignSending || loaded.StartedAt == nil {
		t.Fatalf("campaign = %+v", loaded)
	}

	result := f.drain(t)
	if result.Sent != 3 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if f.fake.SentCount() != 3 {
		t.Fatalf("fake sent = %d", f.fake.SentCount())
	}
	for _, sent := range f.fake.Sent() {
		if sent.Tags["kind"] != string(model.KindCampaign) || sent.Tags["message_id"] == "" {
			t.Errorf("tags = %v", sent.Tags)
		}
		if !strings.Contains(sent.Email.HTML, "/c/") {
			t.Errorf("a campaign email is tracked: %s", sent.Email.HTML)
		}
		if sent.Email.Headers["Feedback-ID"] == "" {
			t.Error("no Feedback-ID")
		}
		if sent.Email.Headers["List-Unsubscribe"] == "" {
			t.Error("no List-Unsubscribe")
		}
	}

	// The campaign settles itself, and the counters follow.
	settled, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.CampaignSent || settled.SentAt == nil {
		t.Fatalf("campaign did not settle: %+v", settled)
	}
	stats, _, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil || stats.Sent != 3 {
		t.Fatalf("stats = %+v %v", stats, err)
	}
	for _, contact := range f.contacts {
		after, err := f.db.Contact(ctx, contact.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Sent != 1 || after.LastSentAt == nil || after.IgnoredIssues != 1 {
			t.Errorf("contact %s = %+v", contact.Email, after)
		}
	}
}

// rate 10/s: 50 sends take 4.5–5.5 s (the plan's acceptance number).
func TestBucketHoldsTheSendRate(t *testing.T) {
	if testing.Short() {
		t.Skip("takes five seconds")
	}
	bucket := NewBucket(10)
	defer bucket.Close()
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := bucket.Take(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 4500*time.Millisecond || elapsed > 5500*time.Millisecond {
		t.Fatalf("50 sends at 10/s took %s", elapsed)
	}
}

func TestBucketStopsOnCancellation(t *testing.T) {
	bucket := NewBucket(1)
	defer bucket.Close()
	// Drain the first slot, then cancel while waiting for the next.
	if err := bucket.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bucket.Take(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A campaign over the bounce limit pauses instead of sending the rest (design §6.2).
func TestSendBatchPausesAboveTheBounceLimit(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// 100 sent and 3 bounced is over the 2% line; the counters come from the SES events in phase 3.
	if err := f.db.AddCampaignStats(ctx, f.campaign.ID, store.CampaignCounters{Sent: 100, Bounced: 3}); err != nil {
		t.Fatal(err)
	}
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.fake.SentCount() != 0 {
		t.Fatalf("sent %d messages past the alarm", f.fake.SentCount())
	}
	_ = result

	loaded, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != model.CampaignPaused {
		t.Fatalf("campaign = %s, want paused", loaded.Status)
	}
	// The messages went back to the queue rather than being burned.
	recipients, _, err := f.db.CountCampaignMessages(ctx, f.campaign.ID)
	if err != nil || recipients != 10 {
		t.Fatalf("recipients = %d %v", recipients, err)
	}
	pending, err := f.db.CountUnfinishedCampaignMessages(ctx, f.campaign.ID)
	if err != nil || pending != 10 {
		t.Fatalf("pending = %d %v, want all 10", pending, err)
	}
}

// kill -9 between the SES call and the write-back: those messages become unknown and are never sent
// again (D14).
func TestRecoverClaimedNeverResends(t *testing.T) {
	f := newFixture(t, 4)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// Send one batch that delivers a single message, then claim the rest without writing back —
	// exactly what a crash between the SES call and the write-back leaves behind.
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 4 {
		t.Fatalf("first batch = %+v", result)
	}
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err == nil {
		t.Fatal("a sent campaign must not queue again")
	}

	// Rebuild the crash scene: a campaign that is still sending, with three rows claimed and no
	// status written back.
	second := model.NewCampaign(f.newsletter.ID, "Issue 13", time.Now().UTC())
	second.Status = model.CampaignSending
	if err := f.db.CreateCampaign(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.InsertMessages(ctx, campaignMessagesFor(t, f, second, 3)); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.db.ClaimMessages(ctx, 3)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("claimed = %d %v", len(claimed), err)
	}
	sentBefore := f.fake.SentCount()

	recovered, err := f.db.RecoverClaimed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 3 {
		t.Fatalf("recovered = %d, want 3", recovered)
	}
	// A fresh engine over the same file sends nothing more.
	engine := NewEngine(f.db, f.fake, f.signer, f.engine.cfg, nil)
	defer engine.Close()
	result, err = engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 0 || f.fake.SentCount() != sentBefore {
		t.Fatalf("recovered messages were sent again: %+v", result)
	}
	for _, message := range claimed {
		after, err := f.db.Message(ctx, message.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != model.MessageUnknown {
			t.Errorf("message %s = %s, want unknown", message.ID, after.Status)
		}
	}
}

func campaignMessagesFor(t *testing.T, f *fixture, campaign model.Campaign, n int) []model.Message {
	t.Helper()
	messages := make([]model.Message, 0, n)
	for i := 0; i < n; i++ {
		messages = append(messages, model.Message{
			ID: ids.New(), Kind: model.KindCampaign, CampaignID: campaign.ID,
			NewsletterID: f.newsletter.ID, ContactID: ids.New(),
			Email:  fmt.Sprintf("extra%d@example.com", i),
			Status: model.MessagePending, CreatedAt: time.Now().UTC(),
		})
	}
	return messages
}

// --limit sends the next most engaged slice and hits NextStepLimit.
func TestQueueStepUsesNextStepLimit(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()

	// A first step of 4: the audience is capped, so the limit counts recipients.
	staged := f.campaign
	staged.AudienceLimit = 4
	if err := f.db.UpdateCampaign(ctx, staged); err != nil {
		t.Fatal(err)
	}
	queued, err := f.engine.Queue(ctx, f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Recipients != 4 {
		t.Fatalf("first step = %+v", queued)
	}
	if result := f.drain(t); result.Sent != 4 {
		t.Fatalf("first send = %+v", result)
	}

	// The next step reaches five more: sentSoFar(4) + additional(5).
	step, err := f.engine.QueueStep(ctx, f.campaign.ID, 5, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !step.Queued || step.Added != 5 || step.Recipients != 9 {
		t.Fatalf("step = %+v", step)
	}
	if result := f.drain(t); result.Sent != 5 {
		t.Fatalf("second send = %+v", result)
	}
	if got := f.fake.SentCount(); got != 9 {
		t.Fatalf("total sent = %d, want 9", got)
	}

	// A step wide enough to reach the last contact adds exactly that one.
	step, err = f.engine.QueueStep(ctx, f.campaign.ID, 50, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if !step.Queued || step.Added != 1 {
		t.Fatalf("final step = %+v", step)
	}
	if result := f.drain(t); result.Sent != 1 {
		t.Fatalf("final send = %+v", result)
	}

	// Now nothing is left to add.
	step, err = f.engine.QueueStep(ctx, f.campaign.ID, 50, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if step.Queued || step.Reason != "Everyone has received it already" {
		t.Fatalf("step past the end = %+v", step)
	}
}

// A contact who unsubscribed between the audience walk and the send is skipped, not mailed.
func TestSuppressedContactsAreSkipped(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AddSuppression(ctx, f.contacts[0].Email, model.SuppressBounce, "ses"); err != nil {
		t.Fatal(err)
	}
	result := f.drain(t)
	if result.Sent != 2 || result.Suppressed != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// The campaign can be paused between queueing and sending; the message stays for later.
func TestPausedCampaignIsNotSent(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignPaused); err != nil {
		t.Fatal(err)
	}
	// A paused campaign's messages are not even claimable, so the batch is empty rather than a
	// claim-and-release cycle.
	result := f.drain(t)
	if result.Sent != 0 || result.Done() != 0 {
		t.Fatalf("result = %+v", result)
	}
	if f.fake.SentCount() != 0 {
		t.Fatal("a paused campaign was sent")
	}
	if pending, err := f.db.CountUnfinishedCampaignMessages(ctx, f.campaign.ID); err != nil || pending != 3 {
		t.Fatalf("pending = %d %v", pending, err)
	}

	// Resuming sends them.
	if _, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignPaused}, model.CampaignSending); err != nil {
		t.Fatal(err)
	}
	if result := f.drain(t); result.Sent != 3 {
		t.Fatalf("resumed send = %+v", result)
	}
}

// A retryable SES error leaves the campaign alone; a rejected message counts as a failure.
func TestSESErrorsAreClassified(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}

	// Every address is throttled, so the first claimed message stops the batch without burning it —
	// the claim order is the database's, not the audience's.
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "ThrottlingException", "Maximum sending rate exceeded")
	}
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 0 || result.Failed != 0 || !result.Retry {
		t.Fatalf("throttled batch = %+v", result)
	}
	if pending, err := f.db.CountUnfinishedCampaignMessages(ctx, f.campaign.ID); err != nil || pending != 2 {
		t.Fatalf("pending = %d %v, want both back in the queue", pending, err)
	}

	// Now a hard rejection: that message fails, the next one still goes out.
	f.fake.ClearFailures()
	f.fake.FailWith(f.contacts[0].Email, "MessageRejected", "Message contains a virus")
	result = f.drain(t)
	if result.Failed != 1 || result.Sent != 1 {
		t.Fatalf("mixed batch = %+v", result)
	}
	failed := messagesWithStatus(t, f, model.MessageFailed)
	if len(failed) != 1 || !strings.Contains(failed[0].Error, "virus") {
		t.Fatalf("failed = %+v", failed)
	}
}

// Five failures in a row pause the campaign (upstream MAX_CONSECUTIVE_FAILURES).
func TestConsecutiveFailuresPauseTheCampaign(t *testing.T) {
	f := newFixture(t, 6)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "MessageRejected", "Message contains a virus")
	}
	result := f.drain(t)
	if result.Failed != rules.MaxConsecutiveFailures {
		t.Fatalf("failed = %d, want %d", result.Failed, rules.MaxConsecutiveFailures)
	}
	loaded, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != model.CampaignPaused {
		t.Fatalf("campaign = %s, want paused", loaded.Status)
	}
}

// A test email goes to the sentinel contact: the campaign report never sees it (design §6.2).
func TestTestEmailUsesTheSentinelContact(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	result, err := f.engine.Test(ctx, f.campaign.ID, "tester@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if result.SESMessageID == "" {
		t.Fatal("no SES message id")
	}
	sent := f.fake.Sent()
	if len(sent) != 1 || !strings.HasPrefix(sent[0].Email.Subject, "[Test] ") {
		t.Fatalf("sent = %+v", sent)
	}
	message, err := f.db.Message(ctx, result.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if message.ContactID != rules.TestContactID || message.Status != model.MessageSent {
		t.Fatalf("message = %+v", message)
	}
	// The campaign's own counters are untouched.
	stats, _, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil || stats.Sent != 0 {
		t.Fatalf("stats = %+v %v", stats, err)
	}
	loaded, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TestSentAt == nil {
		t.Fatal("testSentAt not recorded")
	}
}

// A campaign that cannot pass the pre-send checks never starts.
func TestQueueRefusesAnIncompleteCampaign(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	blank := f.campaign
	blank.Subject = ""
	if err := f.db.UpdateCampaign(ctx, blank); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err == nil ||
		!strings.Contains(err.Error(), "Add a subject") {
		t.Fatalf("err = %v", err)
	}
}

// Sending off is one thing no credential can override (design D7).
func TestQueueAndSendRefuseWhenSendingIsOff(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	f.engine.cfg.SendingOn = false
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err == nil {
		t.Fatal("queue while off")
	}
	if _, err := f.engine.SendBatch(ctx, nil); err == nil {
		t.Fatal("send while off")
	}
	if _, err := f.engine.Test(ctx, f.campaign.ID, "tester@example.com"); err == nil {
		t.Fatal("test while off")
	}
}

func messagesWithStatus(t *testing.T, f *fixture, status model.MessageStatus) []model.Message {
	t.Helper()
	rows, err := f.db.Rows(context.Background(),
		"SELECT id FROM messages WHERE campaign_id = ? AND status = ?", f.campaign.ID, string(status))
	if err != nil {
		t.Fatal(err)
	}
	var idsFound []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		idsFound = append(idsFound, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var out []model.Message
	for _, id := range idsFound {
		message, err := f.db.Message(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, message)
	}
	return out
}
