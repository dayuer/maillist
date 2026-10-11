// Package render turns a campaign body into the email that goes out: HTML, its plain-text
// alternative, click and open tracking, and the webmail link of a confirmation email.
package render

import (
	"strings"

	"mailcheap/internal/core/content"
	"mailcheap/internal/core/model"
)

// Placeholders a generated email prints instead of its own copy, so editing the campaign changes
// the email too.
const (
	UnsubscribePlaceholder = "{{unsubscribe_url}}"
	SubjectPlaceholder     = "{{subject}}"
	PreviewTextPlaceholder = "{{preview_text}}"
)

// EmailInput is one campaign body plus what the shell around it needs.
type EmailInput struct {
	Format         model.Format
	Body           string // Markdown or HTML, per Format
	Subject        string
	PreviewText    string
	Newsletter     model.Newsletter
	UnsubscribeURL string // "" = the per-recipient placeholder
	WebURL         string // the issue's archive page, linked at the top; "" = none
	IsWebVersion   bool   // archive: same design, no subscriber footer
}

// Rendered is an email ready to send.
type Rendered struct {
	HTML  string
	Text  string
	Bytes int
}

// Email renders one campaign body. Upstream's third format, "rich", died with the editor.
func Email(in EmailInput) Rendered {
	unsubscribeURL := in.UnsubscribeURL
	if unsubscribeURL == "" {
		unsubscribeURL = UnsubscribePlaceholder
	}
	webURL := in.WebURL
	if in.IsWebVersion {
		webURL = ""
	}

	if in.Format == model.FormatHTML {
		filled := fillFields(in.Body, in.Subject, in.PreviewText)
		var rendered string
		if in.IsWebVersion {
			rendered = content.BeforeLastTagEnd(filled, func(part string) string {
				return content.ReplaceElements(part, emailOnlyElement)
			})
		} else {
			rendered = withFooter(filled, in.Newsletter, unsubscribeURL, in.PreviewText, webURL)
		}
		text := HTMLToText(strings.ReplaceAll(rendered, UnsubscribePlaceholder, unsubscribeURL))
		return Rendered{HTML: rendered, Text: text, Bytes: len(rendered)}
	}

	bodyHTML := content.MarkdownToHTML(in.Body, true)
	footer := ""
	if !in.IsWebVersion {
		footer = footerHTML(in.Newsletter, unsubscribeURL)
	}
	rendered := wrapMarkdownBody(bodyHTML, in.Subject, in.PreviewText, footer, webURL)
	readOnlineText := ""
	if webURL != "" {
		readOnlineText = "Read online: " + webURL + "\n\n"
	}
	text := readOnlineText + HTMLToText(bodyHTML) + "\n\n" + footerText(in.Newsletter, unsubscribeURL)
	if in.IsWebVersion {
		text = HTMLToText(bodyHTML)
	}
	return Rendered{HTML: rendered, Text: text, Bytes: len(rendered)}
}

// EscapeHTML escapes the five characters that must never reach the document as markup.
func EscapeHTML(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	return strings.ReplaceAll(value, "'", "&#39;")
}
