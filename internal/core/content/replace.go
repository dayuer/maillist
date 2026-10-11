package content

import (
	"regexp"
	"strings"
)

// BeforeLastTagEnd applies transform to the part of html up to and including the last ">", leaving
// the rest untouched. A pattern whose matches end with ">" can only match before the last one, so
// searching past it would read to the end of the body and fail — quadratic time over a run of
// unclosed tags.
func BeforeLastTagEnd(html string, transform func(string) string) string {
	end := strings.LastIndex(html, ">") + 1
	return transform(html[:end]) + html[end:]
}

// ElementMatcher is one "take the element and its content" rule.
type ElementMatcher struct {
	// Start matches an opening tag; group 1 is the tag name.
	Start *regexp.Regexp
	// End matches a closing tag; group 1 is the tag name.
	End *regexp.Regexp
	// Accepts can turn a start tag down without skipping what it covers. It sees the whole match.
	Accepts func(tag string) bool
	// Replace builds the replacement from the start tag and the element's content. The default
	// drops both.
	Replace func(tag, content string) string
}

// ReplaceElements replaces each element matched by m with m.Replace output: a start tag found by
// Start, and the nearest following end tag of the same name. End tags are looked up rather than
// searched for, so a start tag with none after it costs nothing. A Start that reads up to ">" must
// run within BeforeLastTagEnd.
func ReplaceElements(html string, m ElementMatcher) string {
	accepts := m.Accepts
	if accepts == nil {
		accepts = func(string) bool { return true }
	}
	replace := m.Replace
	if replace == nil {
		replace = func(string, string) string { return "" }
	}

	type endTag struct {
		index  int
		length int
	}
	endTags := map[string][]endTag{}
	for _, ix := range m.End.FindAllStringSubmatchIndex(html, -1) {
		name := strings.ToLower(html[ix[2]:ix[3]])
		endTags[name] = append(endTags[name], endTag{index: ix[0], length: ix[1] - ix[0]})
	}

	var b strings.Builder
	cursor := 0
	next := map[string]int{}
	for _, ix := range m.Start.FindAllStringSubmatchIndex(html, -1) {
		if ix[0] < cursor {
			continue
		}
		tag := html[ix[0]:ix[1]]
		if !accepts(tag) {
			continue
		}
		name := strings.ToLower(html[ix[2]:ix[3]])
		ends := endTags[name]
		contentStart := ix[1]
		i := next[name]
		for i < len(ends) && ends[i].index < contentStart {
			i++
		}
		next[name] = i
		if len(ends) == 0 || i >= len(ends) {
			continue
		}
		end := ends[i]
		b.WriteString(html[cursor:ix[0]])
		b.WriteString(replace(tag, html[contentStart:end.index]))
		cursor = end.index + end.length
	}
	b.WriteString(html[cursor:])
	return b.String()
}
