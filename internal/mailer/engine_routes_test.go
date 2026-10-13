package mailer

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/route"
	"nibwire/internal/store"
)

// secondChannel rebuilds the fixture's engine with a named channel beside the default one, and
// returns the fake that channel sends through. Routing is about more than one channel; everything
// else about the fixture stays as it is.
func (f *fixture) secondChannel(t *testing.T, name string, cfg Config, limits map[string]ChannelLimit) *Fake {
	t.Helper()
	fake := NewFake()
	channel := NewChannel(name, ChannelTypeSES, fake, StaticQuota{}, StaticIdentity{}, nil)
	registry, err := NewRegistry(
		NewChannel(model.DefaultChannel, ChannelTypeSES, f.fake, StaticQuota{}, StaticIdentity{}, nil), channel)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ChannelLimits = limits
	f.engine = NewEngine(f.db, registry, f.signer, cfg, slog.New(slog.NewTextHandler(f.logs, nil)))
	t.Cleanup(f.engine.Close)
	return fake
}

// routeTo stores one rule: everything to channel. It is written the way `route add --default` writes
// it, because that is the rule a deployment that wants all of its mail through one channel has.
func (f *fixture) routeTo(t *testing.T, channel string) {
	t.Helper()
	f.route(t, route.Match{Default: true}, []route.Target{{Channel: channel, Weight: 1}})
}

func (f *fixture) route(t *testing.T, match route.Match, targets []route.Target) store.RouteRow {
	t.Helper()
	row := store.RouteRow{ID: ids.New(), Match: match, Targets: targets}
	if err := f.db.CreateRoute(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	return row
}

// asRule is the stored row as the pure routing decision takes it.
func asRule(row store.RouteRow) route.Rule {
	return route.Rule{ID: row.ID, Match: row.Match, Targets: row.Targets}
}

// The rules decide which channel a campaign's messages are recorded on, at the moment they are
// queued — and the lane that sends them is the one that channel belongs to.
func TestQueueRecordsTheChannelTheRulesPick(t *testing.T) {
	f := newFixture(t, 3)
	f.secondChannel(t, "aws2", f.engine.cfg, nil)
	f.routeTo(t, "aws2")

	if _, err := f.engine.Queue(context.Background(), f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.db.ClaimChannelMessages(context.Background(), "aws2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 3 {
		t.Fatalf("aws2 has %d of the campaign's messages, want 3", len(claimed))
	}
}

// A split sends part of the audience through each channel, and the day is reserved per channel: what
// one channel is charged is not what the other one is.
func TestQueueSplitsTheAudienceBetweenChannels(t *testing.T) {
	f := newFixture(t, 20)
	f.secondChannel(t, "aws2", f.engine.cfg, nil)
	rule := f.route(t, route.Match{Default: true}, []route.Target{
		{Channel: model.DefaultChannel, Weight: 50}, {Channel: "aws2", Weight: 50}})
	ctx := context.Background()

	if _, err := f.engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}

	// Which contact lands on which channel is the rule's decision, and is tested where it is made.
	// What this checks is that the day is reserved per channel, for exactly the messages the rule
	// sent to each one.
	want := store.Charge{}
	for _, contact := range f.contacts {
		want[route.Pick([]route.Rule{asRule(rule)}, contact.ID, contact.Email)]++
	}
	byChannel, err := f.db.ReservedByChannel(ctx, store.Day(time.Now()), "")
	if err != nil {
		t.Fatal(err)
	}
	if byChannel.Total() != len(f.contacts) || want.Total() != len(f.contacts) {
		t.Fatalf("reserved = %v, want %d messages", byChannel, len(f.contacts))
	}
	for channel, n := range want {
		if byChannel[channel] != n {
			t.Fatalf("reserved = %v, want %v", byChannel, want)
		}
	}
}

// A rule that sends mail to a channel this process cannot send through refuses the step whole: no
// messages, no charge, and the reason names the channel — an operator decides, it does not quietly
// go out another way (design §4.3).
func TestQueueRefusesARuleThatSendsToAChannelWithNoLane(t *testing.T) {
	f := newFixture(t, 3)
	f.routeTo(t, "ghost")

	result, err := f.engine.Queue(context.Background(), f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued {
		t.Fatal("the step was queued")
	}
	if !strings.Contains(result.Reason, "channel ghost is not loaded") {
		t.Fatalf("reason = %q, want it to name the channel", result.Reason)
	}
	if reserved, err := f.db.Reserved(context.Background(), store.Day(time.Now()), ""); err != nil || reserved != 0 {
		t.Fatalf("reserved = %d %v, want nothing charged", reserved, err)
	}
	if _, err := f.db.ClaimChannelMessages(context.Background(), model.DefaultChannel, 10); err != nil {
		t.Fatal(err)
	}
}

// A channel's own max_per_day is a ceiling the day's budget does not have: the step is refused, and
// the reason says which channel and how much room is left under it.
func TestQueueNamesTheChannelWhoseOwnBudgetIsShort(t *testing.T) {
	f := newFixture(t, 4)
	f.secondChannel(t, "aws2", f.engine.cfg, map[string]ChannelLimit{"aws2": {MaxPerDay: 2}})
	f.routeTo(t, "aws2")

	result, err := f.engine.Queue(context.Background(), f.campaign.ID, Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued {
		t.Fatal("the step was queued over the channel's own ceiling")
	}
	if !strings.Contains(result.Reason, "channel aws2") || !strings.Contains(result.Reason, "at most 2 a day") {
		t.Fatalf("reason = %q, want it to name the channel and its ceiling", result.Reason)
	}
	if reserved, err := f.db.Reserved(context.Background(), store.Day(time.Now()), ""); err != nil || reserved != 0 {
		t.Fatalf("reserved = %d %v, want nothing charged", reserved, err)
	}
}

// A confirmation is a single message with a deadline — a subscriber is waiting for it — so a rule
// that sends it somewhere this process cannot send falls back to the default channel instead of
// failing it forever (#76).
func TestAConfirmationFallsBackWhenItsChannelIsNotLoaded(t *testing.T) {
	f := newFixture(t, 1)
	f.routeTo(t, "ghost")
	ctx := context.Background()
	// A confirmation goes to someone who has not confirmed yet.
	if _, err := f.db.UpdateContactStatus(ctx, f.contacts[0].ID, model.ContactPending,
		[]model.ContactStatus{model.ContactActive}); err != nil {
		t.Fatal(err)
	}
	f.contacts[0].Status = model.ContactPending

	result, err := f.engine.QueueConfirmation(ctx, f.newsletter, f.contacts[0])
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID == "" {
		t.Fatalf("QueueConfirmation = %+v, want it to send anyway", result)
	}
	message, err := f.db.Message(ctx, result.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if message.Channel != model.DefaultChannel {
		t.Fatalf("channel = %q, want the default channel", message.Channel)
	}
	if message.Provider == "" {
		t.Fatal("the provider was not recorded")
	}
}

// A test send has no contact, so its channel is decided from the address being tested.
func TestATestSendFollowsTheRulesForTheAddress(t *testing.T) {
	f := newFixture(t, 1)
	f.secondChannel(t, "aws2", f.engine.cfg, nil)
	f.route(t, route.Match{Domains: []string{"example.com"}}, []route.Target{{Channel: "aws2", Weight: 1}})

	result, err := f.engine.Test(context.Background(), f.campaign.ID, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	message, err := f.db.Message(context.Background(), result.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if message.Channel != "aws2" {
		t.Fatalf("channel = %q, want the rule's channel", message.Channel)
	}
}
