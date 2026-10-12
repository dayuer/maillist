package archive

import (
	"regexp"
	"time"

	"mailcheap/internal/core/model"
)

// PublishedStatuses are the states an issue can be read in: it is going out, it is paused half way,
// or it went out. A draft has no page and a discarded campaign never had one.
var PublishedStatuses = []model.CampaignStatus{
	model.CampaignSending, model.CampaignPaused, model.CampaignSent,
}

// Published reports whether an issue is on the archive at all.
func Published(status model.CampaignStatus) bool {
	for _, published := range PublishedStatuses {
		if status == published {
			return true
		}
	}
	return false
}

// Listed reports whether an issue appears in the archive's lists. Upstream checks `archiveListed !==
// false`: the schema default is true and an unlisted issue keeps its page, it just isn't linked.
func Listed(c model.Campaign) bool {
	return Published(c.Status) && c.ArchiveSlug != "" && c.ArchiveListed
}

// IssueDate is upstream getIssueDate: when the issue went out, or when it started going out. A
// paused half-sent issue is dated by when it began rather than by when it ends, because that is the
// date its readers saw.
func IssueDate(c model.Campaign) *time.Time {
	if c.SentAt != nil {
		return c.SentAt
	}
	return c.StartedAt
}

// HasAPage reports whether an issue has a page to link to: the same conditions the API's
// published-issues list uses, so a link and a page cannot disagree.
func HasAPage(c model.Campaign, n model.Newsletter) bool {
	return n.ArchiveEnabled && c.ArchiveSlug != "" && Published(c.Status)
}

// botUserAgent matches the tools that fetch a page without a person behind them: crawlers, link
// previewers, headless browsers and command-line clients. It is upstream's BOT_PATTERN from
// app/api/views/route.js, character for character, and an empty User-Agent counts as one of them —
// a browser that says nothing is not a reader.
var botUserAgent = regexp.MustCompile(
	`(?i)bot|crawl|spider|slurp|preview|facebookexternalhit|headless|lighthouse|curl|wget|python|axios|go-http`)

// IsBotUserAgent reports whether a reader's User-Agent is a machine's.
func IsBotUserAgent(userAgent string) bool {
	return userAgent == "" || botUserAgent.MatchString(userAgent)
}
