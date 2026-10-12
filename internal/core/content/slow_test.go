package content

import (
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/slowinput"
)

// Translated from tests/slow-inputs.test.mjs. That file sits under phase 3 in the plan as an HTTP
// concern ("oversized body, long headers"); it is in fact entirely about the cleaners, so it lives
// with them. The archive (renderIssueBody) and the Markdown thumbnail branch are phase 7, and the
// htmlToText half is in the render package's slow_test.go.
//
// Upstream works at 500 KB with a 200 ms budget. A race-instrumented build pays several times that
// for reasons the production binary would not, so the budget is scaled there while the measured
// number is logged either way.
const (
	slowInputSize = 500_000
	slowInputMax  = 200 * time.Millisecond
)

func repetition(unit string) string {
	return strings.Repeat(unit, slowInputSize/len(unit))
}

// nested is upstream's nested: a script tag cut open repeatedly.
func nested(depth int) string {
	return strings.Repeat("<scr", depth) + "<script>alert(1)</script>" + strings.Repeat("ipt>", depth)
}

// slowInputShapes are upstream's SHAPES.
func slowInputShapes() map[string]string {
	return map[string]string{
		"nested <scr<script>ipt>":   nested(slowInputSize / 8),
		"unclosed <script ":         repetition("<script "),
		"unclosed <style>":          repetition("<style>"),
		"unclosed comments":         repetition("<!-- >"),
		"unclosed <link ":           repetition("<link "),
		"unclosed <a ":              repetition("<a "),
		"a run of <":                repetition("<"),
		"unclosed <div ":            repetition("<div "),
		"unclosed <body ":           repetition("<body "),
		"unclosed <title>":          repetition("<title>"),
		"unclosed <head ":           repetition("<head "),
		"unclosed email-only parts": repetition("<a data-email-only>"),
		"tag soup":                  tagSoup(1),
		"tag soup with attributes":  tagSoup(2),
		"tag soup with box markers": tagSoup(3),
	}
}

// tagSoup builds a deterministic mix of the pieces upstream uses, so the shape is adversarial rather
// than uniform.
func tagSoup(seed int) string {
	pieces := []string{"<", ">", "</", "<!--", "-->", "<script", "</script>", "<scr", "ipt>",
		"<style>", "<svg", "<link ", "<title>", "<head ", "<body ", "<div ", "</div>", "<a ",
		`<a href="`, "</a>", "<img ", ` src="https://x.test/a.png"`, "<p>", "</p>",
		" data-email-only", " data-preheader", "=", `"`, "'", " ", "\n", "x", "*a", "_", "[", "](",
		"`", "> ", ":::box\n"}

	// A deterministic stand-in for upstream's seeded random: the point is a repeatable adversarial
	// shape, not unpredictability.
	state := seed
	next := func() int {
		state = (state*1103515245 + 12345) & 0x7fffffff
		return state
	}
	var b strings.Builder
	b.Grow(slowInputSize + 64)
	for b.Len() < slowInputSize {
		b.WriteString(pieces[next()%len(pieces)])
	}
	return b.String()
}

// budget is the per-shape limit for this build.
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

// 500 KB 的恶意形状必须在常数时间内清洗完，且输出里没有能跑的标签。
func TestCleaningIsLinearOnSlowInputs(t *testing.T) {
	// Warm the parser so the first shape does not pay for one-off initialisation.
	_ = CleanContent(tagSoup(9)[:20_000])

	for label, input := range slowInputShapes() {
		var cleaned string
		elapsed := timed(t, label, func() { cleaned = CleanContent(input) })
		if elapsed > budget(slowInputMax) {
			t.Errorf("%s: cleaning took %s, want under %s", label, elapsed, budget(slowInputMax))
		}
		assertNoUnsafeMarkup(t, cleaned, label)
	}
}

// 缩略图提取同样线性。
func TestThumbnailExtractionIsLinear(t *testing.T) {
	for label, input := range slowInputShapes() {
		elapsed := timed(t, label+"/firstImageURL", func() { _ = FirstImageURL(input) })
		if elapsed > budget(slowInputMax) {
			t.Errorf("%s/FirstImageURL took %s, want under %s", label, elapsed, budget(slowInputMax))
		}
	}
}

// 嵌套到清洗器处理不了的深度也不能开出脚本。
//
// Upstream asserts `cleanRichHtml(nested(depth))` matches neither /</ nor /alert/ — its regex
// cleaner drops the whole span from the first cut-open "<script" to the nearest "</script>", and for
// depth 8 that is the entire input. bluemonday parses instead: the malformed "<scr" runs are
// dropped as unknown tags, the script element's own text is dropped with it, and what is left is
// text — "alert(1)ipt&gt;ipt&gt;…" for depth 8. The assertions here are the properties that
// actually matter: no markup at all survives (nothing can open a tag), so nothing can run. The
// visible text differs from upstream's empty string.
func TestDeepNestingCannotOpenAScript(t *testing.T) {
	for _, depth := range []int{1, 7, 8, 9, 50} {
		cleaned := CleanContent(nested(depth))
		if strings.Contains(cleaned, "<") {
			t.Errorf("depth %d: markup survived cleaning: %q", depth, cleaned)
		}
		if strings.Contains(strings.ToLower(cleaned), "<script") ||
			strings.Contains(strings.ToLower(cleaned), "script") {
			t.Errorf("depth %d: a script tag survived: %q", depth, cleaned)
		}
	}
}

// assertNoUnsafeMarkup checks the cleaner's own output: whatever it kept must not be able to run.
func assertNoUnsafeMarkup(t *testing.T, html, label string) {
	t.Helper()
	lowered := strings.ToLower(html)
	for _, tag := range []string{"<script", "<style", "<svg", "<iframe", "<form", "<button",
		"<noscript", "<link", "<object", "<embed", "<template", "<textarea", "<title", "<xmp"} {
		if strings.Contains(lowered, tag) {
			t.Errorf("%s: %s survived cleaning", label, tag)
		}
	}
	if strings.Contains(html, "<!--") {
		t.Errorf("%s: a comment survived cleaning", label)
	}
}

// Markdown 的渲染有预算：过不去就退回纯文本（上游 marked 的一秒行为）。
//
// 这是阶段 7 才补上的一半。CommonMark 解析器在被切开的链接、嵌套引用这类形状上是平方的：实测
// 100 KB 的 "[a](" 要 3.06 秒，同样的字节数换成散文是 1 毫秒。这里量的是「等待」——解析器自己的
// 预算一到，调用方就拿到纯文本。
func TestMarkdownRenderingStaysWithinItsBudget(t *testing.T) {
	for label, markdown := range slowinput.MarkdownShapes() {
		elapsed := timed(t, label+"/markdown", func() { _ = MarkdownToHTML(markdown, true) })
		if elapsed > budget(MarkdownBudget+slowInputMax) {
			t.Errorf("%s: %s, want under %s", label, elapsed, budget(MarkdownBudget+slowInputMax))
		}
		// 这里不断言输出里没有危险标签：渲染器按上游语义放行原始 HTML（html.WithUnsafe），
		// 清洗是 CleanContent 的事，那一条由 TestCleaningIsLinearOnSlowInputs 负责。归档页
		// 两者都走，所以它的 slow 测试会同时断言时间与「没有能跑的标签」。
	}
}
