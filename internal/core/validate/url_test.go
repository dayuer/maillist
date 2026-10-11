package validate

import "testing"

// 退订路径的判定要和 URL 解析器一致：点段、百分号编码、尾斜杠。
//
// FI-A2/FI-A3 and FI-B from the phase 1-3 audit. Upstream reads the pathname through the WHATWG
// parser, which removes dot segments and decodes percent-escapes; Go's url.Parse decodes into
// URL.Path but leaves dot segments, so the cleaning is explicit. Both sides of the port — the link
// rewriter and the click classifier — read this one function, or a link could be rewritten and then
// counted.
func TestUnsubscribePath(t *testing.T) {
	cases := map[string]bool{
		"https://mail.example.com/unsubscribe":                   true,
		"https://mail.example.com/unsubscribe?token=abc.def":     true,
		"https://mail.example.com/unsubscribe/?token=abc.def":    true,
		"https://mail.example.com/api/unsubscribe?token=abc.def": true,
		"https://mail.example.com/api/unsubscribe/":              true,
		// Dot segments, which a URL parser removes before anything looks at the path.
		"https://mail.example.com/a/../unsubscribe?token=x":      true,
		"https://mail.example.com/./unsubscribe":                 true,
		"https://mail.example.com/a/b/../../unsubscribe?token=x": true,
		// Percent-escapes are decoded into Path, as WHATWG does.
		"https://mail.example.com/%75nsubscribe?token=x": true,
		// Not the endpoint.
		"https://mail.example.com/unsubscribe-guide": false,
		"https://mail.example.com/blog/unsubscribe":  false,
		"https://mail.example.com/":                  false,
		"https://mail.example.com/unsubscribex":      false,
		// A relative or unparsable value is not the endpoint either.
		"/unsubscribe": true,
		"":             false,
		"not a url":    false,
		"https://mail.example.com/a/../subscribex": false,
	}
	for value, want := range cases {
		if got := UnsubscribePath(value); got != want {
			t.Errorf("UnsubscribePath(%q) = %v, want %v", value, got, want)
		}
	}
}
