package content

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// contentTags mirrors upstream CONTENT_TAGS (lib/clean-html.js): the elements issues are written
// with. Any other tag goes, keeping its text.
var contentTags = []string{
	"a", "b", "blockquote", "br", "code", "del", "div", "em",
	"h1", "h2", "h3", "h4", "h5", "h6", "hr", "i", "img", "li", "mark",
	"ol", "p", "pre", "s", "small", "span", "strong", "sub", "sup",
	"table", "tbody", "td", "th", "thead", "tr", "u", "ul",
}

// cleanPolicy mirrors upstream clean-html.js: CONTENT_TAGS with KEPT_ATTRIBUTES. Inline styles and
// classes are the source's theme, not ours, so style is never allowed; `div` keeps only its
// data-callout marker, and `img` is checked separately for an http(s) src.
var cleanPolicy = newCleanPolicy()

func newCleanPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(contentTags...)
	// Upstream keeps a tag whose attributes were all dropped (`<mark>` keeps its text), so an
	// element must be allowed with no attributes at all.
	p.AllowNoAttrs().OnElements(contentTags...)
	p.AllowAttrs("href", "target", "rel").OnElements("a")
	p.AllowAttrs("src", "alt", "width", "height").OnElements("img")
	p.AllowAttrs("start").OnElements("ol")
	p.AllowAttrs("colspan", "rowspan").OnElements("td", "th")
	p.AllowAttrs("id").OnElements("h2", "h3", "h4")
	p.AllowAttrs("data-callout").OnElements("div")
	// Upstream's href check is /^(https?:|mailto:|#|\/)/i and its src check is https:// only;
	// # and / are relative URLs, which bluemonday allows alongside these schemes.
	p.AllowURLSchemes("http", "https", "mailto")
	return p
}

// Upstream keeps its own canonical shape for cleaned HTML; bluemonday writes <br/> instead of the
// <br /> upstream produces, so void tags are normalized to match.
var voidTagPattern = regexp.MustCompile(`<(br|hr)\s*/?>`)

var (
	imgTagPattern = regexp.MustCompile(`(?i)<img\b[^>]*>`)
	imgSrcPattern = regexp.MustCompile(`(?i)\bsrc="https?://[^"]*"`)
)

// Pictures load from the web only; blob: URLs are uploads that never finished.
func dropOffWebImages(html string) string {
	return imgTagPattern.ReplaceAllStringFunc(html, func(tag string) string {
		if imgSrcPattern.MatchString(tag) {
			return tag
		}
		return ""
	})
}

var trailingEmptyParagraphs = regexp.MustCompile(`(?:<p></p>\s*)+$`)

// CleanContent is upstream cleanRichHtml: the allow list above applied to rendered Markdown or to
// HTML the editor wrote, with blank lines kept as spacing and the trailing empty paragraph dropped.
func CleanContent(html string) string {
	cleaned := cleanPolicy.Sanitize(html)
	cleaned = dropOffWebImages(cleaned)
	cleaned = voidTagPattern.ReplaceAllString(cleaned, "<$1 />")
	cleaned = trailingEmptyParagraphs.ReplaceAllString(cleaned, "")
	cleaned = strings.ReplaceAll(cleaned, "<p></p>", "<p><br></p>")
	return strings.TrimSpace(cleaned)
}

// Upstream SAFE_URL_PATTERN: kept by KeepSafeURLs.
var safeURLPattern = regexp.MustCompile(`(?i)^(https?:|mailto:|tel:|#|/|data:image/)`)

// Upstream's URL_ATTRIBUTE_PATTERN uses a lookbehind for the delimiter before the attribute name;
// RE2 has none, so the delimiter is captured instead and written back unchanged.
var urlAttributePattern = regexp.MustCompile(
	`(^|[\s/"'])((?:xlink:)?(?:href|src|action|formaction|background|poster)\s*=\s*)("[^"]*"|'[^']*'|[^\s"'>]+)`)

// KeepSafeURLs drops URL attributes outside SAFE_URL_PATTERN — javascript:, non-image data: and
// anything else — leaving the rest of the document as it is. HTML-format issues keep their own
// design; only their links are made frame-safe before they go into the archive's sandboxed iframe.
func KeepSafeURLs(html string) string {
	return urlAttributePattern.ReplaceAllStringFunc(html, func(match string) string {
		m := urlAttributePattern.FindStringSubmatch(match)
		url := strings.Trim(strings.Trim(m[3], `"'`), " \t\r\n")
		if safeURLPattern.MatchString(url) {
			return match
		}
		return m[1] + m[2] + `"#"`
	})
}
