package mailer

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
)

// pausingSender is a sender whose own reading of every error is "pause": the engine must act on the
// channel's reading, not on SES's, once the sender can give one.
type pausingSender struct{ *Fake }

func (pausingSender) Classify(error) rules.ErrorAction { return rules.ErrorPause }

func TestTheEngineFollowsTheChannelsOwnErrorReading(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	engine := NewEngine(f.db, pausingSender{f.fake}, f.signer, Config{
		AppURL: testAppURL, SendingOn: true, SendRatePerSec: 1000,
		Limits: model.Limits{MaxEmailBytes: 100_000, BounceRate: 0.02, ComplaintRate: 0.0008,
			MaxSendsPerDay: 50_000, WelcomeEmailsPer10Minute: 100},
		PollInterval: 10 * time.Millisecond, SendConcurrency: 1,
	}, slog.New(slog.NewTextHandler(newLogCapture(t), nil)))
	t.Cleanup(engine.Close)

	if _, err := engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// SES would call this a plain failure; the channel says pause.
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "MessageRejected", "Illegal address")
	}
	if _, err := engine.SendBatch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignPaused {
		t.Fatalf("status = %s, want paused: the engine read the error as SES would", campaign.Status)
	}
}
