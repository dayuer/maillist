package cleanup

import (
	"strings"
	"testing"

	"mailcheap/internal/core/model"
)

// 翻译 tests/last-chance.test.mjs 的内容部分：一封信只问一个问题，只有一个按钮。
func TestTheLastChanceEmailAsksOneQuestionWithOneButton(t *testing.T) {
	content := LastChanceContent(model.Newsletter{Name: "Field notes"})
	if content.Subject != "Do you still want Field notes?" {
		t.Errorf("subject = %q", content.Subject)
	}
	whole := content.Subject + " " + content.PreviewText + " " + content.Markdown
	if strings.Contains(strings.ToLower(whole), "action required") {
		t.Errorf("the copy sounds like a demand: %q", whole)
	}
	if got := strings.Count(content.Markdown, "<a "); got != 1 {
		t.Errorf("%d buttons, want 1", got)
	}
	if !strings.Contains(content.Markdown, KeepURLPlaceholder) {
		t.Errorf("no keep placeholder: %s", content.Markdown)
	}
	// 名字空着时仍然是一句能读的话。
	if got := LastChanceContent(model.Newsletter{}); !strings.Contains(got.Subject, "this newsletter") {
		t.Errorf("subject = %q", got.Subject)
	}
	// 名字两边的空白不该进到邮件里。
	if got := LastChanceContent(model.Newsletter{Name: "  Notes  "}); got.Subject != "Do you still want Notes?" {
		t.Errorf("subject = %q", got.Subject)
	}
}
