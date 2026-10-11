package render

import (
	"regexp"
	"strings"
)

// A button straight to the new subscriber's webmail gets far more confirmation clicks than "check
// your inbox".
var webmails = []struct {
	pattern *regexp.Regexp
	name    string
	url     func(fromEmail string) string
}{
	{
		pattern: regexp.MustCompile(`^(gmail|googlemail)\.com$`),
		name:    "Gmail",
		url: func(fromEmail string) string {
			query := "in:anywhere newer_than:1d"
			if fromEmail != "" {
				query = "from:" + fromEmail + " " + query
			}
			return "https://mail.google.com/mail/u/0/#search/" + encodeComponent(query)
		},
	},
	{pattern: regexp.MustCompile(`^(outlook|hotmail|live|msn)\.[a-z.]+$`), name: "Outlook",
		url: func(string) string { return "https://outlook.live.com/mail/0/" }},
	{pattern: regexp.MustCompile(`^(yahoo|ymail)\.[a-z.]+$`), name: "Yahoo Mail",
		url: func(string) string { return "https://mail.yahoo.com/" }},
	{pattern: regexp.MustCompile(`^(icloud|me|mac)\.com$`), name: "iCloud Mail",
		url: func(string) string { return "https://www.icloud.com/mail" }},
	{pattern: regexp.MustCompile(`^(proton\.me|protonmail\.com|pm\.me)$`), name: "Proton Mail",
		url: func(string) string { return "https://mail.proton.me/u/0/inbox" }},
}

// Inbox returns the webmail link for the reader's provider, or ("", "") when it isn't known.
func Inbox(email, fromEmail string) (name, url string) {
	domain := ""
	if _, after, ok := strings.Cut(email, "@"); ok {
		domain = strings.ToLower(after)
	}
	if domain == "" {
		return "", ""
	}
	for _, w := range webmails {
		if w.pattern.MatchString(domain) {
			return w.name, w.url(fromEmail)
		}
	}
	return "", ""
}

// encodeComponent is JavaScript's encodeURIComponent: a query value with spaces as %20 rather than
// "+", and !~*'()-._ left alone.
func encodeComponent(s string) string {
	const unreserved = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
