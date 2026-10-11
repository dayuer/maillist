package render

import (
	"strings"
	"testing"
	"time"
)

// The htmlToText half of tests/slow-inputs.test.mjs. It lives here rather than in the content
// package because plain-text rendering is this package's job; the cleaning half is in
// internal/core/content/slow_test.go.
const (
	slowInputSize = 500_000
	slowInputMax  = 200 * time.Millisecond
)

func repetition(unit string) string {
	return strings.Repeat(unit, slowInputSize/len(unit))
}

// 500 KB 的恶意形状转纯文本要线性。
func TestHTMLToTextIsLinearOnSlowInputs(t *testing.T) {
	shapes := map[string]string{
		"nested <scr<script>ipt>":   strings.Repeat("<scr", slowInputSize/8) + "<script>alert(1)</script>" + strings.Repeat("ipt>", slowInputSize/8),
		"unclosed <script ":         repetition("<script "),
		"unclosed <style>":          repetition("<style>"),
		"unclosed comments":         repetition("<!-- >"),
		"unclosed <link ":           repetition("<link "),
		"unclosed <a ":              repetition("<a "),
		"a run of <":                repetition("<"),
		"unclosed <div ":            repetition("<div "),
		"unclosed email-only parts": repetition("<a data-email-only>"),
	}
	for label, input := range shapes {
		start := time.Now()
		text := HTMLToText(input)
		elapsed := time.Since(start)
		t.Logf("%s: %s", label, elapsed.Round(time.Millisecond))
		limit := slowInputMax
		if raceEnabled {
			limit *= 10
		}
		if elapsed > limit {
			t.Errorf("%s took %s, want under %s", label, elapsed, limit)
		}
		_ = text
	}
}

// htmlToText 是转换器，不是清洗器：upstream 也只断言它的耗时。它的输入是已经清洗过的邮件 HTML，
// 输出是 text/plain MIME 部分，一段字面文本 "<script " 在那里什么也跑不起来；真正挡住输入的是
// content 包的清洗器（见 content/slow_test.go）。
