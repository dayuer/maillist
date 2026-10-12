package render

import (
	"regexp"
	"strings"
	"testing"

	"nibwire/internal/core/model"
)

// The newsletter fixture of tests/render-email.test.mjs.
var testNewsletter = model.Newsletter{Name: "Jane Doe", PostalAddress: "1 Main St, Paris"}

// tests/render-email.test.mjs "markdown emails get a footer with the unsubscribe placeholder and
// address".
func TestMarkdownEmailsGetAFooterWithTheUnsubscribePlaceholderAndAddress(t *testing.T) {
	r := Email(EmailInput{
		Format:      model.FormatMarkdown,
		Body:        "# Hello\n\nA [link](https://example.com).",
		Subject:     "Hi",
		PreviewText: "Preview",
		Newsletter:  testNewsletter,
	})
	if !strings.Contains(r.HTML, "<h1>Hello</h1>") {
		t.Errorf("heading missing: %s", r.HTML)
	}
	if !strings.Contains(r.HTML, UnsubscribePlaceholder) {
		t.Errorf("placeholder missing: %s", r.HTML)
	}
	if !strings.Contains(r.HTML, "1 Main St, Paris") {
		t.Errorf("address missing: %s", r.HTML)
	}
	if !strings.Contains(r.Text, "link (https://example.com)") {
		t.Errorf("text part = %q", r.Text)
	}
	if !strings.Contains(r.Text, "Unsubscribe: "+UnsubscribePlaceholder) {
		t.Errorf("text footer = %q", r.Text)
	}
	if r.Bytes <= 0 {
		t.Errorf("bytes = %d", r.Bytes)
	}
}

// tests/render-email.test.mjs "per-recipient unsubscribe URLs replace the placeholder in text".
func TestPerRecipientUnsubscribeURLsReplaceThePlaceholderInText(t *testing.T) {
	r := Email(EmailInput{
		Format:         model.FormatMarkdown,
		Body:           "Hi",
		Newsletter:     testNewsletter,
		UnsubscribeURL: "https://mail.example.com/unsubscribe?token=abc",
	})
	if !strings.Contains(r.HTML, "https://mail.example.com/unsubscribe?token=abc") {
		t.Errorf("url missing: %s", r.HTML)
	}
	if strings.Contains(r.Text, UnsubscribePlaceholder) {
		t.Errorf("placeholder survived in text: %q", r.Text)
	}
}

// tests/render-email.test.mjs "HTML emails keep their design and get the footer before </body>".
func TestHTMLEmailsKeepTheirDesignAndGetTheFooterBeforeBodyClose(t *testing.T) {
	r := Email(EmailInput{
		Format:      model.FormatHTML,
		Body:        "<html><body><p>Issue</p></body></html>",
		PreviewText: "This week",
		Newsletter:  testNewsletter,
	})
	if !regexp.MustCompile(`<body><div style="display:none`).MatchString(r.HTML) {
		t.Errorf("preheader not first in body: %s", r.HTML)
	}
	if strings.Index(r.HTML, UnsubscribePlaceholder) > strings.Index(r.HTML, "</body>") {
		t.Errorf("footer must come before </body>: %s", r.HTML)
	}
}

// tests/render-email.test.mjs "HTML emails with their own unsubscribe link are not changed".
func TestHTMLEmailsWithTheirOwnUnsubscribeLinkAreNotChanged(t *testing.T) {
	source := `<html><body><a href="` + UnsubscribePlaceholder + `">Leave</a></body></html>`
	if got := Email(EmailInput{Format: model.FormatHTML, Body: source, Newsletter: testNewsletter}).HTML; got != source {
		t.Errorf("got %s", got)
	}
}

// tests/render-email.test.mjs "web version has no subscriber footer".
func TestWebVersionHasNoSubscriberFooter(t *testing.T) {
	r := Email(EmailInput{
		Format: model.FormatMarkdown, Body: "Hello", Subject: "Hi",
		Newsletter: testNewsletter, IsWebVersion: true,
	})
	if strings.Contains(r.HTML, UnsubscribePlaceholder) {
		t.Errorf("placeholder in web version: %s", r.HTML)
	}
	if strings.Contains(r.HTML, "You're receiving this") {
		t.Errorf("subscriber footer in web version: %s", r.HTML)
	}
}

// tests/render-email.test.mjs "emails link to their web version at the top, the web version doesn't".
func TestEmailsLinkToTheirWebVersionAtTheTop(t *testing.T) {
	const webURL = "https://news.example.com/p/issue-12"
	markdown := Email(EmailInput{
		Format: model.FormatMarkdown, Body: "Hello", Subject: "Hi",
		Newsletter: testNewsletter, WebURL: webURL,
	})
	if strings.Index(markdown.HTML, `href="`+webURL+`"`) > strings.Index(markdown.HTML, "Hello") {
		t.Errorf("read-online link must come first: %s", markdown.HTML)
	}
	if !strings.Contains(markdown.HTML, ">Read online</a>") {
		t.Errorf("read-online link missing: %s", markdown.HTML)
	}
	if !strings.HasPrefix(markdown.Text, "Read online: "+webURL+"\n\nHello") {
		t.Errorf("text = %q", markdown.Text)
	}

	htmlEmail := Email(EmailInput{
		Format: model.FormatHTML, Body: "<html><body><p>Issue</p></body></html>",
		PreviewText: "This week", Newsletter: testNewsletter, WebURL: webURL,
	})
	if !regexp.MustCompile(`(?s)<body><div style="display:none.*?</div><div[^>]*><a href="https://news\.example\.com/p/issue-12"`).MatchString(htmlEmail.HTML) {
		t.Errorf("html read-online link misplaced: %s", htmlEmail.HTML)
	}
	if !strings.HasPrefix(htmlEmail.Text, "Read online (https://news.example.com/p/issue-12)") {
		t.Errorf("the hidden preview text must stay out of the text part: %q", htmlEmail.Text)
	}
	if strings.Contains(htmlEmail.Text, "&zwnj;") {
		t.Errorf("padding leaked into text: %q", htmlEmail.Text)
	}

	if strings.Contains(Email(EmailInput{
		Format: model.FormatMarkdown, Body: "Hello", Newsletter: testNewsletter,
		WebURL: webURL, IsWebVersion: true,
	}).HTML, "Read online") {
		t.Error("the web version must not link to itself")
	}
	if strings.Contains(Email(EmailInput{
		Format: model.FormatMarkdown, Body: "Hello", Newsletter: testNewsletter,
	}).HTML, "Read online") {
		t.Error("an email without a web url must not link one")
	}
}

// tests/render-email.test.mjs "generated HTML prints the subject and preview text, and email-only
// parts stay out of the web version".
func TestGeneratedHTMLPrintsTheSubjectAndPreviewText(t *testing.T) {
	source := "<html><head><title>{{subject}}</title></head><body><table>" +
		"<tr data-email-only><td><h1>{{subject}}</h1><p>{{preview_text}}</p></td></tr>" +
		"<tr><td>Body</td></tr></table></body></html>"
	in := EmailInput{
		Format: model.FormatHTML, Body: source,
		Subject: "Tools & <b>tricks</b>", PreviewText: "Week 12", Newsletter: testNewsletter,
	}

	email := Email(in).HTML
	if !strings.Contains(email, "<h1>Tools &amp; &lt;b&gt;tricks&lt;/b&gt;</h1><p>Week 12</p>") {
		t.Errorf("subject and preview text not printed: %s", email)
	}
	if strings.Contains(email, "{{subject}}") || strings.Contains(email, "{{preview_text}}") {
		t.Errorf("placeholder survived: %s", email)
	}

	webInput := in
	webInput.IsWebVersion = true
	web := Email(webInput).HTML
	if strings.Contains(web, "<h1>") {
		t.Errorf("the archive page shows the title itself: %s", web)
	}
	if !strings.Contains(web, "<title>Tools &amp; &lt;b&gt;tricks&lt;/b&gt;</title>") {
		t.Errorf("title missing: %s", web)
	}
	if !strings.Contains(web, "<td>Body</td>") {
		t.Errorf("body lost: %s", web)
	}
}

// tests/callouts.test.mjs "Markdown boxes are gray inline-styled boxes in emails". Task 1.7 left
// this to Task 1.9 with a t.Skip because it needs render.Email; it lands here. It also covers what
// tests/rich-content.test.mjs "editor issues get inline styles in emails" asserts after the plan's
// rewrite to a Markdown issue — that test's two editor-only assertions, "<li><p style=\"margin:0;\">"
// and the inline image style, come from richEmailBody, which died with the rich format.
func TestMarkdownBoxesAreInlineStyledInEmails(t *testing.T) {
	const markdown = "Intro\n\n:::box\n**Project update**\n\n- Shipped the editor\n:::\n\n:::box\nSponsor\n:::\n\nOutro"
	r := Email(EmailInput{
		Format: model.FormatMarkdown, Body: markdown,
		Newsletter: model.Newsletter{Name: "Field notes"},
	})
	if got := strings.Count(r.HTML, "border-radius:12px;background:#f4f4f5;"); got != 2 {
		t.Errorf("inline-styled boxes = %d, want 2:\n%s", got, r.HTML)
	}
	if strings.Contains(r.HTML, ":::") {
		t.Errorf("box markers leaked:\n%s", r.HTML)
	}
	if strings.Contains(r.HTML, "data-callout") {
		t.Errorf("the archive marker must not reach the email:\n%s", r.HTML)
	}
	if !strings.Contains(r.HTML, UnsubscribePlaceholder) {
		t.Errorf("the footer must be added:\n%s", r.HTML)
	}
	if !strings.Contains(r.Text, "Shipped the editor") {
		t.Errorf("text = %q", r.Text)
	}
}

// tests/rich-content.test.mjs "an empty editor document is not content".
func TestAnEmptyEditorDocumentIsNotContent(t *testing.T) {
	for body, want := range map[string]bool{
		"<p></p>": false, "": false, "<p>Hi</p>": true,
		`<img src="https://cdn.example.com/a.png">`: true,
	} {
		if got := HasContent(body); got != want {
			t.Errorf("HasContent(%q) = %v, want %v", body, got, want)
		}
	}
}
