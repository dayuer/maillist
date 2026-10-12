// Package checks holds the pre-send checklist of a campaign.
package checks

import (
	"strings"
	"unicode/utf16"

	"nibwire/internal/core/model"
)

// Check is one row of the pre-send checklist.
type Check struct {
	// Upstream ids in order: subject, subject-length, preview, content, unsubscribe, size, address,
	// domain, test, sending. subject-length, preview and test are warnings; the other 7 block.
	ID       string
	OK       bool
	Blocking bool
	Message  string
}

// Rendered is the campaign as an email, reduced to the plain facts the checklist reads off it: how
// big it is, whether the body has anything in it, and whether the unsubscribe link is in the HTML.
// Rendering itself stays with the caller — internal/presend does it, both surfaces go through that
// one assembly — so this package never looks at how the email was produced (phase 9 #44).
type Rendered struct {
	HTML  string
	Bytes int
	// Placeholder is the exact text HTML carries where a recipient's unsubscribe link goes. An empty
	// one is a caller that forgot to pass it, and it cannot pass the check: every string contains "".
	Placeholder string
	// HasContent is whether the body has words or a picture once parsed, which is what upstream's
	// hasContent asks: the editor saves "<p></p>" for an empty document.
	HasContent bool
}

// Input is everything the checklist looks at. DomainVerified is whether SES has verified the
// sending domain; Audience is how many contacts the campaign would reach.
type Input struct {
	Campaign       model.Campaign
	Newsletter     model.Newsletter
	Rendered       Rendered
	SendingOn      bool
	DomainVerified bool
	Audience       int
	Limits         model.Limits
}

// Run returns the checklist in a fixed order, whether or not each row passes.
func Run(in Input) []Check {
	subject := strings.TrimSpace(in.Campaign.Subject)
	// Upstream measures subject.length in UTF-16 code units, which is what a JavaScript string
	// length is: an emoji outside the BMP counts as two. Counting runes made a 36-emoji subject look
	// like 36 characters instead of 72, so the warning never fired (the phase 1-3 audit's FI-A4).
	subjectLength := len(utf16.Encode([]rune(subject)))
	return []Check{
		{ID: "subject", OK: subject != "", Blocking: true, Message: "Add a subject"},
		{ID: "subject-length", OK: subjectLength <= 70},
		{ID: "preview", OK: strings.TrimSpace(in.Campaign.PreviewText) != ""},
		{ID: "content", OK: in.Rendered.HasContent, Blocking: true, Message: "Add content"},
		{
			ID:       "unsubscribe",
			OK:       in.Rendered.Placeholder != "" && strings.Contains(in.Rendered.HTML, in.Rendered.Placeholder),
			Blocking: true, Message: "Add " + in.Rendered.Placeholder + " to the HTML",
		},
		{
			ID: "size", OK: in.Rendered.Bytes <= in.Limits.MaxEmailBytes,
			Blocking: true, Message: "Gmail clips emails over 100 KB",
		},
		{
			ID: "address", OK: strings.TrimSpace(in.Newsletter.PostalAddress) != "",
			Blocking: true, Message: "Add a postal address to the newsletter",
		},
		{ID: "domain", OK: in.DomainVerified, Blocking: true, Message: "Verify the sending domain in SES"},
		{ID: "test", OK: in.Campaign.TestSentAt != nil},
		{ID: "sending", OK: in.SendingOn, Blocking: true, Message: "Sending is off"},
	}
}

// Blocked returns the first failing blocking check.
func Blocked(cs []Check) (Check, bool) {
	for _, c := range cs {
		if !c.OK && c.Blocking {
			return c, true
		}
	}
	return Check{}, false
}
