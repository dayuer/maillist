package store

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// claimedEmails is the claimed messages' addresses, sorted, so a test can compare sets.
func claimedEmails(t *testing.T, claimed []string) string {
	t.Helper()
	sort.Strings(claimed)
	return strings.Join(claimed, ",")
}

// Each channel's lane claims only its own messages, so a channel that is slow or throttled never
// holds another channel's mail.
func TestClaimChannelMessagesTakesOnlyThatChannel(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, sendingCampaign(t, db), 4)
	messages[1].Channel = "aws2"
	messages[3].Channel = "aws2"
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.ClaimChannelMessages(ctx, "aws2", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range claimed {
		if message.Channel != "aws2" {
			t.Fatalf("claimed a %s message", message.Channel)
		}
		got = append(got, message.Email)
	}
	if emails := claimedEmails(t, got); emails != "reader1@example.com,reader3@example.com" {
		t.Fatalf("claimed %s", emails)
	}
	// The default channel's two are still there for the default lane.
	claimed, err = db.ClaimChannelMessages(ctx, "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("default claimed %d, want 2", len(claimed))
	}
}

// Mail recorded on a channel this process has not loaded is claimed by nobody's lane; the engine
// claims it separately, to fail it or to pause its campaign with a reason.
func TestClaimMessagesOutsideTakesTheChannelsNobodyServes(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, sendingCampaign(t, db), 4)
	messages[1].Channel = "aws2"
	messages[2].Channel = "gone"
	messages[3].Channel = "off"
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.ClaimMessagesOutside(ctx, []string{"default", "aws2"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range claimed {
		got = append(got, message.Channel)
	}
	if channels := claimedEmails(t, got); channels != "gone,off" {
		t.Fatalf("claimed messages on %s, want gone and off", channels)
	}
	// With no channels named, every pending message is outside: nothing panics on an empty list.
	rest, err := db.ClaimMessagesOutside(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 {
		t.Fatalf("claimed %d, want the 2 left", len(rest))
	}
}
