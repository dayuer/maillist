// Package model holds the records every layer passes around. Field sets follow the upstream
// Mongoose models (marclou/mailcheap@67d13b0 models/*.js) minus what died with the dashboard.
package model

import (
	"time"

	"nibwire/internal/core/ids"
)

// Format is how a campaign body is written. The upstream "rich" format (TipTap) is gone.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatHTML     Format = "html"
)

type CampaignStatus string

const (
	CampaignDraft     CampaignStatus = "draft"
	CampaignScheduled CampaignStatus = "scheduled"
	CampaignSending   CampaignStatus = "sending"
	CampaignPaused    CampaignStatus = "paused"
	CampaignSent      CampaignStatus = "sent"
	CampaignCanceled  CampaignStatus = "canceled"
	CampaignFailed    CampaignStatus = "failed"
)

// Queueable reports whether a campaign in status s may start sending (design §11, invariant 3).
func (s CampaignStatus) Queueable() bool {
	return s == CampaignDraft || s == CampaignScheduled || s == CampaignPaused
}

type ContactStatus string

const (
	ContactActive       ContactStatus = "active"
	ContactPending      ContactStatus = "pending"
	ContactUnsubscribed ContactStatus = "unsubscribed"
	ContactBounced      ContactStatus = "bounced"
	ContactComplained   ContactStatus = "complained"
	ContactDormant      ContactStatus = "dormant"
)

type MessageKind string

const (
	KindCampaign     MessageKind = "campaign"
	KindLastChance   MessageKind = "last-chance"
	KindWelcome      MessageKind = "welcome"
	KindConfirmation MessageKind = "confirmation"
	KindTest         MessageKind = "test"
)

// MessageStatus adds claimed and unknown to the upstream set (design D14).
type MessageStatus string

const (
	MessagePending    MessageStatus = "pending"
	MessageClaimed    MessageStatus = "claimed"
	MessageSent       MessageStatus = "sent"
	MessageDelivered  MessageStatus = "delivered"
	MessageBounced    MessageStatus = "bounced"
	MessageComplained MessageStatus = "complained"
	MessageFailed     MessageStatus = "failed"
	MessageSuppressed MessageStatus = "suppressed"
	MessageUnknown    MessageStatus = "unknown"
)

type SuppressionReason string

const (
	SuppressBounce    SuppressionReason = "bounce"
	SuppressComplaint SuppressionReason = "complaint"
	SuppressManual    SuppressionReason = "manual"
)

// Content is a subject, preview text and Markdown body (welcome and confirmation emails).
type Content struct {
	Subject     string
	PreviewText string
	Markdown    string
}

type Newsletter struct {
	ID                  string
	Slug                string
	Name                string
	ImageURL            string
	FromName            string
	FromEmail           string
	ReplyTo             string
	PostalAddress       string
	ArchiveEnabled      bool
	Description         string
	SocialLinks         []string
	AuthorBio           string
	AuthorImageURL      string
	RequireConfirmation bool
	CleanupEnabled      bool
	CleanupIssues       int // 0 = default
	CleanupDays         int // 0 = default
	WelcomeEnabled      bool
	Welcome             Content
	Confirmation        Content // empty Subject = built-in confirmation email
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Campaign struct {
	ID            string
	NewsletterID  string
	Subject       string
	PreviewText   string
	Format        Format
	Body          string // Markdown or HTML, per Format
	Status        CampaignStatus
	AudienceLimit int // 0 = everyone
	Source        string
	ScheduledAt   *time.Time
	StartedAt     *time.Time
	SentAt        *time.Time
	TestSentAt    *time.Time
	ArchiveSlug   string
	ThumbnailURL  string
	// ReservedDay is the UTC day this campaign's audience was reserved against the send ledger, as
	// store.Day formats it, or "" for one that has never been queued. It is written with the
	// reservation itself, and a resume on a later day reserves today's allowance for what is left
	// before it sends (design D15, §11 invariant 4).
	ReservedDay string
	// ArchiveListed follows the upstream schema default of true: the store must write true on
	// insert, because the Go zero value reads as "not listed" (upstream checks `=== false`).
	ArchiveListed bool
	ArchivePinned bool
	WebViews      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewCampaign is a draft carrying the schema defaults upstream's Mongoose model declares, so every
// creation path (the CLI now, the API and MCP later) starts from the same values.
func NewCampaign(newsletterID, subject string, now time.Time) Campaign {
	return Campaign{
		ID:           ids.New(),
		NewsletterID: newsletterID,
		Subject:      subject,
		Status:       CampaignDraft,
		Format:       FormatMarkdown,
		// Upstream: archiveListed: {type: Boolean, default: true}. The Go zero value is false, so it
		// is stated here; the store's column default is the second line of defence.
		ArchiveListed: true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

type Contact struct {
	ID             string
	NewsletterID   string
	Email          string
	Status         ContactStatus
	Source         string
	SignupIP       string
	SubscribedAt   *time.Time
	ConfirmedAt    *time.Time
	UnsubscribedAt *time.Time
	LastSentAt     *time.Time
	LastOpenAt     *time.Time
	LastClickAt    *time.Time
	LastEngagedAt  *time.Time
	LastChanceAt   *time.Time
	IgnoredIssues  int
	Sent           int
	Opens          int
	Clicks         int
	// Imported history from another provider (CSV): kept so engagement ranking works from day one.
	EmailsReceived int
	OpenRate       *float64
	ClickRate      *float64
	CreatedAt      time.Time
}

// DefaultChannel is the channel the SES_* settings describe, and the one every message used before
// there were others.
const DefaultChannel = "default"

type Message struct {
	ID                string
	Kind              MessageKind
	CampaignID        string // "" for welcome/confirmation
	NewsletterID      string
	ContactID         string
	Email             string
	Provider          string // the recipient's mailbox provider (gmail, outlook, …), from core/providers
	Status            MessageStatus
	ProviderMessageID string // the id the sending channel returned; "" until sent
	// Channel is the sending channel that carries this message; "default" is the SES account the
	// SES_* settings describe. Empty when written means "default".
	Channel      string
	Error        string
	ClaimedAt    *time.Time
	SentAt       *time.Time
	DeliveredAt  *time.Time
	OpenedAt     *time.Time
	ClickedAt    *time.Time
	BouncedAt    *time.Time
	ComplainedAt *time.Time
	CreatedAt    time.Time
}

// Limits are the sending thresholds. SES reviews an account at 5% bounces or 0.1% complaints; pause
// well before. These live in model, not config, so core packages (rules, checks) reference them
// without pulling in internal/config, which reads os (phase-1 purity gate).
type Limits struct {
	BounceRate    float64
	ComplaintRate float64
	MaxEmailBytes int
	// SendRatePerSecond is a ceiling of our own on the send rate, 0 for none. It can only lower the
	// rate the account gives: the account's numbers are the truth, and this is the operator being
	// careful (design §19, D17).
	SendRatePerSecond int
	// SendRateHeadroom is the fraction of the account's rate to aim at, SEND_RATE_HEADROOM. Sending at
	// exactly the account's limit means every rounding and every other client of the same account
	// lands on a throttle.
	SendRateHeadroom         float64
	WelcomeEmailsPer10Minute int
	MaxSendsPerDay           int
}
