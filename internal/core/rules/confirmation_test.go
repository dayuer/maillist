package rules

import (
	"strings"
	"testing"

	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
)

const (
	confirmContactID    = "64f1a2b3c4d5e6f7a8b9c0a1"
	confirmNewsletterID = "64f1a2b3c4d5e6f7a8b9c0b1"
	confirmAppURL       = "https://nibwire.test"
)

func confirmationLetter(name string, confirmation model.Content) model.Newsletter {
	return model.Newsletter{ID: confirmNewsletterID, Name: name, Confirmation: confirmation}
}

// tests/confirmation.test.mjs "the confirmation email carries a clean link in both versions".
func TestTheConfirmationEmailCarriesACleanLink(t *testing.T) {
	content := ConfirmationContent(confirmationLetter("Field notes", model.Content{}))
	if content.Subject != "Confirm your subscription to Field notes" {
		t.Errorf("subject = %q", content.Subject)
	}
	if !strings.Contains(content.Markdown, ConfirmURLPlaceholder) {
		t.Errorf("markdown = %q", content.Markdown)
	}

	email := confirmationEmail(t, content, "https://nibwire.test/confirm?token=abc")
	if !strings.Contains(email.HTML, `href="https://nibwire.test/confirm?token=abc"`) {
		t.Errorf("confirm link not filled: %s", email.HTML)
	}
	if !strings.Contains(email.Text, "Confirm my subscription (https://nibwire.test/confirm?token=abc)") {
		t.Errorf("text = %q", email.Text)
	}
	if strings.Contains(email.HTML, "/c/") || strings.Contains(email.HTML, "/o/") {
		t.Errorf("a confirmation email carries no tracking redirect or pixel: %s", email.HTML)
	}
	// Upstream renders the template to check the button survives rendering.
	if !strings.Contains(renderMarkdown(t, content.Markdown), ConfirmURLPlaceholder) {
		t.Error("the placeholder must survive rendering as an HTML link")
	}
}

// tests/confirmation.test.mjs "a newsletter's own confirmation email goes out with each
// subscriber's link".
func TestANewslettersOwnConfirmationEmailGoesOut(t *testing.T) {
	custom := model.Content{
		Subject:     " [Action Required] Confirm your Field notes subscription ",
		PreviewText: "One click and you're in.",
		Markdown: `<p style="text-align:center"><img src="https://nibwire.test/email/banner.png" alt="Field notes" width="600" height="200"></p>

Confirm your email to get the next issue:

<a href="` + ConfirmURLPlaceholder + `" style="display:inline-block;">Confirm my subscription</a>

Every Sunday, you'll get:

- One idea about building things`,
	}
	content := ConfirmationContent(confirmationLetter("Field notes", custom))
	if content.Subject != "[Action Required] Confirm your Field notes subscription" {
		t.Errorf("subject = %q", content.Subject)
	}
	if content.PreviewText != "One click and you're in." {
		t.Errorf("previewText = %q", content.PreviewText)
	}

	email := confirmationEmail(t, content, "https://nibwire.test/confirm?token=xyz")
	for label, want := range map[string]string{
		"confirm link":  `href="https://nibwire.test/confirm?token=xyz"`,
		"banner image":  `src="https://nibwire.test/email/banner.png"`,
		"markdown list": "<li>One idea about building things</li>",
	} {
		if !strings.Contains(email.HTML, want) {
			t.Errorf("%s missing: %s", label, email.HTML)
		}
	}
	if !strings.Contains(email.Text, "Confirm my subscription (https://nibwire.test/confirm?token=xyz)") {
		t.Errorf("text = %q", email.Text)
	}
	if strings.Contains(email.HTML, ConfirmURLPlaceholder) || strings.Contains(email.Text, ConfirmURLPlaceholder) {
		t.Error("placeholder survived")
	}
	if strings.Contains(email.HTML, "/c/") || strings.Contains(email.HTML, "/o/") {
		t.Error("a confirmation email carries no tracking")
	}
	if !strings.Contains(email.Headers["List-Unsubscribe"], "/api/unsubscribe?token=") {
		t.Errorf("headers = %v", email.Headers)
	}
}

// tests/confirmation.test.mjs "a confirmation email without its button can't be saved and never
// goes out".
func TestAConfirmationEmailWithoutItsButtonNeverGoesOut(t *testing.T) {
	builtIn := ConfirmationContent(confirmationLetter("Field notes", model.Content{}))
	if got := ConfirmationProblem(builtIn); got != "" {
		t.Errorf("the built-in email must be fine: %q", got)
	}
	blankSubject := builtIn
	blankSubject.Subject = " "
	if got := ConfirmationProblem(blankSubject); got != "Confirmation email needs a subject" {
		t.Errorf("got %q", got)
	}

	const prefix = "Confirmation email needs its button: a link like <a "
	for label, markdown := range map[string]string{
		"no button":     "Welcome to Field notes",
		"markdown link": "[Confirm](" + ConfirmURLPlaceholder + ")",
		"indented line": `    <a href="` + ConfirmURLPlaceholder + `">Confirm</a>`,
	} {
		candidate := builtIn
		candidate.Markdown = markdown
		got := ConfirmationProblem(candidate)
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("%s: got %q", label, got)
		}
	}

	// A newsletter whose own email lost its button gets the built-in one instead.
	withoutButton := confirmationLetter("Field notes", model.Content{Subject: "Welcome", Markdown: "No button here"})
	if got := ConfirmationContent(withoutButton); got != builtIn {
		t.Errorf("got %+v", got)
	}
}

func TestConfirmationProblemNeedsASubjectFirst(t *testing.T) {
	// An empty Content (no newsletter-written email) has no subject, so that is the problem
	// reported, not the missing button.
	if got := ConfirmationProblem(model.Content{}); got != "Confirmation email needs a subject" {
		t.Errorf("got %q", got)
	}
}

// confirmationEmail is upstream's buildRecipientEmail({campaign: content, ..., messageId: null,
// confirmUrl}) call.
func confirmationEmail(t *testing.T, content model.Content, confirmURL string) RecipientEmail {
	t.Helper()
	return build(t, RecipientInput{
		AppURL: confirmAppURL,
		Campaign: model.Campaign{
			Format: model.FormatMarkdown, Subject: content.Subject,
			PreviewText: content.PreviewText, Body: content.Markdown,
		},
		Newsletter: model.Newsletter{
			ID: confirmNewsletterID, Name: "Field notes", FromEmail: "hi@news.example.org",
		},
		Contact:    model.Contact{ID: confirmContactID, Email: "reader@example.com"},
		ConfirmURL: confirmURL,
	})
}

// renderMarkdown is upstream's renderEmail({markdown}).
func renderMarkdown(t *testing.T, markdown string) string {
	t.Helper()
	return render.Email(render.EmailInput{Format: model.FormatMarkdown, Body: markdown}).HTML
}
