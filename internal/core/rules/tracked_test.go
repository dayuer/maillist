package rules

import (
	"strings"
	"testing"

	"mailcheap/internal/core/model"
)

const trackMessageID = "66f1a2b3c4d5e6f7a8b9c0d1"

// tests/tracking.test.mjs "sent emails are tracked and keep a direct unsubscribe link". The
// upstream fixture used format "rich"; the rich format died with the editor, so this is the same
// case written as Markdown.
func TestSentEmailsAreTrackedAndKeepADirectUnsubscribeLink(t *testing.T) {
	s := testSigner(t)
	email, err := BuildRecipientEmail(s, RecipientInput{
		AppURL: testAppURL,
		Campaign: model.Campaign{
			Format: model.FormatMarkdown, Subject: "Hi",
			Body: "Hi\n\n[Example](https://example.org)",
		},
		Newsletter: model.Newsletter{
			ID: "74f1a2b3c4d5e6f7a8b9c0d2", Name: "Field notes", FromEmail: "hi@mail.example.com",
		},
		Contact:   model.Contact{ID: "64f1a2b3c4d5e6f7a8b9c0d1", Email: "reader@example.com"},
		MessageID: trackMessageID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(email.HTML, s.OpenURL(testAppURL, trackMessageID)) {
		t.Errorf("open pixel missing: %s", email.HTML)
	}
	if !strings.Contains(email.HTML, s.ClickURL(testAppURL, trackMessageID, "https://example.org")) {
		t.Errorf("click link missing: %s", email.HTML)
	}
	if !strings.Contains(email.HTML, `href="`+email.UnsubscribeURL+`"`) {
		t.Error("the footer link must not be rewritten")
	}
	if !strings.Contains(email.Text, "https://example.org") {
		t.Errorf("the text part keeps the original links: %q", email.Text)
	}
	if strings.Contains(email.HTML, "{{unsubscribe_url}}") {
		t.Errorf("placeholder survived: %s", email.HTML)
	}
}

// A confirmation email is untracked: no pixel, no rewritten links (upstream UNTRACKED_KINDS).
func TestUntrackedMessagesCarryNoTracking(t *testing.T) {
	s := testSigner(t)
	email, err := BuildRecipientEmail(s, RecipientInput{
		AppURL:     testAppURL,
		Campaign:   testCampaign,
		Newsletter: testNewsletter,
		Contact:    testContact,
		Kind:       model.KindConfirmation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(email.HTML, s.OpenURL(testAppURL, "ignored")) {
		t.Error("an untracked message must carry no pixel")
	}
	if !Untracked(model.KindConfirmation) {
		t.Error("confirmation is untracked")
	}
}
