package slug

import (
	"strings"
	"testing"
)

// 上游 lib/slug.js 的 slugify：折叠重音、小写、连字符、截断且不以连字符结尾。
func TestSlugFoldsAccentsAndTrims(t *testing.T) {
	if got := Slug("Issue 12: L'équipe", 80); got != "issue-12-l-equipe" {
		t.Errorf("got %q", got)
	}
	if got := Slug(strings.Repeat("a", 100), 80); got != strings.Repeat("a", 80) {
		t.Errorf("got %q", got)
	}
	if got := Slug("Hello --- ", 80); got != "hello" {
		t.Errorf("got %q", got)
	}
}
