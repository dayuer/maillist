package checks

import (
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
)

var readyNewsletter = model.Newsletter{
	Name: "Jane Doe", PostalAddress: "1 Main St, Paris",
}

func renderBody(body string, n model.Newsletter) render.Rendered {
	return render.Email(render.EmailInput{Format: model.FormatMarkdown, Body: body, Newsletter: n})
}

// tests/render-email.test.mjs "presend checks block on missing essentials and sending off".
func TestPresendChecksBlockOnMissingEssentialsAndSendingOff(t *testing.T) {
	cs := Run(Input{
		Campaign:       model.Campaign{Subject: "", PreviewText: "", Body: ""},
		Newsletter:     model.Newsletter{},
		Rendered:       renderBody("", model.Newsletter{}),
		SendingOn:      false,
		DomainVerified: false,
		Limits:         model.Limits{MaxEmailBytes: 100_000},
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

// tests/render-email.test.mjs "presend checks pass when everything is ready".
func TestPresendChecksPassWhenEverythingIsReady(t *testing.T) {
	campaign := model.Campaign{
		Subject: "Hello", PreviewText: "Hi", Body: "Text",
		TestSentAt: func() *time.Time { v := time.Now(); return &v }(),
	}
	cs := Run(Input{
		Campaign:       campaign,
		Newsletter:     readyNewsletter,
		Rendered:       renderBody(campaign.Body, readyNewsletter),
		SendingOn:      true,
		DomainVerified: true,
		Limits:         model.Limits{MaxEmailBytes: 100_000},
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
		Campaign:       model.Campaign{Subject: "Hello", Body: "Text", PreviewText: "Hi"},
		Newsletter:     readyNewsletter,
		Rendered:       renderBody("Text", readyNewsletter),
		SendingOn:      false,
		DomainVerified: true,
		Limits:         model.Limits{MaxEmailBytes: 100_000},
	})
	c, blocked := Blocked(cs)
	if !blocked || c.ID != "sending" {
		t.Fatalf("blocked = %v %+v", blocked, c)
	}
}
