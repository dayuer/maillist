package checks

import (
	"strings"
	"testing"
	"unicode/utf16"

	"nibwire/internal/core/model"
)

// subject-length 按 UTF-16 码元算，不是按 rune。
//
// FI-A4 from the phase 1-3 audit. Upstream reads subject.length, which is what a JavaScript string's
// length is: an emoji outside the BMP is two code units there. Counting runes made a subject of
// emoji look half as long as the inbox would render it, so the warning never fired. It is a warning
// and not a block, which is why nothing else depended on it.
func TestSubjectLengthCountsUTF16CodeUnits(t *testing.T) {
	// 36 emoji: 36 runes, 72 UTF-16 code units, over the 70 the check warns at.
	emoji := strings.Repeat("🎉", 36)
	if got := len([]rune(emoji)); got != 36 {
		t.Fatalf("the fixture is %d runes, want 36", got)
	}
	if got := len(utf16.Encode([]rune(emoji))); got != 72 {
		t.Fatalf("the fixture is %d UTF-16 code units, want 72", got)
	}
	if ok := checkOK(t, emoji, "subject-length"); ok {
		t.Error("a 72-code-unit subject passed the 70-character warning")
	}

	// 35 emoji is 70 code units: exactly at the limit, and it passes.
	if ok := checkOK(t, strings.Repeat("🎉", 35), "subject-length"); !ok {
		t.Error("a 70-code-unit subject was warned about")
	}
	// Plain ASCII is unaffected.
	if ok := checkOK(t, strings.Repeat("a", 70), "subject-length"); !ok {
		t.Error("a 70-character ASCII subject was warned about")
	}
	if ok := checkOK(t, strings.Repeat("a", 71), "subject-length"); ok {
		t.Error("a 71-character ASCII subject passed")
	}
}

func checkOK(t *testing.T, subject, id string) bool {
	t.Helper()
	report := Run(Input{
		Campaign:   model.Campaign{Subject: subject, PreviewText: "Hi", Body: "Text"},
		Newsletter: model.Newsletter{PostalAddress: "1 Main St"},
		Rendered:   renderedFor("Text"),
		SendingOn:  true,
		Limits:     model.Limits{MaxEmailBytes: 100_000},
	})
	for _, check := range report {
		if check.ID == id {
			return check.OK
		}
	}
	t.Fatalf("no %s check in the report", id)
	return false
}
