// Package rules holds the sending policy of a campaign: batch sizes, pause thresholds, SES error
// classification, and the per-recipient email assembly.
package rules

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/render"
	"mailcheap/internal/core/sign"
)

const (
	SendBatchSize          = 50
	MaxBatchMessages       = 100
	MinSentBeforePause     = 100
	MaxConsecutiveFailures = 5
	// MaxConfirmationsPerContact stops one address being sent the confirmation email over and over.
	MaxConfirmationsPerContact = 3
	// WelcomeWindow is how recently a confirmation must have been sent to count as "already sent".
	WelcomeWindow = 10 * time.Minute
	// TestContactID marks test sends so stats queries can exclude them (design §6.2). It is a valid
	// ID, so the unsubscribe link it signs verifies; it just matches no contact.
	TestContactID = "000000000000000000000000"
)

// Placeholders for the links only some messages carry (confirmation and last-chance emails).
const (
	ConfirmURLPlaceholder = "{{confirm_url}}"
	KeepURLPlaceholder    = "{{keep_url}}"
)

// Untracked kinds get no pixel and no click rewriting.
func Untracked(k model.MessageKind) bool {
	return k == model.KindConfirmation || k == model.KindLastChance
}

// IsCampaignKind reports whether a message is part of a campaign's own send: the issue, or a
// last-chance email for a cold contact (lib/cleanup.js). Upstream calls this set
// CAMPAIGN_MESSAGE_KINDS.
func IsCampaignKind(k model.MessageKind) bool {
	return k == model.KindCampaign || k == model.KindLastChance
}

// Chunk splits items into consecutive slices of at most size.
func Chunk[T any](items []T, size int) [][]T {
	var chunks [][]T
	for index := 0; index < len(items); index += size {
		end := min(index+size, len(items))
		chunks = append(chunks, items[index:end])
	}
	return chunks
}

var (
	headerUnsafeChars = regexp.MustCompile(`[\r\n"\\]`)
	printableASCII    = regexp.MustCompile(`^[\x20-\x7e]+$`)
)

// FormatFromAddress quotes or encodes a display name for the From header. RFC 5322 needs quotes
// around names with punctuation, SES needs RFC 2047 for non-ASCII names, and nothing may let a
// CR/LF through.
func FormatFromAddress(fromName, fromEmail string) string {
	name := strings.TrimSpace(headerUnsafeChars.ReplaceAllString(fromName, ""))
	if name == "" {
		return fromEmail
	}
	if printableASCII.MatchString(name) {
		return `"` + name + `" <` + fromEmail + ">"
	}
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(name)) + "?= <" + fromEmail + ">"
}

var feedbackUnsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// FeedbackID is the SES feedback-id header value: Gmail Postmaster Tools reports spam rates per
// field, and the last field identifies the sender.
func FeedbackID(campaignID string, kind model.MessageKind, newsletterSlug string) string {
	parts := []string{campaignID, newsletterSlug, string(kind), "mailcheap"}
	if campaignID == "" {
		parts[0] = string(kind)
	}
	for i, part := range parts {
		parts[i] = feedbackUnsafeChars.ReplaceAllString(part, "")
	}
	return strings.Join(parts, ":")
}

// WelcomeContent is the welcome email a newsletter sends, or ok=false when it sends none.
// Newsletters that confirm signups say it all in the confirmation email: one email, not two.
func WelcomeContent(n model.Newsletter) (model.Content, bool) {
	if n.RequireConfirmation {
		return model.Content{}, false
	}
	w := n.Welcome
	subject := strings.TrimSpace(w.Subject)
	if !n.WelcomeEnabled || subject == "" || strings.TrimSpace(w.Markdown) == "" {
		return model.Content{}, false
	}
	return model.Content{
		Subject:     subject,
		PreviewText: strings.TrimSpace(w.PreviewText),
		Markdown:    w.Markdown,
	}, true
}

// IssueWebURL is the archive page of an issue, or "" when it has none. A draft has no slug yet, so
// its test email links to the one the subject gives when it goes out. Welcome emails aren't issues.
func IssueWebURL(appURL string, c model.Campaign, n model.Newsletter) string {
	if !n.ArchiveEnabled || n.Slug == "" || c.ID == "" || !c.ArchiveListed {
		return ""
	}
	slug := c.ArchiveSlug
	if slug == "" {
		slug = slugify(c.Subject, 80)
	}
	if slug == "" {
		return ""
	}
	return appURL + "/archive/" + n.Slug + "/p/" + slug
}

// SendProblem is the first blocking reason in upstream's wording ("Add content", …), or "".
func SendProblem(s *sign.Signer, appURL string, c model.Campaign, n *model.Newsletter, maxEmailBytes int) string {
	if n == nil {
		return "Newsletter not found"
	}
	if n.FromEmail == "" {
		return "Add a from email to the newsletter"
	}
	if strings.TrimSpace(c.Subject) == "" {
		return "Add a subject"
	}
	if !render.HasContent(c.Body) {
		return "Add content"
	}
	email, err := BuildRecipientEmail(s, RecipientInput{
		AppURL:     appURL,
		Campaign:   c,
		Newsletter: *n,
		Contact:    model.Contact{ID: TestContactID, Email: n.FromEmail},
		MessageID:  TestContactID,
	})
	if err == nil && email.Bytes > maxEmailBytes {
		return fmt.Sprintf("The email is %d KB. Gmail clips emails over 100 KB.", (email.Bytes+999)/1000)
	}
	return ""
}

// NextStepLimit is the audience size the next send step reaches: everything sent so far plus the
// step. Without a positive step it returns 0, meaning everyone left.
func NextStepLimit(sentSoFar, additional int) int {
	if additional > 0 {
		return sentSoFar + additional
	}
	return 0
}

type CampaignStats struct{ Sent, Bounced, Complained int }

// PauseReason is why a campaign stopped, in upstream's wording.
type PauseReason string

// Rates use sent as the denominator, like SES does for account reviews.
func CampaignPauseReason(st CampaignStats, l model.Limits) PauseReason {
	if st.Sent < MinSentBeforePause {
		return ""
	}
	if float64(st.Bounced)/float64(st.Sent) > l.BounceRate {
		return "bounce rate"
	}
	if float64(st.Complained)/float64(st.Sent) > l.ComplaintRate {
		return "complaint rate"
	}
	return ""
}

type ErrorAction string

const (
	ErrorRetry ErrorAction = "retry"
	ErrorPause ErrorAction = "pause"
	ErrorFail  ErrorAction = "fail"
)

var (
	// Retrying later may work.
	retryErrors = map[string]bool{
		"TooManyRequestsException": true, "ThrottlingException": true, "Throttling": true,
	}
	// These fail every following send too, so the campaign pauses until the account is fixed.
	pauseErrors = map[string]bool{
		"AccountSuspendedException":          true,
		"SendingPausedException":             true,
		"MailFromDomainNotVerifiedException": true,
		"LimitExceededException":             true,
		"NotFoundException":                  true,
	}
	notVerifiedPattern = regexp.MustCompile(`(?i)not verified`)
)

// SendErrorAction classifies an SES error. Both the error code and the message are needed:
// "Sending is off" and MessageRejected's "not verified" are only in the message.
func SendErrorAction(code, message string) ErrorAction {
	if retryErrors[code] || strings.HasPrefix(message, "Sending is off") {
		return ErrorRetry
	}
	if pauseErrors[code] {
		return ErrorPause
	}
	if code == "MessageRejected" && notVerifiedPattern.MatchString(message) {
		return ErrorPause
	}
	return ErrorFail
}
