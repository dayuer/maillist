package mailer

import (
	"context"
	"errors"
	"testing"

	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/rules"
)

func TestNewChannelExposesItsParts(t *testing.T) {
	fake := NewFake()
	quota := StaticQuota{Value: ratectl.Quota{MaxSendRate: 14, Max24HourSend: 50_000}}
	identities := StaticIdentity{Domains: []string{"example.net"}}
	channel := NewChannel(model.DefaultChannel, ChannelTypeSES, fake, quota, identities, nil)

	if channel.Name() != model.DefaultChannel || channel.Type() != ChannelTypeSES {
		t.Fatalf("name, type = %q, %q", channel.Name(), channel.Type())
	}
	ctx := context.Background()
	if _, err := channel.Send(ctx, rules.RecipientEmail{From: "a@example.net", To: "b@example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	if fake.SentCount() != 1 {
		t.Fatalf("sent through the fake %d times", fake.SentCount())
	}
	got, err := channel.Quota(ctx)
	if err != nil || got.MaxSendRate != 14 {
		t.Fatalf("quota = %+v, %v", got, err)
	}
	list, err := channel.Identities(ctx)
	if err != nil || len(VerifiedDomains(list)) != 1 {
		t.Fatalf("identities = %+v, %v", list, err)
	}
}

// With no classifier of its own a channel reads errors the way SES writes them.
func TestNewChannelDefaultsToSESClassification(t *testing.T) {
	channel := NewChannel(model.DefaultChannel, ChannelTypeSES, NewFake(), StaticQuota{}, StaticIdentity{}, nil)
	cases := map[error]rules.ErrorAction{
		&APIError{Code: "ThrottlingException", Message: "Maximum sending rate exceeded."}: rules.ErrorThrottle,
		&APIError{Code: "AccountSuspendedException", Message: "suspended"}:                rules.ErrorPause,
		&APIError{Code: "MessageRejected", Message: "Email address is not verified."}:     rules.ErrorPause,
		&APIError{Code: "MessageRejected", Message: "Illegal address"}:                    rules.ErrorFail,
		errors.New("connection reset"):                                                    rules.ErrorFail,
	}
	for err, want := range cases {
		if got := channel.Classify(err); got != want {
			t.Errorf("Classify(%v) = %s, want %s", err, got, want)
		}
	}
}

func TestNewChannelUsesItsOwnClassifier(t *testing.T) {
	pause := func(error) rules.ErrorAction { return rules.ErrorPause }
	channel := NewChannel("other", "test", NewFake(), StaticQuota{}, StaticIdentity{}, pause)
	if got := channel.Classify(errors.New("anything")); got != rules.ErrorPause {
		t.Fatalf("Classify = %s, want pause", got)
	}
}
