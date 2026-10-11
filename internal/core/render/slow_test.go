package render

import (
	"strings"
	"testing"
	"time"
)

// The htmlToText half of tests/slow-inputs.test.mjs. It lives here rather than in the content
// package because plain-text rendering is this package's job; the cleaning half is in
// internal/core/content/slow_test.go.
//
// The spec is that the conversion stays linear in the input size, and that is what this asserts: a
// four-times-larger input must not take more than eight times as long. A linear pass is ~4x, a
// quadratic one ~16x, so 8x separates them with room for a noisy machine.
//
// It deliberately does not assert a wall-clock budget. The earlier version did — 200 ms, scaled to
// 2 s under -race — and it flaked on CI: `unclosed <style>` took 2.123 s on a shared runner and
// passed on a re-run of the same commit. The conversion is genuinely linear (measured ratios 3.9 to
// 4.1 at 0.5 MB and 2 MB) but its constant is large: 500 KB of `<style>` takes ~37 ms here and ~890
// ms under -race. Comparing sizes instead of seconds measures the property the test is named after
// and does not care how fast the machine is.
const (
	smallInputSize = 125_000
	largeInputSize = 4 * smallInputSize // upstream's 500 KB spec size: 4 x 125 KB
	// ratioLimit sits between linear (4x) and quadratic (16x).
	ratioLimit = 8.0
	// tooQuickToJudge is when the smaller input is too fast for a ratio to mean anything.
	tooQuickToJudge = 200 * time.Microsecond
)

func repetition(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit))
}

// 恶意形状在 4 倍输入下不能超过 8 倍耗时：线性 ≈4×，平方 ≈16×。
func TestHTMLToTextIsLinearOnSlowInputs(t *testing.T) {
	for label, shape := range slowInputShapes() {
		small := shape(smallInputSize)
		large := shape(largeInputSize)
		_ = HTMLToText(small) // warm the renderer: its one-off costs do not depend on the size

		smallTime := timed(t, label+" small", func() { _ = HTMLToText(small) })
		largeTime := timed(t, label+" large", func() { _ = HTMLToText(large) })
		if smallTime < tooQuickToJudge {
			continue // nothing to measure against
		}
		if ratio := float64(largeTime) / float64(smallTime); ratio > ratioLimit {
			t.Errorf("%s: %d bytes took %.1fx the time of %d bytes, want under %.0fx",
				label, largeInputSize, ratio, smallInputSize, ratioLimit)
		}
	}
}

// slowInputShapes builds the adversarial inputs upstream uses, at whatever size is asked for. Each
// one is a piece of malformed markup repeated until it fills the input.
func slowInputShapes() map[string]func(size int) string {
	repeat := func(unit string) func(int) string {
		return func(size int) string { return repetition(unit, size) }
	}
	return map[string]func(int) string{
		"nested <scr<script>ipt>": func(size int) string {
			depth := size / 8
			return strings.Repeat("<scr", depth) + "<script>alert(1)</script>" + strings.Repeat("ipt>", depth)
		},
		"unclosed <script ":         repeat("<script "),
		"unclosed <style>":          repeat("<style>"),
		"unclosed comments":         repeat("<!-- >"),
		"unclosed <link ":           repeat("<link "),
		"unclosed <a ":              repeat("<a "),
		"a run of <":                repeat("<"),
		"unclosed <div ":            repeat("<div "),
		"unclosed email-only parts": repeat("<a data-email-only>"),
	}
}

func timed(t *testing.T, label string, run func()) time.Duration {
	t.Helper()
	start := time.Now()
	run()
	elapsed := time.Since(start)
	t.Logf("%s: %s", label, elapsed.Round(time.Microsecond))
	return elapsed
}

// htmlToText 是转换器，不是清洗器：upstream 也只断言它的耗时。它的输入是已经清洗过的邮件 HTML，
// 输出是 text/plain MIME 部分，一段字面文本 "<script " 在那里什么也跑不起来；真正挡住输入的是
// content 包的清洗器（见 content/slow_test.go）。
