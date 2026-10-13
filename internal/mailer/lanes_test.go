package mailer

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/providers"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

// newLaneFixture is the fixture with a second channel, aws2, sending through a fake of its own. The
// engine is rebuilt over the same database with both channels in its registry.
func newLaneFixture(t *testing.T, contacts int, configure func(*Config)) (*fixture, *Fake) {
	t.Helper()
	f := newFixtureConfigured(t, contacts, configure)
	second := NewFake()
	registry, err := NewRegistry(
		NewChannel(model.DefaultChannel, ChannelTypeSES, f.fake, StaticQuota{}, StaticIdentity{}, nil),
		NewChannel("aws2", ChannelTypeSES, second, StaticQuota{}, StaticIdentity{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	f.engine = NewEngine(f.db, registry, f.signer, f.engine.cfg, f.engine.log)
	t.Cleanup(f.engine.Close)
	return f, second
}

// queueOn queues the fixture's campaign by hand, contact i on channelOf(i), and moves the campaign to
// sending. It stands in for routing, which arrives with step 2c.
func queueOn(t *testing.T, f *fixture, channelOf func(i int) string) []model.Message {
	t.Helper()
	ctx := context.Background()
	messages := make([]model.Message, 0, len(f.contacts))
	for i, contact := range f.contacts {
		messages = append(messages, model.Message{
			ID: ids.New(), Kind: model.KindCampaign, CampaignID: f.campaign.ID,
			NewsletterID: f.newsletter.ID, ContactID: contact.ID, Email: contact.Email,
			Provider: providers.Mailbox(contact.Email), Status: model.MessagePending,
			CreatedAt: time.Now().UTC(), Channel: channelOf(i),
		})
	}
	if _, err := f.db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}
	moved, err := f.db.SetCampaignStatus(ctx, f.campaign.ID,
		[]model.CampaignStatus{model.CampaignDraft}, model.CampaignSending)
	if err != nil || !moved {
		t.Fatalf("move to sending: %v %v", moved, err)
	}
	return messages
}

// evenOnDefault puts even contacts on default and odd ones on aws2.
func evenOnDefault(i int) string {
	if i%2 == 0 {
		return model.DefaultChannel
	}
	return "aws2"
}

func recipients(fake *Fake) string {
	var to []string
	for _, sent := range fake.Sent() {
		to = append(to, sent.Email.To)
	}
	sort.Strings(to)
	return strings.Join(to, ",")
}

// A message goes out through the channel it is on, whether the batch claims it or is handed its id.
func TestEachMessageGoesOutThroughItsOwnChannel(t *testing.T) {
	f, second := newLaneFixture(t, 4, nil)
	ctx := context.Background()
	messages := queueOn(t, f, evenOnDefault)

	// The explicit-ids path (a welcome or confirmation email) goes through the message's channel too.
	result, err := f.engine.SendBatch(ctx, []string{messages[1].ID})
	if err != nil || result.Sent != 1 {
		t.Fatalf("send by id = %+v, %v", result, err)
	}
	if second.SentCount() != 1 || f.fake.SentCount() != 0 {
		t.Fatalf("aws2 sent %d, default %d, want 1 and 0", second.SentCount(), f.fake.SentCount())
	}

	result, err = f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 3 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 3 sent", result)
	}
	if got := recipients(f.fake); got != "reader0@example.com,reader2@example.com" {
		t.Fatalf("default sent to %s", got)
	}
	if got := recipients(second); got != "reader1@example.com,reader3@example.com" {
		t.Fatalf("aws2 sent to %s", got)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSent {
		t.Fatalf("status = %s, want sent", campaign.Status)
	}
}

// Each channel has its own rate: a throttle on aws2 halves aws2's rate and leaves the default
// channel's rate, and its sends, alone.
func TestAThrottledChannelSlowsOnlyItself(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	f, second := newLaneFixture(t, 6, func(cfg *Config) {
		cfg.RateControl = control
		cfg.Limits.SendRateHeadroom = 0.9
	})
	ctx := context.Background()
	queueOn(t, f, evenOnDefault)
	now := time.Now().UTC()
	reading := ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}
	f.engine.SetQuota(model.DefaultChannel, reading, now)
	f.engine.SetQuota("aws2", reading, now)
	aws2, _ := f.engine.laneFor("aws2")
	if aws2.control == control || aws2.bucket == f.engine.defaultLane().bucket {
		t.Fatal("aws2 shares the default channel's controller or bucket")
	}
	if got := aws2.control.Rate(now); got != 450 {
		t.Fatalf("aws2 rate before the throttle = %v, want 450", got)
	}

	for i := 1; i < len(f.contacts); i += 2 {
		second.FailWith(f.contacts[i].Email, "ThrottlingException", "Maximum sending rate exceeded")
	}
	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Retry || result.Failed != 0 {
		t.Fatalf("result = %+v, want a retry and nothing failed", result)
	}
	if got := aws2.control.Rate(time.Now()); got != 225 {
		t.Fatalf("aws2 rate after its throttle = %v, want 225", got)
	}
	if got := aws2.bucket.Rate(); got != 225 {
		t.Fatalf("aws2 bucket = %v, want 225", got)
	}
	if got := control.Rate(time.Now()); got != 450 {
		t.Fatalf("default rate = %v, want 450: another channel's throttle slowed it", got)
	}
	if got := f.engine.defaultLane().bucket.Rate(); got != 450 {
		t.Fatalf("default bucket = %v, want 450", got)
	}
	if got := recipients(f.fake); got != "reader0@example.com,reader2@example.com,reader4@example.com" {
		t.Fatalf("default sent to %s, want all of its own", got)
	}
}

// A channel whose bucket is blocked holds only its own workers: the default channel sends all of its
// mail meanwhile, and aws2's goes back to the queue when the batch stops.
func TestABlockedChannelDoesNotHoldTheOthers(t *testing.T) {
	f, second := newLaneFixture(t, 8, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := queueOn(t, f, evenOnDefault)

	// A rate of zero, and the one free slot spent: every Take on aws2 now waits an hour.
	aws2, _ := f.engine.laneFor("aws2")
	aws2.bucket.SetRate(0)
	if err := aws2.bucket.Take(ctx); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result BatchResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.engine.SendBatch(ctx, nil)
		done <- outcome{result, err}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for f.fake.SentCount() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("default sent %d of 4 while aws2 was blocked", f.fake.SentCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case got := <-done:
		t.Fatalf("the batch finished (%+v, %v) while aws2 was still waiting", got.result, got.err)
	default:
	}
	if second.SentCount() != 0 {
		t.Fatalf("aws2 sent %d through a blocked bucket", second.SentCount())
	}

	cancel()
	select {
	case got := <-done:
		if got.err != nil || !got.result.Retry {
			t.Fatalf("after the stop = %+v, %v, want a retry and no error", got.result, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the batch did not stop")
	}
	// What aws2 never sent is back in the queue, not claimed and not unknown.
	for i := 1; i < len(messages); i += 2 {
		message, err := f.db.Message(context.Background(), messages[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if message.Status != model.MessagePending {
			t.Fatalf("aws2 message %d is %s, want pending", i, message.Status)
		}
	}
}

// An error of the engine's own (here a closed rate limiter) is not the channel's to classify: the
// channel would pause on anything, yet the messages fail and the campaign does not pause.
func TestAnEngineErrorIsNotTheChannelsToClassify(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	alwaysPause := func(error) rules.ErrorAction { return rules.ErrorPause }
	engine := NewEngine(f.db, defaultOnly(t, f.fake, alwaysPause), f.signer, f.engine.cfg, f.engine.log)
	t.Cleanup(engine.Close)
	messages := queueOn(t, f, func(int) string { return model.DefaultChannel })
	engine.defaultLane().bucket.Close()

	result, err := engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 2 || result.Sent != 0 {
		t.Fatalf("result = %+v, want both failed", result)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status == model.CampaignPaused {
		t.Fatal("the campaign paused: the engine's own error was read with the channel's rules")
	}
	for _, queued := range messages {
		message, err := f.db.Message(ctx, queued.ID)
		if err != nil {
			t.Fatal(err)
		}
		if message.Status != model.MessageFailed || !strings.Contains(message.Error, "closed") {
			t.Fatalf("message = %s %q, want failed by the closed limiter", message.Status, message.Error)
		}
	}
}

// A message on a channel that does not exist can never go out: it fails, naming the channel.
func TestAMessageOnAnUnknownChannelFails(t *testing.T) {
	f, _ := newLaneFixture(t, 2, nil)
	ctx := context.Background()
	messages := queueOn(t, f, func(i int) string {
		if i == 0 {
			return model.DefaultChannel
		}
		return "nowhere"
	})

	result, err := f.engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v, want 1 sent and 1 failed", result)
	}
	message, err := f.db.Message(ctx, messages[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if message.Status != model.MessageFailed || !strings.Contains(message.Error, "channel nowhere does not exist") {
		t.Fatalf("message = %s %q", message.Status, message.Error)
	}
	counters, _, err := f.db.CampaignCountersOf(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Failed != 1 {
		t.Fatalf("failed counter = %d, want 1", counters.Failed)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignSent {
		t.Fatalf("status = %s, want sent: nothing is left to send", campaign.Status)
	}
}

// A message on a channel that is disabled, broken or not loaded goes back to the queue without
// counting as failed, and its campaign pauses with a reason that names the channel.
func TestAMessageOnAnUnavailableChannelPausesItsCampaign(t *testing.T) {
	cases := []struct {
		name    string
		row     store.ChannelRow
		problem error
		want    []string
	}{
		{"disabled", store.ChannelRow{Name: "off", Type: "ses", SecretEnv: "OFF", Enabled: false}, nil,
			[]string{"channel off is disabled", "restart serve"}},
		{"broken", store.ChannelRow{Name: "broke", Type: "ses", SecretEnv: "BROKE", Enabled: true},
			errors.New("set both BROKE_ACCESS_KEY_ID and BROKE_SECRET_ACCESS_KEY"),
			[]string{"channel broke could not be loaded", "BROKE_SECRET_ACCESS_KEY"}},
		{"enabled after start", store.ChannelRow{Name: "late", Type: "ses", SecretEnv: "LATE", Enabled: true},
			nil, []string{"channel late is not loaded"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := newLaneFixture(t, 2, func(cfg *Config) {
				if c.problem != nil {
					cfg.ChannelProblems = map[string]error{c.row.Name: c.problem}
				}
			})
			ctx := context.Background()
			if err := f.db.CreateChannel(ctx, c.row); err != nil {
				t.Fatal(err)
			}
			messages := queueOn(t, f, func(int) string { return c.row.Name })

			result, err := f.engine.SendBatch(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Failed != 0 || result.Sent != 0 {
				t.Fatalf("result = %+v, want nothing sent and nothing failed", result)
			}
			campaign, err := f.db.Campaign(ctx, f.campaign.ID)
			if err != nil {
				t.Fatal(err)
			}
			if campaign.Status != model.CampaignPaused {
				t.Fatalf("status = %s, want paused", campaign.Status)
			}
			for _, want := range c.want {
				if !f.logs.says(want) {
					t.Errorf("the pause reason does not say %q", want)
				}
			}
			for _, sent := range messages {
				message, err := f.db.Message(ctx, sent.ID)
				if err != nil {
					t.Fatal(err)
				}
				if message.Status != model.MessagePending {
					t.Fatalf("message is %s, want pending", message.Status)
				}
			}
		})
	}
}

// The default lane runs at SEND_RATE_PER_SECOND with SEND_CONCURRENCY; every other lane is capped by
// its own max_per_second, runs ChannelConcurrency sends at once, and shares the headroom (design §6).
func TestEachLaneHasItsOwnCapAndConcurrency(t *testing.T) {
	control := ratectl.New(ratectl.Config{Headroom: 0.9, Cap: 100})
	f, _ := newLaneFixture(t, 1, func(cfg *Config) {
		cfg.RateControl = control
		cfg.Limits.SendRateHeadroom = 0.5
		cfg.SendConcurrency = 3
		cfg.ChannelConcurrency = 7
		cfg.ChannelLimits = map[string]ChannelLimit{"aws2": {MaxPerSecond: 20}}
	})
	now := time.Now().UTC()
	f.engine.SetQuota(model.DefaultChannel, ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}, now)
	f.engine.SetQuota("aws2", ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}, now)
	f.engine.SetQuota("aws2-unknown", ratectl.Quota{MaxSendRate: 1}, now) // ignored

	lanes := f.engine.Lanes()
	if len(lanes) != 2 || lanes[0].Channel.Name() != model.DefaultChannel || lanes[1].Channel.Name() != "aws2" {
		t.Fatalf("lanes = %v", lanes)
	}
	if lanes[0].Control != control || lanes[0].Bucket.Rate() != 100 {
		t.Fatalf("default lane: control %p, bucket %v, want the configured controller at 100",
			lanes[0].Control, lanes[0].Bucket.Rate())
	}
	if got := lanes[1].Control.Rate(now); got != 20 {
		t.Fatalf("aws2 rate = %v, want its max_per_second, 20", got)
	}
	// Without a cap of its own the same headroom applies: 500 × 0.5.
	f.engine.cfg.ChannelLimits = nil
	uncapped := newLanesFor(t, f.engine.cfg, f.fake)
	uncapped.SetQuota("aws2", ratectl.Quota{MaxSendRate: 500, Max24HourSend: -1}, now)
	if got := uncapped.Lanes()[1].Control.Rate(now); got != 250 {
		t.Fatalf("uncapped aws2 rate = %v, want 250", got)
	}

	aws2, _ := f.engine.laneFor("aws2")
	if got := f.engine.laneConcurrency(f.engine.defaultLane()); got != 3 {
		t.Fatalf("default concurrency = %d, want SEND_CONCURRENCY, 3", got)
	}
	if got := f.engine.laneConcurrency(aws2); got != 7 {
		t.Fatalf("aws2 concurrency = %d, want 7", got)
	}
}

// The stray claim scans the pending queue (no index covers messages.channel until step 2c), so Run
// does not make it on every poll: by default forty poll intervals apart, and never under ten seconds.
func TestStraysArePolledFarLessOftenThanLanes(t *testing.T) {
	cases := []struct {
		poll, stray, want time.Duration
	}{
		{250 * time.Millisecond, 0, 10 * time.Second},
		{time.Second, 0, 40 * time.Second},
		{10 * time.Millisecond, 0, 10 * time.Second},
		{10 * time.Millisecond, 300 * time.Millisecond, 300 * time.Millisecond},
	}
	for _, c := range cases {
		engine := &Engine{cfg: Config{PollInterval: c.poll, StrayInterval: c.stray}}
		if got := engine.strayInterval(); got != c.want {
			t.Errorf("poll %v, stray %v: interval = %v, want %v", c.poll, c.stray, got, c.want)
		}
	}
}

// In Run the default lane sends on its own short poll while a stray waits for the stray poll, and
// the stray is still dealt with once that comes round.
func TestRunPicksUpStraysOnTheirOwnLongerInterval(t *testing.T) {
	f, _ := newLaneFixture(t, 2, func(cfg *Config) { cfg.StrayInterval = 600 * time.Millisecond })
	messages := queueOn(t, f, func(i int) string {
		if i == 0 {
			return model.DefaultChannel
		}
		return "nowhere"
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- f.engine.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	}()

	status := func(id string) model.MessageStatus {
		t.Helper()
		message, err := f.db.Message(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return message.Status
	}
	for status(messages[0].ID) != model.MessageSent {
		if time.Since(start) > 5*time.Second {
			t.Fatal("the default lane did not send")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if time.Since(start) < 500*time.Millisecond && status(messages[1].ID) != model.MessagePending {
		t.Fatalf("the stray was claimed at %v, before its own interval", time.Since(start))
	}
	for status(messages[1].ID) != model.MessageFailed {
		if time.Since(start) > 10*time.Second {
			t.Fatal("the stray was never picked up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed < 550*time.Millisecond {
		t.Fatalf("the stray was dealt with after %v, before its interval of 600ms", elapsed)
	}
}

// newLanesFor is an engine over a fresh registry of default and aws2, both sending through sender.
func newLanesFor(t *testing.T, cfg Config, sender Sender) *Engine {
	t.Helper()
	registry, err := NewRegistry(
		NewChannel(model.DefaultChannel, ChannelTypeSES, sender, StaticQuota{}, StaticIdentity{}, nil),
		NewChannel("aws2", ChannelTypeSES, sender, StaticQuota{}, StaticIdentity{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(nil, registry, nil, cfg, nil)
	t.Cleanup(engine.Close)
	return engine
}
