package render

import (
	"strings"
	"testing"

	"nibwire/internal/core/sign"
)

// 上游测试没覆盖的边角：双 href、含另一种引号的 URL、点段的退订路径。
//
// The phase 1-3 audit's FI-A2 (and FI-A3 downstream): the port rewrote links in two passes, one per
// quote kind, where upstream does one pass with an alternation and a backreference. The three cases
// below are where that showed. Upstream's own tests do not reach them — which is exactly why the
// divergence survived translation — so they are pinned here.
func TestTrackingLinkEdges(t *testing.T) {
	signer := newTestSigner(t)
	const messageID = "0123456789abcdef01234567"
	base := "https://mail.example.com"

	cases := []struct {
		name string
		html string
		// want is the fragment that must appear after rewriting; absent means "not rewritten".
		rewritten []string
		untouched []string
	}{
		{
			name:      "the first href of a tag is rewritten, a second is not",
			html:      `<a href="https://example.com/one" href='https://example.com/two'>Read</a>`,
			rewritten: []string{"/c/" + messageID},
			// Upstream's single pass consumes the first match and resumes after it, where there is no
			// <a to start another; the second href stays a plain address. Two passes rewrote both.
			untouched: []string{"href='https://example.com/two'"},
		},
		{
			name: "an address containing the other quote kind is left alone",
			// The closing quote cannot match the opening one, which \2 rejected.
			html:      `<a href="https://example.com/a?q='b'">Read</a>`,
			untouched: []string{`href="https://example.com/a?q='b'"`},
		},
		{
			name: "an unsubscribe link behind a dot segment stays direct",
			// A WHATWG parser removes the dot segments, so this is the unsubscribe page upstream and
			// must not go through the click redirect.
			html:      `<a href="https://mail.example.com/anything/../unsubscribe?token=x">Unsubscribe</a>`,
			untouched: []string{"https://mail.example.com/anything/../unsubscribe?token=x"},
		},
		{
			name:      "a plain unsubscribe link stays direct",
			html:      `<a href="https://mail.example.com/unsubscribe?token=x">Unsubscribe</a>`,
			untouched: []string{"https://mail.example.com/unsubscribe?token=x"},
		},
		{
			name:      "an ordinary link is rewritten",
			html:      `<a href="https://example.com/one">Read</a>`,
			rewritten: []string{"/c/" + messageID},
		},
		{
			name:      "a single-quoted link is rewritten",
			html:      `<a href='https://example.com/one'>Read</a>`,
			rewritten: []string{"/c/" + messageID},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AddTracking(signer, tc.html, base, messageID)
			for _, fragment := range tc.rewritten {
				if !strings.Contains(got, fragment) {
					t.Errorf("want %q in:\n%s", fragment, got)
				}
			}
			for _, fragment := range tc.untouched {
				if !strings.Contains(got, fragment) {
					t.Errorf("want %q left alone in:\n%s", fragment, got)
				}
			}
		})
	}
}

// newTestSigner builds the signer the tracking tests need.
func newTestSigner(t *testing.T) *sign.Signer {
	t.Helper()
	signer, err := sign.New("test-session-secret-with-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
