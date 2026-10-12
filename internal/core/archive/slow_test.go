package archive

import (
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/slowinput"
)

// tests/slow-inputs.test.mjs 的归档那一半：500 KB 的恶意正文必须在常数时间内渲染完，而且渲染出来的
// 东西里没有能跑的标签。
//
// 上游给的是 200 ms；带 -race 的构建为同样的工作付几倍的钱，所以预算在那里放宽，实测值两边都写进
// 日志（阶段 4 定下的做法）。

// slowInputMax is the budget for one shape in a normal build, and markdownInputMax is upstream's
// second number (MAX_MARKDOWN_MS): a Markdown shape may spend the parser's whole budget before the
// fallback shows it as text, so its own limit has to be above that.
const (
	slowInputMax     = 200 * time.Millisecond
	markdownInputMax = 2000 * time.Millisecond
)

// budget widens the limit for a race-instrumented build.
func budget(limit time.Duration) time.Duration {
	if raceEnabled {
		return limit * 10
	}
	return limit
}

func timed(t *testing.T, label string, run func()) time.Duration {
	t.Helper()
	start := time.Now()
	run()
	elapsed := time.Since(start)
	t.Logf("%s: %s", label, elapsed.Round(time.Millisecond))
	return elapsed
}

// 归档页的三条路（Markdown、HTML 的 frame、作者与 SEO 的文本处理）都必须在常数时间内走完。
func TestTheArchiveRendersSlowInputsInLinearTime(t *testing.T) {
	newsletter := model.Newsletter{Slug: "field-notes", Name: "Field notes", FromName: "Jane Doe", ArchiveEnabled: true}

	// 预热：第一次调用会付一次解析器的初始化。
	_ = RenderIssueBody(campaignWith(model.FormatMarkdown, slowinput.TagSoup(9)[:20_000]), newsletter)

	for label, input := range slowinput.Shapes() {
		body := RenderIssueBody(campaignWith(model.FormatHTML, input), newsletter)
		if body.Kind != KindFrame {
			t.Errorf("%s: an HTML issue should be framed", label)
		}
		elapsed := timed(t, label+"/frame", func() {
			_ = RenderIssueBody(campaignWith(model.FormatHTML, input), newsletter)
		})
		if elapsed > budget(slowInputMax) {
			t.Errorf("%s: framing took %s, want under %s", label, elapsed, budget(slowInputMax))
		}
	}

	for label, markdown := range slowinput.MarkdownShapes() {
		var rendered Body
		elapsed := timed(t, label+"/article", func() {
			rendered = RenderIssueBody(campaignWith(model.FormatMarkdown, markdown), newsletter)
		})
		if elapsed > budget(markdownInputMax) {
			t.Errorf("%s: rendering took %s, want under %s", label, elapsed, budget(markdownInputMax))
		}
		if rendered.Kind != KindArticle {
			t.Errorf("%s: a Markdown issue should be an article", label)
			continue
		}
		assertNoRunnableMarkup(t, rendered.HTML, label)
	}
}

// SEO 也只是字符串处理，同样不该被 500 KB 拖住（它给每个 issue 的标题与描述做绝对化与转义）。
func TestTheSEOHandlesSlowInputs(t *testing.T) {
	newsletter := model.Newsletter{Slug: "field-notes", Name: "Field notes", ArchiveEnabled: true}
	for label, input := range slowinput.MarkdownShapes() {
		campaign := campaignWith(model.FormatMarkdown, input)
		campaign.PreviewText = input[:1000]
		elapsed := timed(t, label+"/seo", func() {
			_ = IssueSEO(newsletter, campaign, "https://mail.example.com", "news.example.com=field-notes")
		})
		if elapsed > budget(slowInputMax) {
			t.Errorf("%s: seo took %s, want under %s", label, elapsed, budget(slowInputMax))
		}
	}
}

// 深到清洗器处理不了的嵌套也开不出一个脚本：真正要保住的是「没有任何标记活下来」。
func TestDeepNestingCannotOpenAScriptOnThePage(t *testing.T) {
	newsletter := model.Newsletter{Slug: "field-notes", ArchiveEnabled: true}
	for _, depth := range []int{1, 7, 8, 9, 50} {
		rendered := RenderIssueBody(campaignWith(model.FormatMarkdown, slowinput.Nested(depth)), newsletter)
		assertNoRunnableMarkup(t, rendered.HTML, "nested")
		if strings.Contains(rendered.HTML, "alert") {
			t.Errorf("depth %d: the script's text survived: %q", depth, rendered.HTML)
		}
	}
}

func campaignWith(format model.Format, body string) model.Campaign {
	return model.Campaign{
		ID: "64f1a2b3c4d5e6f7a8b9c0d1", Subject: "Slow", Format: format, Body: body,
		Status: model.CampaignSent, ArchiveListed: true, ArchiveSlug: "slow",
	}
}

// assertNoRunnableMarkup checks the archive's own output: whatever survived must not be able to run.
func assertNoRunnableMarkup(t *testing.T, html, label string) {
	t.Helper()
	lowered := strings.ToLower(html)
	for _, tag := range slowinput.UnsafeTags {
		if strings.Contains(lowered, tag) {
			t.Errorf("%s: %s survived", label, tag)
		}
	}
	if strings.Contains(html, "<!--") {
		t.Errorf("%s: a comment survived", label)
	}
}
