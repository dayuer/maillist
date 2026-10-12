package mcpsrv

import (
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/store"
)

// The views are what the read tools answer with. The core model's fields are Go names, and the tool
// arguments that set them are snake_case — the names an agent reads and the names it writes should
// be the same names, so the entities that cross this surface are mapped here (and only the fields
// that mean something to a caller: a campaign's body is not in its view because a listing of issues
// is not where anyone reads one).

type newsletterView struct {
	ID                  string `json:"id"`
	Slug                string `json:"slug"`
	Name                string `json:"name"`
	FromName            string `json:"from_name,omitempty"`
	FromEmail           string `json:"from_email"`
	ReplyTo             string `json:"reply_to,omitempty"`
	PostalAddress       string `json:"postal_address,omitempty"`
	ArchiveEnabled      bool   `json:"archive_enabled"`
	RequireConfirmation bool   `json:"require_confirmation"`
	WelcomeEnabled      bool   `json:"welcome_enabled"`
	CleanupEnabled      bool   `json:"cleanup_enabled"`
	CleanupIssues       int    `json:"cleanup_issues,omitempty"`
	CleanupDays         int    `json:"cleanup_days,omitempty"`
	CreatedAt           string `json:"created_at"`
}

func viewNewsletter(n model.Newsletter) newsletterView {
	return newsletterView{
		ID: n.ID, Slug: n.Slug, Name: n.Name, FromName: n.FromName, FromEmail: n.FromEmail,
		ReplyTo: n.ReplyTo, PostalAddress: n.PostalAddress,
		ArchiveEnabled: n.ArchiveEnabled, RequireConfirmation: n.RequireConfirmation,
		WelcomeEnabled: n.WelcomeEnabled, CleanupEnabled: n.CleanupEnabled,
		CleanupIssues: n.CleanupIssues, CleanupDays: n.CleanupDays,
		CreatedAt: stamp(n.CreatedAt),
	}
}

type campaignView struct {
	ID            string `json:"id"`
	NewsletterID  string `json:"newsletter_id"`
	Subject       string `json:"subject"`
	PreviewText   string `json:"preview_text,omitempty"`
	Status        string `json:"status"`
	Format        string `json:"format"`
	AudienceLimit int    `json:"audience_limit,omitempty"`
	ArchiveSlug   string `json:"archive_slug,omitempty"`
	ArchiveListed bool   `json:"archive_listed"`
	// BodyBytes rather than the body: a listing is not where anyone reads an issue, and a hundred of
	// them is exactly the "do not pour rows into the context" this surface is built to avoid.
	BodyBytes   int    `json:"body_bytes"`
	ScheduledAt string `json:"scheduled_at,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	SentAt      string `json:"sent_at,omitempty"`
	TestSentAt  string `json:"test_sent_at,omitempty"`
	WebViews    int    `json:"web_views,omitempty"`
}

func viewCampaign(c model.Campaign) campaignView {
	return campaignView{
		ID: c.ID, NewsletterID: c.NewsletterID, Subject: c.Subject, PreviewText: c.PreviewText,
		Status: string(c.Status), Format: string(c.Format), AudienceLimit: c.AudienceLimit,
		ArchiveSlug: c.ArchiveSlug, ArchiveListed: c.ArchiveListed, BodyBytes: len(c.Body),
		ScheduledAt: stampPtr(c.ScheduledAt), StartedAt: stampPtr(c.StartedAt),
		SentAt: stampPtr(c.SentAt), TestSentAt: stampPtr(c.TestSentAt), WebViews: c.WebViews,
	}
}

type contactView struct {
	ID             string  `json:"id"`
	Email          string  `json:"email"`
	Status         string  `json:"status"`
	Source         string  `json:"source,omitempty"`
	SubscribedAt   string  `json:"subscribed_at,omitempty"`
	UnsubscribedAt string  `json:"unsubscribed_at,omitempty"`
	Sent           int     `json:"sent"`
	Opens          int     `json:"opens"`
	Clicks         int     `json:"clicks"`
	OpenRate       float64 `json:"open_rate,omitempty"`
	ClickRate      float64 `json:"click_rate,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

func viewContact(c model.Contact) contactView {
	out := contactView{
		ID: c.ID, Email: c.Email, Status: string(c.Status), Source: c.Source,
		SubscribedAt: stampPtr(c.SubscribedAt), UnsubscribedAt: stampPtr(c.UnsubscribedAt),
		Sent: c.Sent, Opens: c.Opens, Clicks: c.Clicks, CreatedAt: stamp(c.CreatedAt),
	}
	// The engagement rates are the imported ones when there are no sends of our own to measure: a
	// contact who arrived with history is ranked by that history (core/audience).
	if c.OpenRate != nil {
		out.OpenRate = *c.OpenRate
	} else if c.Sent > 0 {
		out.OpenRate = float64(c.Opens) / float64(c.Sent)
	}
	if c.ClickRate != nil {
		out.ClickRate = *c.ClickRate
	} else if c.Sent > 0 {
		out.ClickRate = float64(c.Clicks) / float64(c.Sent)
	}
	return out
}

// providerView is one line of a campaign report.
type providerView struct {
	Provider   string `json:"provider"`
	Sent       int    `json:"sent"`
	Delivered  int    `json:"delivered"`
	Opened     int    `json:"opened"`
	Clicked    int    `json:"clicked"`
	Bounced    int    `json:"bounced"`
	Complained int    `json:"complained"`
}

func viewProvider(row store.ProviderRow) providerView {
	return providerView{
		Provider: row.Provider, Sent: row.Sent, Delivered: row.Delivered,
		Opened: row.Opened, Clicked: row.Clicked, Bounced: row.Bounced,
		Complained: row.Complained,
	}
}

// countersView is a campaign's own figures, named the way the report reads them.
type countersView struct {
	Recipients   int `json:"recipients"`
	Sent         int `json:"sent"`
	Delivered    int `json:"delivered"`
	Opened       int `json:"opened"`
	Clicked      int `json:"clicked"`
	Bounced      int `json:"bounced"`
	Complained   int `json:"complained"`
	Unsubscribed int `json:"unsubscribed"`
	Failed       int `json:"failed"`
}

func viewCounters(c store.CampaignCounters) countersView {
	return countersView{
		Recipients: c.Recipients, Sent: c.Sent, Delivered: c.Delivered, Opened: c.Opened,
		Clicked: c.Clicked, Bounced: c.Bounced, Complained: c.Complained,
		Unsubscribed: c.Unsubscribed, Failed: c.Failed,
	}
}

type lastChanceView struct {
	Recipients int `json:"recipients"`
	Sent       int `json:"sent"`
	Delivered  int `json:"delivered"`
	Bounced    int `json:"bounced"`
	Complained int `json:"complained"`
	Failed     int `json:"failed"`
	Dormant    int `json:"dormant"`
}

func viewLastChance(c store.LastChanceCounters) lastChanceView {
	return lastChanceView{
		Recipients: c.Recipients, Sent: c.Sent, Delivered: c.Delivered, Bounced: c.Bounced,
		Complained: c.Complained, Failed: c.Failed, Dormant: c.Dormant,
	}
}

// stamp is the one time format this surface prints: RFC3339 in UTC, which is what an agent can
// compare and what a client can parse without being told a time zone.
func stamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

func stampPtr(at *time.Time) string {
	if at == nil {
		return ""
	}
	return stamp(*at)
}
