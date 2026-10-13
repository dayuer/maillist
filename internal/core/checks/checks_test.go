package checks

import (
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/model"
)

var readyNewsletter = model.Newsletter{
	Name: "Jane Doe", PostalAddress: "1 Main St, Paris",
}

// unsubscribePlaceholder is the text a rendered email carries where the unsubscribe link goes. The
// checklist is told it rather than importing the renderer, so these tests state the literal too; the
// one place it is tied back to an actual render is internal/presend's test.
const unsubscribePlaceholder = "{{unsubscribe_url}}"

// renderedFor is the plain form of a body, the way internal/presend hands it over: the renderer
// stays outside this package (phase 9 #44).
func renderedFor(body string) Rendered {
	return Rendered{
		HTML:        "<p>" + body + "</p>\n" + unsubscribePlaceholder,
		Bytes:       len(body),
		Placeholder: unsubscribePlaceholder,
		HasContent:  strings.TrimSpace(body) != "",
	}
}

// tests/render-email.test.mjs "presend checks block on missing essentials and sending off".
func TestPresendChecksBlockOnMissingEssentialsAndSendingOff(t *testing.T) {
	cs := Run(Input{
		Campaign:           model.Campaign{Subject: "", PreviewText: "", Body: ""},
		Newsletter:         model.Newsletter{},
		Rendered:           renderedFor(""),
		SendingOn:          false,
		UnverifiedChannels: []string{model.DefaultChannel},
		Limits:             model.Limits{MaxEmailBytes: 100_000},
	})
	var blocking []string
	for _, c := range cs {
		if c.Blocking && !c.OK {
			blocking = append(blocking, c.ID)
		}
	}
	want := []string{"subject", "content", "address", "domain", "sending"}
	if strings.Join(blocking, ",") != strings.Join(want, ",") {
		t.Fatalf("blocking = %v, want %v", blocking, want)
	}
}

// The domain row names the channels that have not verified the sending domain: "the SES account"
// keeps its old wording, and a channel of its own is named.
func TestTheDomainCheckNamesEveryUnverifiedChannel(t *testing.T) {
	campaign := model.Campaign{Subject: "Hello", PreviewText: "Hi", Body: "Text"}
	for name, unverified := range map[string]struct {
		channels []string
		want     string
	}{
		"the default channel": {[]string{model.DefaultChannel}, "Verify the sending domain in SES"},
		"one channel":         {[]string{"aws2"}, "Verify the sending domain in aws2"},
		"two channels":        {[]string{"aws2", "back"}, "Verify the sending domain in aws2, back"},
	} {
		cs := Run(Input{
			Campaign: campaign, Newsletter: readyNewsletter, Rendered: renderedFor("Text"),
			SendingOn: true, UnverifiedChannels: unverified.channels,
			Limits: model.Limits{MaxEmailBytes: 100_000},
		})
		var domain Check
		for _, c := range cs {
			if c.ID == "domain" {
				domain = c
			}
		}
		if domain.OK || domain.Message != unverified.want {
			t.Errorf("%s: domain = %+v, want %q", name, domain, unverified.want)
		}
	}
}

// tests/render-email.test.mjs "presend checks pass when everything is ready".
func TestPresendChecksPassWhenEverythingIsReady(t *testing.T) {
	campaign := model.Campaign{
		Subject: "Hello", PreviewText: "Hi", Body: "Text",
		TestSentAt: func() *time.Time { v := time.Now(); return &v }(),
	}
	cs := Run(Input{
		Campaign:   campaign,
		Newsletter: readyNewsletter,
		Rendered:   renderedFor(campaign.Body),
		SendingOn:  true,
		Limits:     model.Limits{MaxEmailBytes: 100_000},
	})
	if _, blocked := Blocked(cs); blocked {
		t.Fatalf("unexpected block in %+v", cs)
	}
	for _, c := range cs {
		if !c.OK {
			t.Errorf("%s not ok", c.ID)
		}
	}
}

// Added by the plan: Run always returns the same ten rows in the same order.
func TestRunReturnsTenChecksInOrder(t *testing.T) {
	cs := Run(Input{Limits: model.Limits{MaxEmailBytes: 100_000}})
	want := []struct {
		id       string
		blocking bool
	}{
		{"subject", true}, {"subject-length", false}, {"preview", false}, {"content", true},
		{"unsubscribe", true}, {"size", true}, {"address", true}, {"domain", true},
		{"test", false}, {"sending", true},
	}
	if len(cs) != len(want) {
		t.Fatalf("got %d checks, want %d", len(cs), len(want))
	}
	for i, w := range want {
		if cs[i].ID != w.id || cs[i].Blocking != w.blocking {
			t.Errorf("check %d = %+v, want id %q blocking %v", i, cs[i], w.id, w.blocking)
		}
	}
}

func TestBlockedReturnsTheFirstBlockingFailure(t *testing.T) {
	cs := Run(Input{
		Campaign:   model.Campaign{Subject: "Hello", Body: "Text", PreviewText: "Hi"},
		Newsletter: readyNewsletter,
		Rendered:   renderedFor("Text"),
		SendingOn:  false,
		Limits:     model.Limits{MaxEmailBytes: 100_000},
	})
	c, blocked := Blocked(cs)
	if !blocked || c.ID != "sending" {
		t.Fatalf("blocked = %v %+v", blocked, c)
	}
}
