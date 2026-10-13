package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// throughDefault is n messages through the default channel: what a deployment with no second channel
// sends, and what every reservation in the tests before 2c-2 meant.
func throughDefault(n int) Charge { return Charge{model.DefaultChannel: n} }

// Each channel has its own share of the day: what one spends is not what another has.
func TestReserveChargesEachChannelSeparately(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const day = "2026-10-11"
	limits := Limits{Channels: map[string]int{"aws2": 100, "back": 100}}

	if ok, err := db.Reserve(ctx, day, "", Charge{"aws2": 60}, limits); err != nil || !ok {
		t.Fatalf("aws2 = %v %v", ok, err)
	}
	// back has a hundred of its own, untouched by what aws2 spent.
	if ok, err := db.Reserve(ctx, day, "", Charge{"back": 60}, limits); err != nil || !ok {
		t.Fatalf("back = %v %v", ok, err)
	}
	// aws2's own ceiling: 60 + 50 is over its 100.
	if ok, err := db.Reserve(ctx, day, "", Charge{"aws2": 50}, limits); err != nil || ok {
		t.Fatalf("aws2 over its own ceiling = %v %v, want false", ok, err)
	}
	// A channel nobody gave a ceiling to is unlimited, as it always was.
	if ok, err := db.Reserve(ctx, day, "", Charge{"third": 1_000_000}, limits); err != nil || !ok {
		t.Fatalf("channel without a ceiling = %v %v", ok, err)
	}

	byChannel, err := db.ReservedByChannel(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if byChannel["aws2"] != 60 || byChannel["back"] != 60 || byChannel["third"] != 1_000_000 {
		t.Fatalf("by channel = %v", byChannel)
	}
	if total, err := db.Reserved(ctx, day, ""); err != nil || total != 60+60+1_000_000 {
		t.Fatalf("reserved = %d %v, want every channel counted", total, err)
	}
}

// One channel short refuses the whole step: not even the channel that had room is charged. A step
// reserved halfway is a campaign sent halfway, which is what per-channel budgets must not allow
// (design §4.3).
func TestReserveRefusesTheWholeStepWhenOneChannelIsShort(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const day = "2026-10-11"
	limits := Limits{Channels: map[string]int{"aws2": 100, "back": 10}}

	if ok, err := db.Reserve(ctx, day, "", Charge{"aws2": 50, "back": 9}, limits); err != nil || !ok {
		t.Fatalf("seeding = %v %v", ok, err)
	}
	if ok, err := db.Reserve(ctx, day, "", Charge{"aws2": 20, "back": 2}, limits); err != nil || ok {
		t.Fatalf("aws2 has room and back does not = %v %v, want false", ok, err)
	}
	byChannel, err := db.ReservedByChannel(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if byChannel["aws2"] != 50 || byChannel["back"] != 9 {
		t.Fatalf("a refused step reserved something: %v", byChannel)
	}
}

// The deployment's own ceiling and the credential's are still day-wide: every channel counts toward
// both, so a deployment cannot spend its quota twice by having two channels.
func TestReserveSumsEveryChannelAgainstTheDayCeilings(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const day = "2026-10-11"
	limits := Limits{Global: 100, Credential: 70, Channels: map[string]int{"aws2": 1000, "back": 1000}}

	if ok, err := db.Reserve(ctx, day, "cred-1", Charge{"aws2": 40}, limits); err != nil || !ok {
		t.Fatalf("aws2 = %v %v", ok, err)
	}
	if ok, err := db.Reserve(ctx, day, "cred-1", Charge{"back": 30}, limits); err != nil || !ok {
		t.Fatalf("back = %v %v, want the credential's 70 to be reachable", ok, err)
	}
	// The credential's ceiling counts the two channels together.
	if ok, err := db.Reserve(ctx, day, "cred-1", Charge{"back": 1}, limits); err != nil || ok {
		t.Fatalf("credential over its budget = %v %v, want false", ok, err)
	}
	// Another credential has its own 70, inside the same 100 for the deployment: 40 + 30 + 29 + 1
	// is exactly the hundred.
	if ok, err := db.Reserve(ctx, day, "cred-2", Charge{"aws2": 29}, limits); err != nil || !ok {
		t.Fatalf("another credential = %v %v", ok, err)
	}
	if ok, err := db.Reserve(ctx, day, "cred-2", Charge{"back": 1}, limits); err != nil || !ok {
		t.Fatalf("the hundredth send = %v %v, want it to fit", ok, err)
	}
	if ok, err := db.Reserve(ctx, day, "cred-2", Charge{"back": 1}, limits); err != nil || ok {
		t.Fatalf("over the deployment's ceiling = %v %v, want false", ok, err)
	}
}

// A refund gives each channel back its own reservation.
func TestRefundIsPerChannel(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const day = "2026-10-11"
	if ok, err := db.Reserve(ctx, day, "", Charge{"aws2": 6, "back": 4}, Limits{}); err != nil || !ok {
		t.Fatalf("reserve = %v %v", ok, err)
	}
	if err := db.Refund(ctx, day, "", Charge{"back": 3}); err != nil {
		t.Fatal(err)
	}
	byChannel, err := db.ReservedByChannel(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if byChannel["aws2"] != 6 || byChannel["back"] != 1 {
		t.Fatalf("by channel = %v, want aws2 6 and back 1", byChannel)
	}
	// A refund larger than what is left floors at zero rather than going negative.
	if err := db.Refund(ctx, day, "", Charge{"back": 100}); err != nil {
		t.Fatal(err)
	}
	if byChannel, err = db.ReservedByChannel(ctx, day, ""); err != nil {
		t.Fatal(err)
	}
	if byChannel["back"] != 0 {
		t.Fatalf("back after an oversized refund = %d, want 0", byChannel["back"])
	}
}

// The charge of a step is the channels its messages go through; an unset channel is the default one.
func TestChargeOfCountsMessagesByChannel(t *testing.T) {
	charge := chargeOf([]model.Message{
		{Channel: "aws2"}, {Channel: "aws2"}, {Channel: "back"}, {},
	})
	want := Charge{"aws2": 2, "back": 1, model.DefaultChannel: 1}
	if len(charge) != len(want) {
		t.Fatalf("charge = %v, want %v", charge, want)
	}
	for channel, n := range want {
		if charge[channel] != n {
			t.Fatalf("charge = %v, want %v", charge, want)
		}
	}
	if got := charge.Channels(); !slices.Equal(got, []string{"aws2", "back", model.DefaultChannel}) {
		t.Fatalf("Channels = %v, want a stable order", got)
	}
	if charge.Total() != 4 {
		t.Fatalf("Total = %d, want 4", charge.Total())
	}
}

// A retry gives each channel its own reservation back: rows that already existed are not going to be
// sent, so their channel must not stay charged. A count cannot say that — which is why the insert
// asks for the rows it wrote back by channel.
func TestReserveAndInsertRefundsTheChannelThatAlreadyHadTheMessage(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	day := Day(time.Now().UTC())
	campaign := sendingCampaign(t, db)

	first := campaignMessages(t, db, campaign, 3)
	first[0].Channel, first[1].Channel, first[2].Channel = "aws2", "back", "aws2"
	if fits, inserted, err := db.ReserveAndInsert(ctx, day, "", Limits{Global: 100}, first); err != nil || !fits || inserted != 3 {
		t.Fatalf("first pass: fits = %v, inserted = %d, %v", fits, inserted, err)
	}

	// A retry of two of them, one from each channel. Both already exist, so the day must end up
	// charged for the one message that is actually going out, and for the channel it is on.
	retry := []model.Message{retried(first[1]), retried(first[2])}
	fits, inserted, err := db.ReserveAndInsert(ctx, day, "", Limits{Global: 100}, retry)
	if err != nil || !fits {
		t.Fatalf("retry: fits = %v %v", fits, err)
	}
	if inserted != 0 {
		t.Fatalf("retry inserted %d, want 0", inserted)
	}
	byChannel, err := db.ReservedByChannel(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if byChannel["aws2"] != 2 || byChannel["back"] != 1 {
		t.Fatalf("by channel after the retry = %v, want aws2 2 and back 1", byChannel)
	}
}

// retried is a message a job wrote a second time: the same recipient and campaign, a new id.
func retried(message model.Message) model.Message {
	message.ID = ids.New()
	message.CreatedAt = time.Now().UTC()
	return message
}

// What a campaign still has to send, per channel, is the charge a resume reserves.
func TestChargeOfCampaignCountsTheUnfinishedMessages(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	campaign := sendingCampaign(t, db)
	messages := campaignMessages(t, db, campaign, 4)
	for i := range messages {
		if i%2 == 0 {
			messages[i].Channel = "aws2"
		} else {
			messages[i].Channel = "back"
		}
	}
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}
	charge, err := db.ChargeOfCampaign(ctx, campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if charge["aws2"] != 2 || charge["back"] != 2 {
		t.Fatalf("charge = %v, want two on each channel", charge)
	}
	// A message that has reached a final state is not reserved for again.
	if err := db.MarkMessageSent(ctx, messages[0].ID, "aws2-1", messages[0].CreatedAt); err != nil {
		t.Fatal(err)
	}
	if charge, err = db.ChargeOfCampaign(ctx, campaign.ID); err != nil {
		t.Fatal(err)
	}
	if charge["aws2"] != 1 || charge["back"] != 2 {
		t.Fatalf("charge after one was sent = %v, want one on aws2", charge)
	}
}
