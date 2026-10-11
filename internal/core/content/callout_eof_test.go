package content

import (
	"strings"
	"testing"
)

// 结尾的 ::: 没有换行时不能漏一个冒号出来。
//
// FI-A1 from the phase 1-3 audit, and a real defect: the closing marker was consumed with
// Advance(segment.Len()-1), leaving the newline for the block loop to take. A last line has no
// newline, so the final ":" stayed in the text and rendered as a paragraph of its own —
// ":::box\nb\n:::" produced "<p>b\n:</p>". A body that ends with a box is enough to trigger it.
//
// Upstream's tests never end a document that way, which is why this survived translation.
func TestAClosingMarkerAtTheEndOfTheDocumentLeavesNoColon(t *testing.T) {
	for _, body := range []string{
		":::box\nb\n:::",   // no trailing newline at all
		":::box\nb\n:::\n", // and the ordinary case, which always worked
	} {
		name := "without a trailing newline"
		if strings.HasSuffix(body, "\n") {
			name = "with a trailing newline"
		}
		t.Run(name, func(t *testing.T) {
			html := MarkdownToHTML(body, false)
			if strings.Contains(html, "::") || strings.Contains(html, ">:") {
				t.Errorf("a colon from the marker survived:\n%s", html)
			}
			if !strings.Contains(html, `<div data-callout=""><p>b</p>`) {
				t.Errorf("the box is not a box:\n%s", html)
			}
		})
	}
}
