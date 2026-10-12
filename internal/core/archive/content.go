package archive

import (
	"regexp"
	"strings"

	"nibwire/internal/core/content"
	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
)

// Body is what an issue page shows, and how it has to be shown.
//
// Two kinds, because the two formats have different owners (design §12, D12): a Markdown issue is
// ours, rendered and cleaned, and goes straight into the page. An HTML issue is somebody else's
// whole design — often an entire email — so it keeps it, inside a sandboxed frame, with only its
// links made frame-safe.
type Body struct {
	Kind string // "article" or "frame"
	HTML string
}

// The two kinds, as the templates and the tests name them.
const (
	KindArticle = "article"
	KindFrame   = "frame"
)

// imgPattern is every picture in a body, in document order.
var imgPattern = regexp.MustCompile(`(?i)<img\b([^>]*?)/?>`)

// headPattern is the opening <head> tag the frame's base target goes into.
var headPattern = regexp.MustCompile(`(?i)<head([^>]*)>`)

// prioritizeImages is upstream prioritizeImages: the first picture is the hero, so it loads right
// away, and the rest wait until they scroll into view.
func prioritizeImages(html string) string {
	index := 0
	return imgPattern.ReplaceAllStringFunc(html, func(tag string) string {
		index++
		attributes := imgPattern.FindStringSubmatch(tag)[1]
		loading := ` loading="lazy"`
		if index == 1 {
			loading = ` fetchpriority="high"`
		}
		return "<img" + attributes + loading + ` decoding="async">`
	})
}

// RenderIssueBody is upstream renderIssueBody for the two formats this port has.
//
// Upstream's third branch was the editor's own "rich" format; it died with the editor (design §2), so
// a Markdown issue and a "rich" one are the same thing here.
func RenderIssueBody(c model.Campaign, n model.Newsletter) Body {
	if c.Format != model.FormatHTML {
		// Upstream runs its regex cleaner, then adds the picture attributes, then sanitizes. This port
		// has one parse-based pass for the archive instead: it is the same allow list, so the two
		// cleaner runs upstream needs would only re-serialize the same markup — and the second one is
		// the pass that has to keep the attributes the first would drop.
		return Body{Kind: KindArticle, HTML: content.SanitizeIssueHTML(prioritizeImages(content.MarkdownToHTML(c.Body, false)))}
	}
	return Body{Kind: KindFrame, HTML: frameHTML(c, n)}
}

// frameHTML is the HTML issue's page body: the sent email, with the parts only an email needs taken
// out, its links made safe for a frame, and a base target so a click opens a new tab rather than
// navigating the frame it is in.
func frameHTML(c model.Campaign, n model.Newsletter) string {
	// Rendered as the web version: no subscriber footer, no preheader, no Read-online link — the page
	// already is the online version.
	rendered := render.Email(render.EmailInput{
		Format: model.FormatHTML, Body: c.Body, Subject: c.Subject,
		PreviewText: c.PreviewText, Newsletter: n, IsWebVersion: true,
	}).HTML

	framed := content.KeepSafeURLs(withResponsiveImages(rendered))
	framed = content.BeforeLastTagEnd(framed, func(part string) string {
		return headPattern.ReplaceAllStringFunc(part, func(head string) string {
			return head + `<base target="_blank">`
		})
	})
	// The unsubscribe placeholder has no meaning on a page this deployment serves to anyone: the
	// email's own footer carries the real link.
	return strings.ReplaceAll(framed, render.UnsubscribePlaceholder, "#")
}

// withResponsiveImages is upstream withResponsiveImages: HTML issues built elsewhere may rely on
// their own site's CSS to keep pictures inside the column.
func withResponsiveImages(html string) string {
	const style = "<style>img{max-width:100%;height:auto}</style>"
	return content.BeforeLastTagEnd(html, func(part string) string {
		return headPattern.ReplaceAllStringFunc(part, func(head string) string {
			return head + style
		})
	})
}
