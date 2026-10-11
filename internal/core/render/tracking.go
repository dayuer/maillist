package render

import (
	"net/url"
	"regexp"
	"time"

	"mailcheap/internal/core/sign"
)

const pixelStyle = "display:block;width:1px;height:1px;border:0;"

// Upstream LINK_PATTERN is /(<a\b[^>]*?\shref\s*=\s*)(["'])(https?:\/\/[^"'\s]+)\2/gi. RE2 has no
// backreferences, so the "the closing quote must match the opening one" it enforced is spelled out
// as one pattern per quote kind.
var (
	doubleQuotedLink = regexp.MustCompile(`(?i)(<a\b[^>]*?\shref\s*=\s*")(https?://[^"\s]+)"`)
	singleQuotedLink = regexp.MustCompile(`(?i)(<a\b[^>]*?\shref\s*=\s*')(https?://[^'\s]+)'`)
)

// Entities the address may hide behind before it is signed.
var escapedAmpersand = regexp.MustCompile(`(?i)&amp;|&#0*38;|&#x0*26;`)

// Unsubscribe links, in body and header, stay direct: inbox providers expect them that way.
var unsubscribePathPattern = regexp.MustCompile(`^/(api/)?unsubscribe/?$`)

func isUnsubscribeURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && unsubscribePathPattern.MatchString(u.Path)
}

// AddTracking rewrites http(s) links (except unsubscribe links) to signed click URLs and puts the
// open pixel first in <body>, so it still loads when Gmail clips a long email.
func AddTracking(s *sign.Signer, html, baseURL, messageID string) string {
	rewrite := func(re *regexp.Regexp, quote string) {
		html = re.ReplaceAllStringFunc(html, func(match string) string {
			m := re.FindStringSubmatch(match)
			target := escapedAmpersand.ReplaceAllString(m[2], "&")
			if isUnsubscribeURL(target) {
				return match
			}
			return m[1] + s.ClickURL(baseURL, messageID, target) + quote
		})
	}
	rewrite(doubleQuotedLink, `"`)
	rewrite(singleQuotedLink, "'")

	pixel := `<img src="` + s.OpenURL(baseURL, messageID) + `" width="1" height="1" alt="" style="` + pixelStyle + `">`
	if bodyOpenPattern.MatchString(html) {
		return replaceFirst(bodyOpenPattern, html, func(tag string) string { return tag + pixel })
	}
	return pixel + html
}

// Security gateways (Microsoft, Proofpoint, Mimecast…) open every link as the email arrives.
var scannerPattern = regexp.MustCompile(
	`(?i)bot|crawl|spider|preview|scan|proofpoint|mimecast|barracuda|safelinks|python|curl|wget|java/|okhttp|go-http|headless`)

const scannerWindow = 30 * time.Second

// IsLikelyScanner reports whether a visit is a security scanner rather than a reader: an empty or
// bot-like User-Agent, or a visit within 30 seconds of sending.
func IsLikelyScanner(userAgent string, sentAt *time.Time, now time.Time) bool {
	if userAgent == "" || scannerPattern.MatchString(userAgent) {
		return true
	}
	return sentAt != nil && now.Sub(*sentAt) < scannerWindow
}
