package content

import (
	"strings"
	"time"

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

// MarkdownBudget is how long one body may spend being parsed before it is shown as plain text.
//
// A CommonMark parser has shapes it is quadratic on, and a body may be 100 KB: measured here, 100 KB
// of "[a](" takes 3.06 s while 100 KB of prose takes 1 ms, and nested quotes and runs of "*a" are
// between the two. Upstream's parser stops after a second on the same shapes and shows the Markdown as
// text; this is that budget.
const MarkdownBudget = time.Second

// MarkdownToHTML renders Markdown with the :::box callout extension: GFM tables, strikethrough and
// autolinks, raw HTML passed through. isEmail selects the inline-styled box over the archive's
// data-callout div.
//
// It goes through the process's Cache: a body is parsed once however many times it is rendered, and a
// body whose parse overruns the budget is shown as text rather than allowed to become another
// minutes-long goroutine on somebody else's request. See markdown_cache.go for why all three of those
// are the same decision.
func MarkdownToHTML(src string, isEmail bool) string {
	rendered, _ := defaultCache.Render(src, isEmail)
	return rendered
}

func convertMarkdown(src string, isEmail bool) string {
	renderer := archiveMarkdown
	if isEmail {
		renderer = emailMarkdown
	}
	var b strings.Builder
	if err := renderer.Convert([]byte(src), &b); err != nil {
		// Upstream shows the Markdown as plain text when its parser gives up; same fallback here.
		return plainText(src)
	}
	return b.String()
}

// plainText is the fallback both a failed and an over-budget parse get: the source, escaped, so it
// reads as the text somebody wrote rather than as markup that was never rendered.
func plainText(src string) string {
	return "<p>" + escapeText(src) + "</p>\n"
}

// escapeText mirrors upstream escapeText: &, < and > only.
func escapeText(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}
