package rules

import (
	"strings"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/render"
)

// confirmLink is the exact attribute upstream looks for to decide a confirmation email has its
// button. A Markdown link encodes the braces (%7B%7B) and an indented line becomes a code block, so
// both leave the placeholder literal in the rendered HTML and the email would go out with a dead
// button.
const confirmLink = `href="` + ConfirmURLPlaceholder + `"`

// DefaultConfirmation is the built-in confirmation email: one job, one button, and it says what
// happens to someone who didn't sign up.
func DefaultConfirmation(n model.Newsletter) model.Content {
	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = "the newsletter"
	}
	return model.Content{
		Subject:     "Confirm your subscription to " + name,
		PreviewText: "One click and you're in. Until then, you won't get " + name + ".",
		Markdown: `**One click and you're in.**

You asked to get ` + name + `. Confirm your email and the next issue lands in your inbox.

<a ` + confirmLink + ` style="display:inline-block;background:#18181b;color:#ffffff;text-decoration:none;font-weight:600;padding:12px 22px;border-radius:8px;">Confirm my subscription</a>

Didn't sign up? Ignore this email and you won't hear from us again.`,
	}
}

// ConfirmationProblem is "" when a confirmation email can go out.
func ConfirmationProblem(c model.Content) string {
	if strings.TrimSpace(c.Subject) == "" {
		return "Confirmation email needs a subject"
	}
	html := render.Email(render.EmailInput{Format: model.FormatMarkdown, Body: c.Markdown}).HTML
	if !strings.Contains(html, confirmLink) {
		return "Confirmation email needs its button: a link like <a " + confirmLink + ">Confirm</a>"
	}
	return ""
}

// ConfirmationContent is what a new subscriber gets when their newsletter asks them to confirm.
// It's the only email they get, so a newsletter can write its own; without one, or if it lost its
// button, the built-in one goes out.
func ConfirmationContent(n model.Newsletter) model.Content {
	if ConfirmationProblem(n.Confirmation) == "" {
		return model.Content{
			Subject:     strings.TrimSpace(n.Confirmation.Subject),
			PreviewText: strings.TrimSpace(n.Confirmation.PreviewText),
			Markdown:    n.Confirmation.Markdown,
		}
	}
	return DefaultConfirmation(n)
}
