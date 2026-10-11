package rules

import (
	"strings"
	"testing"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/sign"
)

const testAppURL = "https://mail.example.com"

// The fixtures of tests/sending.test.mjs.
var (
	testNewsletter = model.Newsletter{
		ID: "74f1a2b3c4d5e6f7a8b9c0d2", Name: "Field notes",
		FromName: "Jane Doe", FromEmail: "jane@example.net", PostalAddress: "1 Main St, Paris",
	}
	testContact  = model.Contact{ID: "64f1a2b3c4d5e6f7a8b9c0d1", Email: "reader@example.com"}
	testCampaign = model.Campaign{
		Format: model.FormatMarkdown, Subject: "Issue 12", PreviewText: "This week",
		Body: "# Hello\n\nRead [this](https://example.com).",
	}
)

var testLimits = model.Limits{BounceRate: 0.02, ComplaintRate: 0.0008, MaxEmailBytes: 100_000}

func testSigner(t *testing.T) *sign.Signer {
	t.Helper()
	s, err := sign.New("test-session-secret-with-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func build(t *testing.T, in RecipientInput) RecipientEmail {
	t.Helper()
	if in.AppURL == "" {
		in.AppURL = testAppURL
	}
	email, err := BuildRecipientEmail(testSigner(t), in)
	if err != nil {
		t.Fatal(err)
	}
	return email
}

// tests/sending.test.mjs "recipient emails carry a personal unsubscribe link and one-click headers".
func TestRecipientEmailsCarryAPersonalUnsubscribeLinkAndOneClickHeaders(t *testing.T) {
	s := testSigner(t)
	email, err := BuildRecipientEmail(s, RecipientInput{
		AppURL: testAppURL, Campaign: testCampaign, Newsletter: testNewsletter, Contact: testContact,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := email.UnsubscribeURL[strings.LastIndex(email.UnsubscribeURL, "=")+1:]

	if email.From != `"Jane Doe" <jane@example.net>` {
		t.Errorf("from = %q", email.From)
	}
	if email.To != "reader@example.com" {
		t.Errorf("to = %q", email.To)
	}
	if email.ReplyTo != "" {
		t.Errorf("replyTo = %q", email.ReplyTo)
	}
	if email.Subject != "Issue 12" {
		t.Errorf("subject = %q", email.Subject)
	}
	for _, part := range []string{email.HTML, email.Text} {
		if strings.Contains(part, "{{unsubscribe_url}}") {
			t.Errorf("placeholder survived: %s", part)
		}
	}
	if !strings.Contains(email.HTML, `href="`+email.UnsubscribeURL+`"`) {
		t.Errorf("footer link missing: %s", email.HTML)
	}
	if !strings.Contains(email.Text, "Unsubscribe: "+email.UnsubscribeURL) {
		t.Errorf("text footer = %q", email.Text)
	}
	if ref, ok := s.VerifyUnsubscribe(token); !ok || ref != (sign.Ref{ContactID: testContact.ID, NewsletterID: testNewsletter.ID}) {
		t.Errorf("token = %+v %v", ref, ok)
	}
	wantHeaders := map[string]string{
		"List-Unsubscribe":      "<" + testAppURL + "/api/unsubscribe?token=" + token + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	}
	for k, v := range wantHeaders {
		if email.Headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, email.Headers[k], v)
		}
	}
	if email.Bytes != len(email.HTML) {
		t.Errorf("bytes = %d, want %d", email.Bytes, len(email.HTML))
	}
}

// tests/sending.test.mjs "custom HTML keeps its own unsubscribe link, filled in per recipient".
func TestCustomHTMLKeepsItsOwnUnsubscribeLink(t *testing.T) {
	html := `<html><body><p>Issue</p><a href="{{unsubscribe_url}}">Leave</a></body></html>`
	nl := testNewsletter
	nl.ReplyTo = "hello@example.net"
	email := build(t, RecipientInput{
		Campaign:   model.Campaign{Format: model.FormatHTML, Subject: "Issue", Body: html},
		Newsletter: nl, Contact: testContact,
	})
	if got := strings.Count(email.HTML, "unsubscribe?token="); got != 1 {
		t.Errorf("unsubscribe links = %d, want 1: %s", got, email.HTML)
	}
	if strings.Contains(email.HTML, "{{unsubscribe_url}}") {
		t.Errorf("placeholder survived: %s", email.HTML)
	}
	if !strings.Contains(email.Text, email.UnsubscribeURL) {
		t.Errorf("text = %q", email.Text)
	}
	if email.ReplyTo != "hello@example.net" {
		t.Errorf("replyTo = %q", email.ReplyTo)
	}
}

// tests/sending.test.mjs "issues link to their archive page; drafts to the one their subject gives".
//
// Upstream's schema defaults archiveListed to true, so a campaign built by hand must set it: the Go
// zero value would otherwise read as "not listed" (see the plan's note on model.Campaign).
func TestIssuesLinkToTheirArchivePage(t *testing.T) {
	archived := testNewsletter
	archived.Slug = "weekly"
	archived.ArchiveEnabled = true
	issue := testCampaign
	issue.ID = "84f1a2b3c4d5e6f7a8b9c0d3"
	issue.ArchiveSlug = "issue-12-is-out"
	issue.ArchiveListed = true

	sent := build(t, RecipientInput{Campaign: issue, Newsletter: archived, Contact: testContact})
	want := "https://mail.example.com/archive/weekly/p/issue-12-is-out"
	if !strings.Contains(sent.HTML, `href="`+want+`"`) {
		t.Errorf("archive link missing: %s", sent.HTML)
	}
	if !strings.HasPrefix(sent.Text, "Read online: "+want) {
		t.Errorf("text = %q", sent.Text)
	}

	draft := issue
	draft.ArchiveSlug = ""
	if got := build(t, RecipientInput{Campaign: draft, Newsletter: archived, Contact: testContact}); !strings.Contains(got.HTML, `href="https://mail.example.com/archive/weekly/p/issue-12"`) {
		t.Errorf("a draft must link to the slug its subject gives: %s", got.HTML)
	}

	if got := IssueWebURL(testAppURL, issue, testNewsletter); got != "" {
		t.Errorf("archive off: %q", got)
	}
	unlisted := issue
	unlisted.ArchiveListed = false
	if got := IssueWebURL(testAppURL, unlisted, archived); got != "" {
		t.Errorf("unlisted: %q", got)
	}
	welcome, ok := WelcomeContent(model.Newsletter{WelcomeEnabled: true, Welcome: model.Content{Subject: "Welcome", Markdown: "Hi"}})
	if !ok {
		t.Fatal("welcome content expected")
	}
	if got := IssueWebURL(testAppURL, model.Campaign{ID: issue.ID, Subject: welcome.Subject}, archived); got != "" {
		t.Errorf("welcome email has no page: %q", got)
	}
}

// tests/sending.test.mjs "each step reaches the most engaged contacts up to what was sent plus the
// step".
func TestEachStepReachesUpToWhatWasSentPlusTheStep(t *testing.T) {
	for _, tc := range []struct{ sent, additional, want int }{
		{2000, 5000, 7000},
		{2000, 0, 0}, // everyone left
		{2000, -1, 0},
	} {
		if got := NextStepLimit(tc.sent, tc.additional); got != tc.want {
			t.Errorf("NextStepLimit(%d, %d) = %d, want %d", tc.sent, tc.additional, got, tc.want)
		}
	}
}

// tests/sending.test.mjs "each recipient gets a different unsubscribe token".
func TestEachRecipientGetsADifferentUnsubscribeToken(t *testing.T) {
	first := build(t, RecipientInput{Campaign: testCampaign, Newsletter: testNewsletter, Contact: testContact})
	second := build(t, RecipientInput{
		Campaign: testCampaign, Newsletter: testNewsletter,
		Contact: model.Contact{ID: "64f1a2b3c4d5e6f7a8b9c0d9", Email: "other@example.com"},
	})
	if first.UnsubscribeURL == second.UnsubscribeURL {
		t.Error("the same token for two contacts")
	}
	if second.To != "other@example.com" {
		t.Errorf("to = %q", second.To)
	}
}

// tests/sending.test.mjs "from names are quoted, encoded, and can't inject headers".
func TestFromNamesAreQuotedEncodedAndCantInjectHeaders(t *testing.T) {
	const fromEmail = "jane@example.net"
	for _, tc := range []struct{ name, want string }{
		{"Jane, from Field notes", `"Jane, from Field notes" <` + fromEmail + `>`},
		{"  ", fromEmail},
		{"", fromEmail},
		{"Jane \"Doe\"\r\nBcc: x@evil.example", `"Jane DoeBcc: x@evil.example" <` + fromEmail + `>`},
		{"L'équipe Field notes", "=?UTF-8?B?TCfDqXF1aXBlIEZpZWxkIG5vdGVz?= <" + fromEmail + ">"},
	} {
		if got := FormatFromAddress(tc.name, fromEmail); got != tc.want {
			t.Errorf("FormatFromAddress(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// tests/sending.test.mjs "campaigns pause above the bounce or complaint limit once 100 are sent".
func TestCampaignsPauseAboveTheBounceOrComplaintLimit(t *testing.T) {
	for _, tc := range []struct {
		stats CampaignStats
		want  PauseReason
	}{
		{CampaignStats{}, ""},
		{CampaignStats{Sent: 99, Bounced: 50, Complained: 10}, ""},
		{CampaignStats{Sent: 100, Bounced: 2}, ""},
		{CampaignStats{Sent: 100, Bounced: 3}, "bounce rate"},
		{CampaignStats{Sent: 1250, Complained: 1}, ""},
		{CampaignStats{Sent: 1000, Complained: 1}, "complaint rate"},
		{CampaignStats{Sent: 5000, Bounced: 99, Complained: 4}, ""},
	} {
		if got := CampaignPauseReason(tc.stats, testLimits); got != tc.want {
			t.Errorf("CampaignPauseReason(%+v) = %q, want %q", tc.stats, got, tc.want)
		}
	}
}

// tests/sending.test.mjs "SES errors map to retry later, pause the campaign, or fail one message".
func TestSESErrorsMapToRetryPauseOrFail(t *testing.T) {
	for _, tc := range []struct {
		code, message string
		want          ErrorAction
	}{
		{"TooManyRequestsException", "TooManyRequestsException", ErrorRetry},
		{"", "Sending is off (SENDING_ENABLED is not true)", ErrorRetry},
		{"SendingPausedException", "SendingPausedException", ErrorPause},
		{"AccountSuspendedException", "AccountSuspendedException", ErrorPause},
		{"LimitExceededException", "LimitExceededException", ErrorPause},
		{"MessageRejected", "Email address is not verified. The following identities failed", ErrorPause},
		{"MessageRejected", "Message contains a virus", ErrorFail},
		{"BadRequestException", "BadRequestException", ErrorFail},
		{"", "", ErrorFail},
	} {
		if got := SendErrorAction(tc.code, tc.message); got != tc.want {
			t.Errorf("SendErrorAction(%q, %q) = %q, want %q", tc.code, tc.message, got, tc.want)
		}
	}
}

// tests/sending.test.mjs "send problems block incomplete or oversized emails".
func TestSendProblemsBlockIncompleteOrOversizedEmails(t *testing.T) {
	s := testSigner(t)
	if got := SendProblem(s, testAppURL, testCampaign, &testNewsletter, testLimits.MaxEmailBytes); got != "" {
		t.Errorf("ready campaign: %q", got)
	}
	if got := SendProblem(s, testAppURL, testCampaign, nil, testLimits.MaxEmailBytes); got != "Newsletter not found" {
		t.Errorf("got %q", got)
	}
	noFrom := testNewsletter
	noFrom.FromEmail = ""
	if got := SendProblem(s, testAppURL, testCampaign, &noFrom, testLimits.MaxEmailBytes); got != "Add a from email to the newsletter" {
		t.Errorf("got %q", got)
	}
	noSubject := testCampaign
	noSubject.Subject = "  "
	if got := SendProblem(s, testAppURL, noSubject, &testNewsletter, testLimits.MaxEmailBytes); got != "Add a subject" {
		t.Errorf("got %q", got)
	}
	noBody := testCampaign
	noBody.Body = ""
	if got := SendProblem(s, testAppURL, noBody, &testNewsletter, testLimits.MaxEmailBytes); got != "Add content" {
		t.Errorf("got %q", got)
	}
	blankHTML := model.Campaign{Format: model.FormatHTML, Subject: "Issue", Body: " "}
	if got := SendProblem(s, testAppURL, blankHTML, &testNewsletter, testLimits.MaxEmailBytes); got != "Add content" {
		t.Errorf("got %q", got)
	}

	oversized := testCampaign
	oversized.Body = strings.Repeat("word ", 30_000)
	if got := SendProblem(s, testAppURL, oversized, &testNewsletter, testLimits.MaxEmailBytes); !strings.Contains(got, "Gmail clips emails over 100 KB") {
		t.Errorf("got %q", got)
	}
}

// tests/sending.test.mjs "welcome content needs the toggle, a subject, and content".
func TestWelcomeContentNeedsTheToggleSubjectAndContent(t *testing.T) {
	welcome := model.Content{Subject: " Welcome ", Markdown: "Hello"}
	if _, ok := WelcomeContent(model.Newsletter{}); ok {
		t.Error("no welcome configured")
	}
	if _, ok := WelcomeContent(model.Newsletter{WelcomeEnabled: true, Welcome: welcome, RequireConfirmation: true}); ok {
		t.Error("a confirming newsletter says it in the confirmation email")
	}
	noSubject := welcome
	noSubject.Subject = ""
	if _, ok := WelcomeContent(model.Newsletter{WelcomeEnabled: true, Welcome: noSubject}); ok {
		t.Error("empty subject")
	}
	blank := welcome
	blank.Markdown = " \n"
	if _, ok := WelcomeContent(model.Newsletter{WelcomeEnabled: true, Welcome: blank}); ok {
		t.Error("blank markdown")
	}
	got, ok := WelcomeContent(model.Newsletter{WelcomeEnabled: true, Welcome: welcome})
	if !ok || got.Subject != "Welcome" || got.Markdown != "Hello" || got.PreviewText != "" {
		t.Errorf("got %+v %v", got, ok)
	}
	withPreview := welcome
	withPreview.PreviewText = " Reply hi "
	if got, _ := WelcomeContent(model.Newsletter{WelcomeEnabled: true, Welcome: withPreview}); got.PreviewText != "Reply hi" {
		t.Errorf("preview = %q", got.PreviewText)
	}
}

// tests/sending.test.mjs "chunk splits ids into send batches".
func TestChunkSplitsIDsIntoSendBatches(t *testing.T) {
	got := Chunk([]int{1, 2, 3, 4, 5}, 2)
	want := [][]int{{1, 2}, {3, 4}, {5}}
	if len(got) != len(want) {
		t.Fatalf("chunks = %v", got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("chunk %d = %v", i, got[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("chunk %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
	if len(Chunk([]int{}, 50)) != 0 {
		t.Error("no items, no chunks")
	}
	ids := make([]int, 120)
	if len(Chunk(ids, 50)) != 3 {
		t.Error("120 ids in batches of 50 is three batches")
	}
}

// tests/sending.test.mjs "sent emails are tracked and keep a direct unsubscribe link" is translated
// as TestSentEmailsAreTrackedAndKeepADirectUnsubscribeLink in tracked_test.go, which needs the
// tracking helpers from render.
func TestUntrackedKinds(t *testing.T) {
	for kind, want := range map[model.MessageKind]bool{
		model.KindConfirmation: true, model.KindLastChance: true,
		model.KindCampaign: false, model.KindWelcome: false, model.KindTest: false,
	} {
		if got := Untracked(kind); got != want {
			t.Errorf("Untracked(%s) = %v, want %v", kind, got, want)
		}
	}
}

func TestFeedbackIDIdentifiesTheSender(t *testing.T) {
	if got := FeedbackID("84f1a2b3c4d5e6f7a8b9c0d3", model.KindCampaign, "weekly"); got != "84f1a2b3c4d5e6f7a8b9c0d3:weekly:campaign:mailcheap" {
		t.Errorf("got %q", got)
	}
	if got := FeedbackID("", model.KindLastChance, "weekly"); got != "last-chance:weekly:last-chance:mailcheap" {
		t.Errorf("got %q", got)
	}
}

func TestSlugifyFoldsAccents(t *testing.T) {
	if got := Slugify("Issue 12: L'équipe", 80); got != "issue-12-l-equipe" {
		t.Errorf("got %q", got)
	}
	if got := Slugify(strings.Repeat("a", 100), 80); got != strings.Repeat("a", 80) {
		t.Errorf("got %q", got)
	}
	if got := Slugify("Hello --- ", 80); got != "hello" {
		t.Errorf("got %q", got)
	}
}
