package archive

import (
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/slowinput"
)

// tests/slow-inputs.test.mjs 的归档那一半：500 KB 的恶意正文必须在线性时间内渲染完，而且渲染出来的
// 东西里没有能跑的标签。
//
// 规格是「与输入大小成正比」，所以量的也是这个：四倍的输入不能超过八倍的时间。线性的约 4×，平方的
// 约 16×，8× 把两者分开，且不关心机器快不快、跑不跑 -race（阶段 4 在 htmlToText 上定下的做法——
// 绝对墙钟预算在共享 runner 上会偶发失败）。
//
// 解析器的预算是 MarkdownToHTML 自己的事，由 content 包直接量；这里量的是整条归档渲染路径与输入的
// 比例关系。
const (
	smallInputSize = slowinput.Small
	largeInputSize = slowinput.Spec
	// ratioLimit sits between linear (4x) and quadratic (16x).
	ratioLimit = 8.0
	// tooQuickToJudge is when the smaller input is too fast for a ratio to mean anything.
	tooQuickToJudge = 200 * time.Microsecond
)

func timed(t *testing.T, label string, run func()) time.Duration {
	t.Helper()
	start := time.Now()
	run()
	elapsed := time.Since(start)
	t.Logf("%s: %s", label, elapsed.Round(time.Millisecond))
	return elapsed
}

// 恶意 HTML：frame 一路（渲染成邮件、保住设计、放进 iframe）在四倍输入下不能超过八倍耗时。
func TestFramingIsLinearOnSlowInputs(t *testing.T) {
	newsletter := model.Newsletter{Slug: "field-notes", Name: "Field notes", FromName: "Jane Doe", ArchiveEnabled: true}
	for label, shape := range slowinput.Shapes() {
		small := campaignWith(model.FormatHTML, shape(smallInputSize))
		large := campaignWith(model.FormatHTML, shape(largeInputSize))
		_ = RenderIssueBody(small, newsletter) // warm the renderers

		smallTime := timed(t, label+" small", func() { _ = RenderIssueBody(small, newsletter) })
		largeTime := timed(t, label+" large", func() { _ = RenderIssueBody(large, newsletter) })
		if smallTime < tooQuickToJudge {
			continue
		}
		if ratio := float64(largeTime) / float64(smallTime); ratio > ratioLimit {
			t.Errorf("%s: %d bytes took %.1fx the time of %d bytes, want under %.0fx",
				label, largeInputSize, ratio, smallInputSize, ratioLimit)
		}
	}
}

// 恶意 Markdown：article 一路同样线性，输出里没有能跑的标签，而且渲染器自己的预算被守住了。
func TestRenderingMarkdownIsLinearOnSlowInputs(t *testing.T) {
	newsletter := model.Newsletter{Slug: "field-notes", Name: "Field notes", ArchiveEnabled: true}
	for label, shape := range slowinput.MarkdownShapes() {
		small := campaignWith(model.FormatMarkdown, shape(smallInputSize))
		large := campaignWith(model.FormatMarkdown, shape(largeInputSize))
		_ = RenderIssueBody(small, newsletter)

		var rendered Body
		smallTime := timed(t, label+" small", func() { _ = RenderIssueBody(small, newsletter) })
		largeTime := timed(t, label+" large", func() { rendered = RenderIssueBody(large, newsletter) })
		if smallTime >= tooQuickToJudge {
			if ratio := float64(largeTime) / float64(smallTime); ratio > ratioLimit {
				t.Errorf("%s: %d bytes took %.1fx the time of %d bytes, want under %.0fx",
					label, largeInputSize, ratio, smallInputSize, ratioLimit)
			}
		}
		// 这里不断言解析器的预算：那是 MarkdownToHTML 自己的事，在 content 包里直接量（那条测的是
		// 「有没有在预算内退回纯文本」）。归档这一步在预算之后还有清洗与渲染，而清洗 500 KB 的
		// 转义文本本身就要时间——把它算进预算，量到的就不是预算了。
		if rendered.Kind != KindArticle {
			t.Errorf("%s: a Markdown issue should be an article", label)
			continue
		}
		assertNoRunnableMarkup(t, rendered.HTML, label)
	}
}

// SEO 也只是字符串处理，同样不该被大输入拖住。
func TestSEOIsLinearOnSlowInputs(t *testing.T) {
	newsletter := model.Newsletter{Slug: "field-notes", Name: "Field notes", ArchiveEnabled: true}
	for label, shape := range slowinput.MarkdownShapes() {
		small := campaignWith(model.FormatMarkdown, shape(smallInputSize))
		large := campaignWith(model.FormatMarkdown, shape(largeInputSize))
		small.PreviewText, large.PreviewText = small.Body[:1000], large.Body[:1000]

		smallTime := timed(t, label+" small", func() {
			_ = IssueSEO(newsletter, small, "https://mail.example.com", "news.example.com=field-notes")
		})
		largeTime := timed(t, label+" large", func() {
			_ = IssueSEO(newsletter, large, "https://mail.example.com", "news.example.com=field-notes")
		})
		if smallTime < tooQuickToJudge {
			continue
		}
		if ratio := float64(largeTime) / float64(smallTime); ratio > ratioLimit {
			t.Errorf("%s: %d bytes took %.1fx the time of %d bytes, want under %.0fx",
				label, largeInputSize, ratio, smallInputSize, ratioLimit)
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
