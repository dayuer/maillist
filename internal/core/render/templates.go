package render

import (
	"regexp"
	"strings"

	"mailcheap/internal/core/model"
)

// The email shell is the upstream template string, split at its interpolation points. It is built
// with a strings.Builder rather than html/template, which would escape the already-cleaned body.
const fontStack = "-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif"

const emailShellStart = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light">
<title>`

const emailShellStyle = `</title>
<style>
.body a { color: #18181b; }
.body img { max-width: 100%; height: auto; border-radius: 8px; }
.body h1, .body h2, .body h3 { line-height: 1.3; margin: 32px 0 12px; color: #09090b; }
.body p, .body ul, .body ol { margin: 0 0 16px; }
.body blockquote { margin: 0 0 16px; padding-left: 16px; border-left: 3px solid #e4e4e7; color: #52525b; }
.body code { font-family: ui-monospace, Menlo, monospace; font-size: 14px; background: #f4f4f5; padding: 2px 4px; border-radius: 4px; }
.body hr { border: 0; border-top: 1px solid #e4e4e7; margin: 32px 0; }
</style>
</head>
<body style="margin:0;padding:0;background:#ffffff;">
`

const emailShellOpen = `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#ffffff;">
<tr><td align="center" style="padding:32px 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;font-family:` + fontStack + `;font-size:16px;line-height:1.65;color:#27272a;">
`

const emailShellClose = `</table>
</td></tr>
</table>
</body>
</html>`

// wrapMarkdownBody is upstream wrapMarkdownBody: the reader-facing shell around a Markdown issue.
func wrapMarkdownBody(bodyHTML, subject, previewText, footer, webURL string) string {
	var b strings.Builder
	b.WriteString(emailShellStart)
	b.WriteString(EscapeHTML(subject))
	b.WriteString(emailShellStyle)
	b.WriteString(preheaderHTML(previewText))
	b.WriteString(emailShellOpen)
	if webURL != "" {
		b.WriteString(`<tr><td align="right" style="padding:0 0 24px;font-size:12px;line-height:1;">` +
			readOnlineLink(webURL) + "</td></tr>\n")
	}
	b.WriteString(`<tr><td class="body">` + bodyHTML + "</td></tr>\n")
	b.WriteString(footer + "\n")
	b.WriteString(emailShellClose)
	return b.String()
}

// preheaderHTML pads the inbox preview so clients stop pulling body text into it.
func preheaderHTML(previewText string) string {
	if previewText == "" {
		return ""
	}
	return `<div style="display:none;max-height:0;overflow:hidden;opacity:0;" data-preheader>` +
		EscapeHTML(previewText) + strings.Repeat("&#847;&zwnj;&nbsp;", 40) + "</div>"
}

func readOnlineLink(webURL string) string {
	return `<a href="` + EscapeHTML(webURL) + `" style="color:#a1a1aa;text-decoration:none;">Read online</a>`
}

// footerHTML is the subscriber footer row of a Markdown email.
func footerHTML(n model.Newsletter, unsubscribeURL string) string {
	address := ""
	if n.PostalAddress != "" {
		address = "<br>" + EscapeHTML(n.PostalAddress)
	}
	return `<tr><td style="padding:32px 0 0;font-size:12px;line-height:1.6;color:#71717a;border-top:1px solid #e4e4e7;">
You're receiving this because you subscribed to ` + EscapeHTML(newsletterName(n)) + `.<br>
<a href="` + unsubscribeURL + `" style="color:#71717a;text-decoration:underline;">Unsubscribe</a>` + address + `
</td></tr>`
}

// footerText is the plain-text footer of a Markdown email.
func footerText(n model.Newsletter, unsubscribeURL string) string {
	lines := []string{
		"—",
		"You're receiving this because you subscribed to " + newsletterName(n) + ".",
		"Unsubscribe: " + unsubscribeURL,
	}
	if n.PostalAddress != "" {
		lines = append(lines, n.PostalAddress)
	}
	return strings.Join(lines, "\n")
}

func newsletterName(n model.Newsletter) string {
	if n.Name == "" {
		return "this newsletter"
	}
	return n.Name
}

var (
	bodyOpenPattern  = regexp.MustCompile(`(?i)<body[^>]*>`)
	bodyClosePattern = regexp.MustCompile(`(?i)</body>`)
)

// withFooter is upstream withFooter: generated HTML (an issue built by another app) keeps its own
// design and gets the footer appended.
func withFooter(html string, n model.Newsletter, unsubscribeURL, previewText, webURL string) string {
	footer := `<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center" style="padding:0 20px 32px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;font-family:` +
		fontStack + `;">` + footerHTML(n, unsubscribeURL) + "</table></td></tr></table>"
	// Its column width is unknown, so the link sits centered above it.
	readOnline := ""
	if webURL != "" {
		readOnline = `<div style="padding:12px 16px 0;text-align:center;font-family:` + fontStack +
			`;font-size:12px;line-height:1.4;">` + readOnlineLink(webURL) + "</div>"
	}
	top := preheaderHTML(previewText) + readOnline
	// Only a <body before the last ">" can match; when one does, so does the first, where the replace stops.
	var withTop string
	if bodyOpenPattern.MatchString(html[:strings.LastIndex(html, ">")+1]) {
		withTop = replaceFirst(bodyOpenPattern, html, func(match string) string { return match + top })
	} else {
		withTop = top + html
	}
	if strings.Contains(withTop, UnsubscribePlaceholder) {
		return withTop
	}
	if bodyClosePattern.MatchString(withTop) {
		return replaceFirst(bodyClosePattern, withTop, func(string) string { return footer + "</body>" })
	}
	return withTop + footer
}

// replaceFirst replaces only the first match, as JavaScript's String.replace does for a
// non-global pattern.
func replaceFirst(re *regexp.Regexp, s string, fn func(string) string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + fn(s[loc[0]:loc[1]]) + s[loc[1]:]
}

func fillFields(html, subject, previewText string) string {
	html = strings.ReplaceAll(html, SubjectPlaceholder, EscapeHTML(subject))
	return strings.ReplaceAll(html, PreviewTextPlaceholder, EscapeHTML(previewText))
}
