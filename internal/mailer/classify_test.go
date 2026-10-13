package mailer

import (
	"context"
	"testing"

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
	cfg := f.engine.cfg
	cfg.SendConcurrency = 1
	engine := NewEngine(f.db, pausingSender{f.fake}, f.signer, cfg, f.engine.log)
	t.Cleanup(engine.Close)

	if _, err := engine.Queue(ctx, f.campaign.ID, Charge{}); err != nil {
		t.Fatal(err)
	}
	// SES would call this a plain failure; the channel says pause.
	for _, contact := range f.contacts {
		f.fake.FailWith(contact.Email, "MessageRejected", "Illegal address")
	}
	result, err := engine.SendBatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 || result.Sent != 0 {
		t.Fatalf("result = %+v, want nothing sent and nothing counted as failed", result)
	}
	campaign, err := f.db.Campaign(ctx, f.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignPaused {
		t.Fatalf("status = %s, want paused: the engine read the error as SES would", campaign.Status)
	}
	// A pause releases the messages rather than failing them: both are back in the queue.
	if claimed, unknown, pending := claimedCount(t, f.db); claimed != 0 || unknown != 0 || pending != 2 {
		t.Fatalf("claimed = %d, unknown = %d, pending = %d, want 0/0/2", claimed, unknown, pending)
	}
}
