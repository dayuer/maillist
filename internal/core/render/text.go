package render

import (
	"regexp"
	"strings"

	"mailcheap/internal/core/content"
)

var (
	hiddenElement    = content.ElementMatcher{Start: regexp.MustCompile(`(?i)<(style|script|head)`), End: regexp.MustCompile(`(?i)</(style|script|head)>`)}
	preheaderElement = content.ElementMatcher{Start: regexp.MustCompile(`(?i)<(div)\b[^>]*>`), End: regexp.MustCompile(`(?i)</(div)>`), Accepts: hasPreheaderAttr}
	linkElement      = content.ElementMatcher{Start: regexp.MustCompile(`(?i)<(a)\s[^>]*>`), End: regexp.MustCompile(`(?i)</(a)>`), Accepts: hasHref, Replace: linkText}
	emailOnlyElement = content.ElementMatcher{Start: regexp.MustCompile(`(?i)<([a-z][a-z0-9]*)\b[^>]*>`), End: regexp.MustCompile(`(?i)</([a-z][a-z0-9]*)>`), Accepts: hasEmailOnlyAttr}
	anyTagPattern    = regexp.MustCompile(`<[^>]+>`)
	breakTagPattern  = regexp.MustCompile(`(?i)<br\s*/?>`)
	blockEndPattern  = regexp.MustCompile(`(?i)</(p|div|h[1-6]|li|tr|table|blockquote)>`)
	listItemPattern  = regexp.MustCompile(`(?i)<li[^>]*>`)
	preheaderAttr    = regexp.MustCompile(`(?i)\bdata-preheader\b`)
	emailOnlyAttr    = regexp.MustCompile(`(?i)\sdata-email-only\b`)
	hrefAttrPattern  = regexp.MustCompile(`(?i)^<a\s[^>]*href="([^"]+)"`)
	imageTagPattern  = regexp.MustCompile(`(?i)<img\b`)
	blankRunsPattern = regexp.MustCompile(`\n{3,}`)
)

func hasPreheaderAttr(tag string) bool { return preheaderAttr.MatchString(tag) }
func hasEmailOnlyAttr(tag string) bool { return emailOnlyAttr.MatchString(tag) }
func hasHref(tag string) bool          { return hrefAttrPattern.MatchString(tag) }

// linkText labels a link with its address: "Example (https://example.com)", or the address alone
// when the text already is the address.
func linkText(tag, label string) string {
	href := hrefAttrPattern.FindStringSubmatch(tag)[1]
	text := strings.TrimSpace(stripTags(label))
	if text == "" || text == href {
		return href
	}
	return text + " (" + href + ")"
}

func stripTags(html string) string {
	return content.BeforeLastTagEnd(html, func(part string) string {
		return anyTagPattern.ReplaceAllLiteralString(part, "")
	})
}

// HTMLToText is upstream htmlToText: the plain-text alternative of a rendered email.
func HTMLToText(html string) string {
	visible := content.BeforeLastTagEnd(
		content.ReplaceElements(html, hiddenElement),
		func(part string) string {
			return content.ReplaceElements(content.ReplaceElements(part, preheaderElement), linkElement)
		},
	)
	blocks := breakTagPattern.ReplaceAllLiteralString(visible, "\n")
	blocks = blockEndPattern.ReplaceAllLiteralString(blocks, "\n\n")
	text := stripTags(content.BeforeLastTagEnd(blocks, func(part string) string {
		return listItemPattern.ReplaceAllLiteralString(part, "• ")
	}))
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", `"`)
	text = strings.ReplaceAll(text, "&#39;", "'")
	// Starting only at the first space of a run, so a long run not ending a line is read once:
	// the same as trimming trailing spaces and tabs from each line.
	text = trimTrailingSpacesPerLine(text)
	text = blankRunsPattern.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func trimTrailingSpacesPerLine(text string) string {
	if !strings.Contains(text, "\n") {
		return strings.TrimRight(text, " \t")
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// HasContent is upstream hasContent: the editor saves "<p></p>" for an empty document, so content
// means words or a picture.
func HasContent(body string) bool {
	return imageTagPattern.MatchString(body) || HTMLToText(body) != ""
}
