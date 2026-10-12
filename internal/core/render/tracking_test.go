package render

import (
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/sign"
)

const (
	trackBase      = "https://mail.example.com"
	trackMessageID = "66f1a2b3c4d5e6f7a8b9c0d1"
)

func testSigner(t *testing.T) *sign.Signer {
	t.Helper()
	s, err := sign.New("test-session-secret-with-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// tests/tracking.test.mjs "links go through Mailcheap except unsubscribe links, and the pixel comes
// first".
func TestLinksGoThroughNibwireExceptUnsubscribeLinksAndThePixelComesFirst(t *testing.T) {
	s := testSigner(t)
	html := strings.Join([]string{
		`<html><body style="margin:0"><p><a href="https://example.org/?a=1&amp;b=2">Example</a>`,
		"<a href='https://x.com/example'>X</a>",
		`<a href="mailto:hi@example.com">Mail</a> <a href="#top">Top</a>`,
		`<a href="{{unsubscribe_url}}">Unsubscribe</a>`,
		`<a href="https://mail.example.com/unsubscribe?token=abc">Unsubscribe here</a></p></body></html>`,
	}, "")
	tracked := AddTracking(s, html, trackBase, trackMessageID)

	pixel := `<img src="` + s.OpenURL(trackBase, trackMessageID) + `"`
	if !strings.HasPrefix(tracked, `<html><body style="margin:0">`+pixel) {
		t.Errorf("the pixel must come first in <body>: %s", tracked)
	}
	if !strings.Contains(tracked, `href="`+s.ClickURL(trackBase, trackMessageID, "https://example.org/?a=1&b=2")+`"`) {
		t.Errorf("double-quoted link not rewritten (entities must be decoded first): %s", tracked)
	}
	if !strings.Contains(tracked, `href='`+s.ClickURL(trackBase, trackMessageID, "https://x.com/example")+`'`) {
		t.Errorf("single-quoted link not rewritten: %s", tracked)
	}
	for _, kept := range []string{
		`href="mailto:hi@example.com"`,
		`href="#top"`,
		`href="{{unsubscribe_url}}"`,
		`href="https://mail.example.com/unsubscribe?token=abc"`,
	} {
		if !strings.Contains(tracked, kept) {
			t.Errorf("%s must be left alone: %s", kept, tracked)
		}
	}
}

// tests/tracking.test.mjs "security scanners don't count as clicks".
func TestSecurityScannersDoNotCountAsClicks(t *testing.T) {
	sentAt := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	chrome := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/141.0 Safari/537.36"
	for _, tc := range []struct {
		userAgent string
		at        time.Time
		want      bool
	}{
		{chrome, sentAt.Add(5 * time.Minute), false},
		{chrome, sentAt.Add(5 * time.Second), true}, // too fast for a person
		{"Mimecast Link Checker", sentAt.Add(600 * time.Minute), true},
		{"", sentAt, true},
	} {
		if got := IsLikelyScanner(tc.userAgent, &sentAt, tc.at); got != tc.want {
			t.Errorf("IsLikelyScanner(%q, +%s) = %v, want %v", tc.userAgent, tc.at.Sub(sentAt), got, tc.want)
		}
	}
}
