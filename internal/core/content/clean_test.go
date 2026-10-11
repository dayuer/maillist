package content

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// The payloads of tests/clean-html.test.mjs, verbatim.
var cleanPayloads = map[string]string{
	"audit":         `<x_ style="position:fixed;inset:0" onpointerover="alert(document.domain)"></x_>`,
	"underscore":    `<a_ href="https://x.test" onclick="alert(1)">a_</a_><img_ src=x onerror=alert(1)>`,
	"punctuation":   "<x-y onpointerover=alert(1)>dash</x-y> <x.y onpointerover=alert(1)>dot</x.y> <x:y onpointerover=alert(1)>colon</x:y> <x$ onpointerover=alert(1)>dollar</x$> <xé onpointerover=alert(1)>accent</xé>",
	"whitespace":    "<p onpointerover=alert(1)>nbsp</p><img\vsrc=x onerror=alert(1)>",
	"mixedCase":     `<X_ OnPointerOver="alert(1)">upper</X_><IMG SRC=x ONERROR=alert(1)><A HREF="JaVaScRiPt:alert(1)">link</A>`,
	"joined":        "<<x_>p onclick=alert(1)>joined<<x_>img src=x onerror=alert(1)>",
	"splitScript":   "<scr<script>ipt>alert(1)</script><scr<x_>ipt>alert(2)</script><script>alert(3)</script >",
	"svg":           `<svg><foreignObject><img src=x onerror=alert(1)></foreignObject></svg><svg><a href="javascript:alert(1)">s</a></svg>`,
	"math":          "<math><mtext><table><mglyph><style><img src=x onerror=alert(1)></style></mglyph></table></mtext></math>",
	"noscript":      `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></p></noscript>`,
	"template":      "<template><img src=x onerror=alert(1)></template>",
	"textarea":      `<textarea><p title="</textarea><img src=x onerror=alert(1)>"></p></textarea>`,
	"title":         `<title><p title="</title><img src=x onerror=alert(1)>"></p></title>`,
	"xmp":           `<xmp><p title="</xmp><img src=x onerror=alert(1)>"></p></xmp>`,
	"iframe":        `<iframe srcdoc="<script>alert(1)</script>"></iframe><iframe srcdoc="&lt;img src=x onerror=alert(1)&gt;">`,
	"quotedGt":      `<a title="x>" onclick="alert(1)" href="https://ok.test">q</a><img alt=">" src="https://ok.test/a.png" onerror="alert(1)">`,
	"encodedUrl":    `<a href="&#106;avascript:alert(1)">e1</a><a href="jav&#x09;ascript:alert(1)">e2</a><a href="javascript&colon;alert(1)">e3</a><a href="java` + "\n" + `script:alert(1)">e4</a><a href=" javascript:alert(1)">e5</a>`,
	"dataUrl":       `<a href="data:text/html,<script>alert(1)</script>">d1</a><img src="data:text/html;base64,PHNjcmlwdD4="><object data="javascript:alert(1)"></object><embed src="javascript:alert(1)">`,
	"valueBreakout": `<a href="/<img src=x//">x</a><p>" onmouseover="alert(1)" x="</p>`,
}

// Cleaned HTML is written in one shape: lowercase allowed tags with double-quoted attributes, and no
// "<" anywhere else. Text can still read like markup, so only tags are checked.
var (
	cleanTagPattern  = regexp.MustCompile(`<\/?([a-z][a-z0-9]*)((?:\s+[a-z-]+(?:="[^"]*")?)*)\s*\/?>`)
	cleanAttrPattern = regexp.MustCompile(`([a-z-]+)(?:="([^"]*)")?`)
	urlPatterns      = map[string]*regexp.Regexp{
		"href": regexp.MustCompile(`(?i)^(https?:|mailto:|#|/)`),
		"src":  regexp.MustCompile(`(?i)^https?://`),
	}
)

func assertInert(t *testing.T, html, label string) {
	t.Helper()
	text := cleanTagPattern.ReplaceAllStringFunc(html, func(tag string) string {
		m := cleanTagPattern.FindStringSubmatch(tag)
		if !contains(contentTags, m[1]) {
			t.Errorf("%s: <%s> in %s", label, m[1], html)
		}
		for _, a := range cleanAttrPattern.FindAllStringSubmatch(m[2], -1) {
			name, value := a[1], a[2]
			if strings.HasPrefix(name, "on") {
				t.Errorf("%s: %s in %s", label, name, html)
			}
			if regexp.MustCompile(`(?i)javascript:|data:`).MatchString(value) {
				t.Errorf("%s: %s=%q in %s", label, name, value, html)
			}
			if re, ok := urlPatterns[name]; ok && !re.MatchString(value) {
				t.Errorf("%s: %s=%q in %s", label, name, value, html)
			}
		}
		return ""
	})
	if strings.Contains(text, "<") {
		t.Errorf("%s: markup outside the allowed tags in %s", label, html)
	}
	if regexp.MustCompile(`(?i)</?(script|style|svg|math|iframe|template|textarea|title|xmp|noscript|object|embed)\b`).MatchString(html) {
		t.Errorf("%s: dropped element survived in %s", label, html)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestTagsOutsideAllowlistGoWithTheirAttributes(t *testing.T) {
	for in, want := range map[string]string{
		cleanPayloads["audit"]:       "",
		cleanPayloads["underscore"]:  "a_",
		cleanPayloads["punctuation"]: "dash dot colon dollar accent",
		cleanPayloads["mixedCase"]:   "upper<a>link</a>",
		// Upstream's keepContentAttributes('<P CLASS="x">Hi</P ><br/>') is "<p>Hi</p><br />";
		// bluemonday writes the void tag as "<br/>", which CleanContent normalizes back to "<br />".
		`<P CLASS="x">Hi</P ><br/>`: "<p>Hi</p><br />",
	} {
		if got := CleanContent(in); got != want {
			t.Errorf("CleanContent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDroppingATagCantJoinAStrayLtToTheTextAfterIt(t *testing.T) {
	// Upstream cleanRichHtml("<<x_>p onclick=alert(1)>joined") is "&lt;p onclick=alert(1)>joined".
	// bluemonday escapes the ">" in text as "&gt;" (it renders the same); everything else matches.
	got := CleanContent("<<x_>p onclick=alert(1)>joined")
	want := "&lt;p onclick=alert(1)&gt;joined"
	if got != want {
		t.Errorf("CleanContent = %q, want %q", got, want)
	}
	assertInert(t, got, "joined")
}

func TestABracketKeptInALinkCantBeCutOpenByLaterPasses(t *testing.T) {
	// Upstream keeps the anchor and its relative href: `<a href="/&lt;img src=x//">x</a>`.
	// bluemonday drops that href because it contains a space, which is stricter hardening, and
	// then (with AllowNoAttrs) keeps the empty anchor. Both are inert.
	got := CleanContent(cleanPayloads["valueBreakout"])
	if got != `<a>x</a><p>&#34; onmouseover=&#34;alert(1)&#34; x=&#34;</p>` {
		t.Errorf("CleanContent = %q", got)
	}
	assertInert(t, got, "valueBreakout")
}

func TestMarkupTheCleanerDoesNotKnowCannotRun(t *testing.T) {
	for label, payload := range cleanPayloads {
		// Upstream also renders each payload through renderIssueBody (archive, phase 7) and
		// renderEmail (Task 1.9); phase 1 covers the editor-HTML path.
		assertInert(t, CleanContent(payload), label)
	}
}

func TestALongRunOfLtTakesLinearTime(t *testing.T) {
	// Upstream uses 500_000 "<" and a 2000 ms budget inside renderIssueBody; the plan asks for
	// 100_000 and 1 s, which the cleaner alone must meet.
	body := "<p>Hi</p>" + strings.Repeat("<", 100_000) + ">"
	start := time.Now()
	html := CleanContent(body)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cleaning took %s", elapsed)
	}
	if !strings.HasPrefix(html, "<p>Hi</p>&lt;&lt;") {
		t.Errorf("got %q", html[:min(40, len(html))])
	}
}

// tests/callouts.test.mjs "editor boxes keep only their marker, other divs become plain layout".
func TestEditorBoxesKeepOnlyTheirMarkerAndOtherDivsBecomePlainLayout(t *testing.T) {
	html := CleanContent(`<div data-callout="" style="background:#fff8e6"><p>Update</p></div>
<div style="background-color:#fcf5ff;padding:0"><p>Tint</p></div>`)
	if !strings.Contains(html, `<div data-callout=""><p>Update</p></div>`) {
		t.Errorf("box marker lost: %s", html)
	}
	if !strings.Contains(html, "<div><p>Tint</p></div>") {
		t.Errorf("plain div not reduced to layout: %s", html)
	}
	if strings.Contains(html, "#fff8e6") {
		t.Errorf("stored style survived: %s", html)
	}
}

// tests/callouts.test.mjs "highlights lose their inline colors".
func TestHighlightsLoseTheirInlineColors(t *testing.T) {
	html := CleanContent(`<p><mark style="background-color:#fff4d1">Margin.</mark></p>`)
	if !strings.Contains(html, "<mark>Margin.</mark>") {
		t.Errorf("got %s", html)
	}
}

// tests/rich-content.test.mjs "editor HTML keeps its content and drops scripts, handlers and
// unfinished uploads".
func TestEditorHTMLKeepsItsContentAndDropsScriptsHandlersAndUploads(t *testing.T) {
	html := CleanContent(richEditorHTML())
	if !strings.HasPrefix(html, "<h1>Big news</h1>") {
		t.Errorf("got %s", html)
	}
	if !strings.Contains(html, `<a target="_blank" rel="noopener noreferrer nofollow" href="https://example.org">Example</a>`) {
		t.Errorf("safe link changed: %s", html)
	}
	if !strings.Contains(html, "<a>bad</a>") {
		t.Errorf("javascript: link must lose its href: %s", html)
	}
	if !strings.Contains(html, "<p><br></p>") {
		t.Errorf("blank lines must stay as spacing: %s", html)
	}
	if strings.HasSuffix(html, "<p><br></p>") {
		t.Errorf("the editor's trailing empty paragraph must go: %s", html)
	}
	if strings.Contains(html, "blob:") {
		t.Errorf("images still uploading must be dropped: %s", html)
	}
	if !strings.Contains(html, `<img src="https://cdn.example.com/chart.png" alt="Chart" width="320" height="160">`) {
		t.Errorf("content image changed: %s", html)
	}
	if regexp.MustCompile(`onerror|<script|evil`).MatchString(html) {
		t.Errorf("handlers, scripts or stored styles survived: %s", html)
	}
	if !strings.Contains(html, richBox) {
		t.Errorf("box lost: %s", html)
	}
	if !strings.Contains(html, `<div data-callout=""><p>Shipped</p></div>`) {
		t.Errorf("boxes must keep no stored styles: %s", html)
	}
}

// tests/rich-content.test.mjs "everything the editor writes keeps its markup on the archive".
// Upstream renders through renderIssueBody, which adds the archive's own image attributes
// (fetchpriority/decoding) and the article wrapper; those belong to phase 7. Phase 1 asserts the
// cleaner keeps the markup unchanged.
func TestEverythingTheEditorWritesKeepsItsMarkup(t *testing.T) {
	in := strings.Join([]string{
		"<h1>One</h1><h2>Two</h2><h3>Three</h3>",
		"<p><strong>b</strong> <em>i</em> <s>s</s> <u>u</u> <code>c</code><br>next</p>",
		"<blockquote><p>Quote</p></blockquote><ol><li><p>1</p></li></ol><ul><li><p>a</p></li></ul><hr>",
		`<p><a target="_blank" rel="noopener noreferrer nofollow" href="https://example.org">Example</a></p>`,
		`<img src="https://cdn.example.com/chart.png" alt="Chart" width="320" height="160">`,
		richBox,
	}, "")
	want := strings.NewReplacer("<br>", "<br />", "<hr>", "<hr />").Replace(in)
	if got := CleanContent(in); got != want {
		t.Errorf("CleanContent =\n%s\nwant\n%s", got, want)
	}
}

// tests/rich-content.test.mjs "editor issues take their thumbnail from the first image".
// Upstream's firstImageUrl also takes the Markdown branch (format "markdown"); phase 1's signature
// is the HTML branch, and the Markdown branch belongs with the archive/import path (phase 7).
func TestEditorIssuesTakeTheirThumbnailFromTheFirstImage(t *testing.T) {
	if got := FirstImageURL(richEditorHTML()); got != "https://cdn.example.com/chart.png" {
		t.Errorf("FirstImageURL = %q", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const richBox = `<div data-callout=""><p>Startups I built</p></div>`

// The editorHtml fixture of tests/rich-content.test.mjs.
func richEditorHTML() string {
	return strings.Join([]string{
		"<h1>Big news</h1>",
		`<p>Hi <strong>you</strong>, <a target="_blank" rel="noopener noreferrer nofollow" href="https://example.org">Example</a> <a href="javascript:alert(1)">bad</a></p>`,
		"<p></p>",
		"<ul><li><p>One</p></li><li><p>Two</p></li></ul>",
		`<img src="blob:http://localhost:3100/5f1c" alt="" width="600" height="300">`,
		`<img src="https://cdn.example.com/chart.png" alt="Chart" width="320" height="160" onerror="alert(1)">`,
		richBox,
		`<div data-callout="" style="background:url(https://evil.example/x)"><p>Shipped</p></div>`,
		"<script>alert(1)</script>",
		"<p></p>",
	}, "")
}
