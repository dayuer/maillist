package content

import (
	"strings"
	"testing"
)

// FI-B from the phase 1-3 audit: four places where this port and upstream differ, none of which was
// written down or pinned. Two are decided here (the third, /%75nsubscribe, is pinned in
// validate/url_test.go; the fourth, normalizeDate's narrower set of formats, in
// importer/csv_test.go). Upstream's tests do not reach any of them, which is why they went unnoticed.
//
// The cleaner is the one that is a deliberate difference rather than an open question:
// lib/clean-html.js drops a rejected element *and everything inside it* with a regular expression,
// while bluemonday parses, so the text of a dropped <script> survives as escaped plain text. Both
// leave nothing that can run; the visible text differs.
func TestADroppedElementKeepsItsTextAsEscapedText(t *testing.T) {
	cleaned := CleanContent("<p>before</p><script>alert(1)</script><p>after</p>")
	if strings.Contains(cleaned, "<script") {
		t.Fatalf("a script tag survived:\n%s", cleaned)
	}
	// bluemonday keeps the element's text, escaped; upstream's regex cleaner drops it with the tag.
	if !strings.Contains(cleaned, "before") || !strings.Contains(cleaned, "after") {
		t.Fatalf("the paragraphs around it were lost:\n%s", cleaned)
	}
}

// 空的盒子渲染成空盒子。
//
// Upstream has no test for ":::box" with nothing between the markers; this port renders an empty
// callout div, which is what a box with no content is. Pinned so a future change is a decision
// rather than a surprise.
func TestAnEmptyBoxRendersAsAnEmptyBox(t *testing.T) {
	for _, body := range []string{":::box\n:::", ":::box\n\n:::"} {
		html := MarkdownToHTML(body, false)
		if !strings.Contains(html, `<div data-callout=""></div>`) {
			t.Errorf("%q rendered as:\n%s", body, html)
		}
		if strings.Contains(html, ":::") {
			t.Errorf("%q leaked its markers:\n%s", body, html)
		}
	}
}

// 两个盒子紧挨着：第二个照常开。
func TestAdjacentBoxesBothRender(t *testing.T) {
	html := MarkdownToHTML(":::box\none\n:::\n:::box\ntwo\n:::", false)
	if got := strings.Count(html, `<div data-callout="">`); got != 2 {
		t.Fatalf("boxes = %d, want 2:\n%s", got, html)
	}
	if !strings.Contains(html, "one") || !strings.Contains(html, "two") {
		t.Fatalf("a box lost its content:\n%s", html)
	}
}
