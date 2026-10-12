package render

import (
	"regexp"
	"time"

	"nibwire/internal/core/sign"
	"nibwire/internal/core/validate"
)

const pixelStyle = "display:block;width:1px;height:1px;border:0;"

// trackedLink is upstream LINK_PATTERN: /(<a\b[^>]*?\shref\s*=\s*)(["'])(https?:\/\/[^"'\s]+)\2/gi.
//
// RE2 has no backreferences, so the fourth group is matched loosely and checked in the replacement:
// a link whose closing quote does not match its opening one is left alone, which is what \2 did.
// The middle group excludes both quote kinds, as upstream's does.
//
// One pattern and one pass matter for two reasons. The replacement is never re-examined, so a
// rewritten click URL cannot be rewritten again; and after a match the scan resumes at its end, so a
// second href in the same tag is left alone exactly as upstream leaves it. Two passes (one per quote
// kind) broke both: a tag with a double-quoted and a single-quoted href got both rewritten.
var trackedLink = regexp.MustCompile(`(?i)(<a\b[^>]*?\shref\s*=\s*)(["'])(https?://[^"'\s]+)(["'])`)

// Entities the address may hide behind before it is signed.
var escapedAmpersand = regexp.MustCompile(`(?i)&amp;|&#0*38;|&#x0*26;`)

// isUnsubscribeURL is upstream isUnsubscribeUrl, dot segments and all: see validate.UnsubscribePath.
func isUnsubscribeURL(value string) bool { return validate.UnsubscribePath(value) }

// AddTracking rewrites http(s) links (except unsubscribe links) to signed click URLs and puts the
// open pixel first in <body>, so it still loads when Gmail clips a long email.
func AddTracking(s *sign.Signer, html, baseURL, messageID string) string {
	html = trackedLink.ReplaceAllStringFunc(html, func(match string) string {
		m := trackedLink.FindStringSubmatch(match)
		if m[2] != m[4] {
			return match // the quotes do not pair up: not a link upstream would touch either
		}
		target := escapedAmpersand.ReplaceAllString(m[3], "&")
		if isUnsubscribeURL(target) {
			return match
		}
		return m[1] + m[2] + s.ClickURL(baseURL, messageID, target) + m[2]
	})

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
