package content

import (
	"strings"
	"testing"
	"time"
)

// calloutMarkdown is the fixture of tests/callouts.test.mjs.
const calloutMarkdown = "Intro\n\n:::box\n**Project update**\n\n- Shipped the editor\n:::\n\n:::box\nSponsor\n:::\n\nOutro"

// Upstream (tests/callouts.test.mjs, "Markdown boxes become archive boxes") renders through
// renderIssueBody, which is cleanRichHtml(markdownToHtml(...)). Phase 1 stops at the Markdown
// output: the callout div is already in its final form, so the same bytes must appear here.
func TestMarkdownBoxesBecomeArchiveBoxes(t *testing.T) {
	html := MarkdownToHTML(calloutMarkdown, false)
	want := "<div data-callout=\"\"><p><strong>Project update</strong></p>\n" +
		"<ul>\n<li>Shipped the editor</li>\n</ul>\n</div>"
	if !strings.Contains(html, want) {
		t.Errorf("archive box %q missing from:\n%s", want, html)
	}
	if !strings.Contains(html, `<div data-callout=""><p>Sponsor</p>`) {
		t.Errorf("second box missing from:\n%s", html)
	}
	if !strings.Contains(html, "<p>Outro</p>") {
		t.Errorf("text after the last box missing from:\n%s", html)
	}
	if strings.Contains(html, ":::") {
		t.Errorf("box markers leaked into the output:\n%s", html)
	}
}

// tests/callouts.test.mjs "Markdown boxes are gray inline-styled boxes in emails" goes through
// renderEmail, which Task 1.9 adds; but the email box tag itself already lives here.
func TestMarkdownBoxesAreInlineStyledInEmails(t *testing.T) {
	html := MarkdownToHTML(calloutMarkdown, true)
	if got := strings.Count(html, "border-radius:12px;background:#f4f4f5;"); got != 2 {
		t.Errorf("inline-styled boxes = %d, want 2:\n%s", got, html)
	}
	if strings.Contains(html, ":::") {
		t.Errorf("box markers leaked into the output:\n%s", html)
	}
	if !strings.Contains(html, "Shipped the editor") {
		t.Errorf("box content lost:\n%s", html)
	}
}

func TestInlineCalloutStylesSwapsCalloutDivs(t *testing.T) {
	got := InlineCalloutStyles(`<div data-callout="" class="x"><p>a</p></div>` + `<div class="keep">b</div>`)
	want := EmailBoxTag + `<p>a</p></div>` + `<div class="keep">b</div>`
	if got != want {
		t.Fatalf("InlineCalloutStyles = %q, want %q", got, want)
	}
}

// Upstream caps unclosed boxes with MAX_UNCLOSED to dodge an O(n²) rescan in marked. goldmark
// parses blocks in one pass, so the counter isn't ported; this is the test that replaces it.
func TestUnclosedCalloutsRenderFast(t *testing.T) {
	src := strings.Repeat(":::box\n", 10_000) + "text\n"
	start := time.Now()
	html := MarkdownToHTML(src, false)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("10k unclosed boxes took %s", elapsed)
	}
	if strings.Contains(html, "data-callout") {
		t.Error("an unclosed box must stay a plain paragraph")
	}
	if !strings.Contains(html, ":::box") {
		t.Error("an unclosed box keeps its marker as literal text, as upstream does")
	}
}
