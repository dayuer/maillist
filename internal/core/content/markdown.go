package content

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// Markdown renderers are built once: goldmark's parser keeps no per-parse state (the callout
// lookahead lives in parser.Context), so both are safe for concurrent use.
var (
	archiveMarkdown = newMarkdownRenderer(false)
	emailMarkdown   = newMarkdownRenderer(true)
)

func newMarkdownRenderer(isEmail bool) goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM, calloutExtension{isEmail: isEmail}),
		// No heading ids: marked, the upstream parser, doesn't add them either.
		// Raw HTML is allowed here: the result is cleaned afterwards (CleanContent), and HTML-format
		// issues never come through this path at all.
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
}

// MarkdownToHTML renders Markdown with the :::box callout extension: GFM tables, strikethrough and
// autolinks, raw HTML passed through. isEmail selects the inline-styled box over the archive's
// data-callout div.
func MarkdownToHTML(src string, isEmail bool) string {
	renderer := archiveMarkdown
	if isEmail {
		renderer = emailMarkdown
	}
	var b strings.Builder
	if err := renderer.Convert([]byte(src), &b); err != nil {
		// Upstream shows the Markdown as plain text when its parser gives up; same fallback here.
		return "<p>" + escapeText(src) + "</p>\n"
	}
	return b.String()
}

// escapeText mirrors upstream escapeText: &, < and > only.
func escapeText(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}
