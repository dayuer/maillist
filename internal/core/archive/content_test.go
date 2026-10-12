package archive

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/model"
)

// 翻译 tests/archive-content.test.mjs。上游有三种格式，本端口两种：`rich` 是编辑器的产物，随编辑器
// 一起删除（设计 §2），所以那份夹具里针对 rich 的用例在这里用的是 markdown——判据相同，输入不同。

func body(a model.Campaign, n model.Newsletter) Body { return RenderIssueBody(a, n) }

// tagFor is the <img> tag whose src ends in the given name: the archive renders pictures with the
// attributes this pass added, and the assertions are about those.
func tagFor(t *testing.T, html, name string) string {
	t.Helper()
	for _, tag := range regexp.MustCompile(`(?i)<img\b[^>]*>`).FindAllString(html, -1) {
		if strings.Contains(tag, name) {
			return tag
		}
	}
	t.Fatalf("no image for %q in %s", name, html)
	return ""
}

func markdownCampaign(body string) model.Campaign {
	c := model.NewCampaign("nl", "Issue", time.Now().UTC())
	c.Format = model.FormatMarkdown
	c.Body = body
	return c
}

// 首图先加载，其余的懒加载。
func TestTheHeroImageLoadsFirstAndTheRestLazily(t *testing.T) {
	c := markdownCampaign("![Hero](https://example.com/hero.jpg)\n\n![Game](https://example.com/game.jpg)")
	got := body(c, model.Newsletter{Name: "Notes"}).HTML

	// The serializer rewrites the tag (attribute order, no self-closing slash), so what is asserted is
	// which attributes each picture carries.
	hero := tagFor(t, got, "hero.jpg")
	for _, want := range []string{`fetchpriority="high"`, `decoding="async"`} {
		if !strings.Contains(hero, want) {
			t.Errorf("hero %s: %s", tagFor(t, got, "hero.jpg"), want)
		}
	}
	if strings.Contains(hero, `loading="lazy"`) {
		t.Errorf("the hero loads lazily: %s", hero)
	}
	second := tagFor(t, got, "game.jpg")
	for _, want := range []string{`loading="lazy"`, `decoding="async"`} {
		if !strings.Contains(second, want) {
			t.Errorf("second image %s: %s", second, want)
		}
	}
	if strings.Contains(second, `fetchpriority="high"`) {
		t.Errorf("two heroes: %s", second)
	}
}

// Markdown 是 article，HTML 是 frame。
func TestIssuesPickTheArticleLayoutUnlessTheirHTMLHasItsOwnDesign(t *testing.T) {
	markdown := body(markdownCampaign("## Hi\n\n![a](https://x.test/a.png)"), model.Newsletter{})
	if markdown.Kind != KindArticle {
		t.Fatalf("markdown kind = %q", markdown.Kind)
	}
	if !strings.Contains(markdown.HTML, "<h2>Hi</h2>") || !strings.Contains(markdown.HTML, `fetchpriority="high"`) {
		t.Errorf("markdown html = %s", markdown.HTML)
	}

	email := model.NewCampaign("nl", "Issue", time.Now().UTC())
	email.Format = model.FormatHTML
	email.Body = `<html><head></head><body><table><tr><td><a href="{{unsubscribe_url}}">Bye</a></td></tr></table></body></html>`
	framed := body(email, model.Newsletter{Name: "Notes"})
	if framed.Kind != KindFrame {
		t.Fatalf("html kind = %q", framed.Kind)
	}
	if !strings.Contains(framed.HTML, `<base target="_blank">`) {
		t.Errorf("no base target: %s", framed.HTML)
	}
	if !strings.Contains(framed.HTML, `href="#"`) {
		t.Errorf("the unsubscribe placeholder survived: %s", framed.HTML)
	}
	if regexp.MustCompile(`loading="lazy"`).MatchString(framed.HTML) {
		t.Errorf("the frame measures its height once pictures load: %s", framed.HTML)
	}
	if !strings.Contains(framed.HTML, "img{max-width:100%;height:auto}") {
		t.Errorf("no responsive images: %s", framed.HTML)
	}
}

// 通过 API 传来的 HTML 保持 frame，即使它读起来像一篇文章；Markdown 则一律走清洗。
func TestHTMLKeepsItsFrameAndMarkdownIsCleaned(t *testing.T) {
	c := model.NewCampaign("nl", "Issue", time.Now().UTC())
	c.Format = model.FormatHTML
	c.Body = `<h2 id="chapter-1">Chapter 1</h2><p onclick="alert(1)">Hi <x_ onpointerover="alert(2)">there</x_></p>`
	if got := body(c, model.Newsletter{}); got.Kind != KindFrame {
		t.Fatalf("kind = %q", got.Kind)
	}

	m := markdownCampaign(`<p onclick="alert(1)">Hi <x_ onpointerover="alert(2)">there</x_></p>`)
	cleaned := body(m, model.Newsletter{})
	if cleaned.Kind != KindArticle || cleaned.HTML != "<p>Hi there</p>" {
		t.Fatalf("cleaned = %+v", cleaned)
	}
}

// 内联的 issue 在页面渲染时过一遍清洗器：指向别处的链接需要 scheme。
func TestInlineIssuesGoThroughTheSanitizerWhenThePageRenders(t *testing.T) {
	m := markdownCampaign(`<p><a href="//evil.test/x">x</a> <a href="/p/next">next</a></p>`)
	if got := body(m, model.Newsletter{}).HTML; got != `<p><a>x</a> <a href="/p/next">next</a></p>` {
		t.Errorf("got %s", got)
	}
}

// Markdown 的表格、代码与删除线在归档页上保留原样。
func TestMarkdownTablesCodeAndStrikethroughKeepTheirMarkup(t *testing.T) {
	m := markdownCampaign("#### Four\n\n| a | b |\n| - | - |\n| 1 | 2 |\n\n```\ncode\n```\n\n~~old~~ `inline`")
	got := body(m, model.Newsletter{}).HTML
	for _, want := range []string{
		"<h4>Four</h4>", "<th>a</th>", "<td>1</td>", "<pre><code>code\n</code></pre>", "<p><del>old</del> <code>inline</code></p>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %s", want, got)
		}
	}
}

// 藏在清洗器后面的脚本与危险链接到不了页面上。
func TestScriptsAndUnsafeLinksNeverReachThePage(t *testing.T) {
	tricks := strings.Join([]string{
		"<scr<script></script>ipt>alert(1)</script>",
		"<script>alert(2)</script >",
		`<p>Hi</p><img src=x onerror="alert(3)"><a href=" javascript:alert(4)">x</a>`,
		"<p>Bye</p><script src=//evil.test/x.js",
	}, "")

	unsafe := regexp.MustCompile(`(?i)<script|onerror|javascript:|<[^>]*//evil`)
	for _, got := range []string{
		body(markdownCampaign(tricks), model.Newsletter{}).HTML,
	} {
		if unsafe.MatchString(got) {
			t.Errorf("unsafe markup survived: %s", got)
		}
		if !strings.Contains(got, "Hi") {
			t.Errorf("content was lost: %s", got)
		}
	}
}

// frame 只保留 web、mail 与图片链接。
func TestFramedIssuesKeepWebMailAndImageLinksOnly(t *testing.T) {
	c := model.NewCampaign("nl", "Issue", time.Now().UTC())
	c.Format = model.FormatHTML
	c.Body = `<html><head></head><body><a href="https://x.test/a">Web</a><a href="mailto:a@x.test">Mail</a>` + "\n" +
		`<a href="javascript:alert(1)">Bad</a><a title="t"href='&#106;avascript:alert(1)'>Encoded</a><a/href=vbscript:x>Slash</a>` + "\n" +
		`<img src="data:image/png;base64,AAA"></body></html>`
	got := body(c, model.Newsletter{}).HTML

	for _, want := range []string{`href="https://x.test/a"`, `href="mailto:a@x.test"`, `src="data:image/png;base64,AAA"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %s", want, got)
		}
	}
	if regexp.MustCompile(`(?i)javascript|&#106;|vbscript`).MatchString(got) {
		t.Errorf("an unsafe link survived: %s", got)
	}
}
