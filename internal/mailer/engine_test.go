package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/core/sign"
	"nibwire/internal/store"
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
	// logs is what the engine said, which is where the things only an operator sees end up.
	logs *logCapture
}

func newFixture(t *testing.T, contacts int) *fixture {
	t.Helper()
	return newFixtureWith(t, contacts)
}

// newFixtureWith is newFixture with options for the fake, for the tests that need it to behave in a
// particular way (see WithContextCancellation).
func newFixtureWith(t *testing.T, contacts int, options ...FakeOption) *fixture {
	t.Helper()
	return newFixtureConfigured(t, contacts, nil, options...)
}

// newFixtureConfigured is the fixture with a hook for the engine's configuration, which is where the
// rate controller goes: the tests about the rate following SES need one, the rest do not.
func newFixtureConfigured(t *testing.T, contacts int, configure func(*Config),
	options ...FakeOption) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "nibwire.db"))
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
	cfg := Config{
		AppURL: testAppURL, SendingOn: true, SendRatePerSec: 1000,
		Limits: model.Limits{MaxEmailBytes: 100_000, BounceRate: 0.02, ComplaintRate: 0.0008,
			MaxSendsPerDay: 50_000, WelcomeEmailsPer10Minute: 100},
		PollInterval: 10 * time.Millisecond,
		// The whole suite runs with the engine's real concurrency: the invariants this port promises
		// (C1 in flight is never released, C2 settling counts claimed, F1 shutdown leaves nothing
		// claimed) have to hold when sixteen sends are in the air, not only when they are queued up
		// one at a time. A test that needs a single file sets it back to 1.
		SendConcurrency: 16,
	}
	if configure != nil {
		configure(&cfg)
	}
	logs := newLogCapture(t)
	engine := NewEngine(db, fake, signer, cfg, slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(engine.Close)

	return &fixture{db: db, engine: engine, fake: fake, signer: signer, logs: logs,
		newsletter: newsletter, campaign: campaign, contacts: audience}
}

// logCapture is a logger that writes what the engine said into the test's own output, and keeps a
// copy. It keeps one because a pause's reason reaches the log and the alert and nothing else, so this
// is the only place a test can read it back.
type logCapture struct {
	t     *testing.T
	mu    sync.Mutex
	lines []string
}

func newLogCapture(t *testing.T) *logCapture { return &logCapture{t: t} }

func (c *logCapture) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	c.mu.Lock()
	c.lines = append(c.lines, line)
	c.mu.Unlock()
	c.t.Log(line)
	return len(p), nil
}

// says is whether any line contains substring.
func (c *logCapture) says(substring string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range c.lines {
		if strings.Contains(line, substring) {
			return true
		}
	}
	return false
}

// serial puts the fixture back to one send at a time.
//
// The assertions that count "the messages the batch had not reached yet" are statements about a
// queue, and only a single file has one: with sixteen workers and six messages, every message has
// already been handed out by the time the first send returns, so there is nothing left to reach.
// Those tests say so out loud and use this; the concurrency-independent invariants (nothing left
// claimed, nothing unknown, nothing sent twice) are asserted at the fixture's real concurrency.
func (f *fixture) serial() *fixture {
	f.engine.cfg.SendConcurrency = 1
	return f
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
//
// The counter is a property of the order sends come back in, so the exact stopping point is a
// statement about a single file; the concurrent version below says what is left of it at sixteen.
func TestConsecutiveFailuresPauseTheCampaign(t *testing.T) {
	f := newFixture(t, 6).serial()
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
	// The one message that was not reached is back in the queue, not failed and not claimed.
	if claimed, unknown, pending := claimedCount(t, f.db); claimed != 0 || unknown != 0 || pending != 1 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d, want 0/0/1", claimed, unknown, pending)
	}
}

// 并发之下「五连败就暂停」仍成立，只是在飞的都会落地：停掉的判定只能拦住还没发出去的消息。
//
// With SEND_CONCURRENCY workers, by the time the fifth failure is counted up to concurrency-1 more
// sends are already in flight and will come back too. What survives concurrency is the part that
// matters: the campaign stops, the failures are recorded, and every message the batch did not reach
// is released rather than left claimed.
func TestConsecutiveFailuresPauseTheCampaignUnderConcurrency(t *testing.T) {
	f := newFixture(t, 40)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "MessageRejected", "Message contains a virus")
	}
	result := f.drain(t)

	if result.Failed < rules.MaxConsecutiveFailures {
		t.Fatalf("failed = %d, want at least %d", result.Failed, rules.MaxConsecutiveFailures)
	}
	// Nothing beyond the workers that were already in flight when the rule fired.
	if most := rules.MaxConsecutiveFailures + f.engine.cfg.SendConcurrency - 1; result.Failed > most {
		t.Fatalf("failed = %d, want no more than %d (the rule fires at %d and at most %d were in flight)",
			result.Failed, most, rules.MaxConsecutiveFailures, f.engine.cfg.SendConcurrency)
	}
	loaded, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != model.CampaignPaused {
		t.Fatalf("campaign = %s, want paused", loaded.Status)
	}
	claimed, unknown, pending := claimedCount(t, f.db)
	if claimed != 0 || unknown != 0 {
		t.Fatalf("claimed = %d, unknown = %d, want 0/0", claimed, unknown)
	}
	if result.Failed+pending != 40 {
		t.Fatalf("failed %d + pending %d != 40: a message was lost", result.Failed, pending)
	}
}

// ARCHIVE_HOSTS 的值要一路走到发出去的那封信里：config 读进来、app 交给引擎、引擎交给收件人渲染——
// 三处字段赋值此前没有一条直接断言，"Read online" 链接就会随着任何一次重构静静退化（#50 与审计 F2）。
func TestTheArchiveHostReachesTheSentEmail(t *testing.T) {
	f := newFixtureConfigured(t, 1, func(cfg *Config) {
		cfg.ArchiveHosts = "archive.example.com=field-notes"
	})
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	if result := f.drain(t); result.Sent != 1 {
		t.Fatalf("result = %+v", result)
	}

	sent := f.fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent = %d messages", len(sent))
	}
	// The link is a click redirect, so its target is behind the tracking token: decode it rather than
	// looking for the URL in the HTML, which would also find the reader's own links to example.com.
	want := "https://archive.example.com/p/issue-12"
	targets := map[string]bool{}
	for _, token := range clickTokens(t, sent[0].Email.HTML) {
		click, ok := f.signer.ReadClickToken(token)
		if ok {
			targets[click.URL] = true
		}
	}
	if !targets[want] {
		t.Errorf("no link to %q; the email links to %v", want, keysOf(targets))
	}
	if targets[testAppURL+"/archive/field-notes/p/issue-12"] {
		t.Error("the email links to the shared archive path although the newsletter has a host of its own")
	}
}

// clickTokens pulls the tracking tokens out of a rendered email's links.
func clickTokens(t *testing.T, html string) []string {
	t.Helper()
	matches := regexp.MustCompile(`/c/([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(html, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

func keysOf(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// A test email goes to the sentinel contact: the campaign report never sees it (design §6.2).
func TestTestEmailUsesTheSentinelContact(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	result, err := f.engine.Test(ctx, f.campaign.ID, "tester@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderMessageID == "" {
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

// 发送关闭时，后台发送循环照常空转，直到 ctx 结束才返回，且不返回错误。
//
// Sending off is a setting, not a failure. serve stops everything when one of its components returns
// an error, so a Run that reported "Sending is off" took the archive, the unsubscribe links and MCP
// down with it — and SENDING_ENABLED=false is what deploy/.env.example ships.
func TestRunKeepsRunningWhileSendingIsOff(t *testing.T) {
	f := newFixture(t, 1)
	f.engine.cfg.SendingOn = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.engine.Run(ctx) }()

	// Twenty poll intervals: long enough for the old Run to have hit SendBatch and returned.
	select {
	case err := <-done:
		t.Fatalf("Run returned %v while sending is off, want it to keep running", err)
	case <-time.After(20 * f.engine.cfg.PollInterval):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop within five seconds of cancellation")
	}
	if f.fake.SentCount() != 0 {
		t.Fatalf("sent = %d, want 0", f.fake.SentCount())
	}
}

// 空转就是空转：队列里的消息一封都不发、不领取，而且都还在队列里等着发送打开。
//
// The two tests overlap on purpose: the one above arrived on main with PR #19 and this one with the
// phase 8 branch, and the reviewer asked for both. This one queues its messages first, so it says
// what idling means for the work that is already waiting rather than only that the loop survived.
func TestRunIdlesWhenSendingIsOff(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	f.engine.cfg.SendingOn = false

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.engine.Run(runCtx) }()
	// Twenty ticks of the fixture's 10 ms poll interval: an engine that stopped on the first one has
	// had every chance to say so.
	select {
	case err := <-done:
		cancel()
		t.Fatalf("Run returned %v while sending is off, want it to idle", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want a clean stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}

	// Idle means idle: the queued messages are still in the queue and nothing was claimed or sent.
	if claimed, unknown, pending := claimedCount(t, f.db); claimed != 0 || unknown != 0 || pending != 2 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d, want 0/0/2", claimed, unknown, pending)
	}
	if sent := f.fake.SentCount(); sent != 0 {
		t.Fatalf("%d messages were sent while sending is off", sent)
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

// 归档打开时，一期在发出时拿到自己页面的地址（上游 assignArchiveSlug）：从主题来、唯一、且此后不再变。
func TestAnIssueGetsItsArchiveSlugWhenItGoesOut(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	f.newsletter.ArchiveEnabled = true
	if err := f.db.UpdateNewsletter(ctx, f.newsletter); err != nil {
		t.Fatal(err)
	}
	f.campaign.Subject = "Issue 12: the one about pricing!"
	if err := f.db.UpdateCampaign(ctx, f.campaign); err != nil {
		t.Fatal(err)
	}

	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	queued, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.ArchiveSlug != "issue-12-the-one-about-pricing" {
		t.Fatalf("slug = %q", queued.ArchiveSlug)
	}

	// 第二期同样的主题：地址不撞车。
	second := model.NewCampaign(f.newsletter.ID, "Issue 12: the one about pricing!", time.Now().UTC())
	second.Body = "# Hello\n\nRead [this](https://example.com)."
	if err := f.db.CreateCampaign(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Queue(ctx, second.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	other, err := f.db.Campaign(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.ArchiveSlug != "issue-12-the-one-about-pricing-2" {
		t.Fatalf("second slug = %q", other.ArchiveSlug)
	}

	// 归档关着：不分配（一个永远不会被服务的地址只是看着像回事）。
	off := newFixture(t, 1)
	off.newsletter.ArchiveEnabled = false
	if err := off.db.UpdateNewsletter(ctx, off.newsletter); err != nil {
		t.Fatal(err)
	}
	if _, err := off.engine.Queue(ctx, off.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	if plain, err := off.db.Campaign(ctx, off.campaign.ID); err != nil || plain.ArchiveSlug != "" {
		t.Fatalf("archive off: %q %v", plain.ArchiveSlug, err)
	}
}
