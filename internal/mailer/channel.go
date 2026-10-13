package mailer

import "nibwire/internal/core/rules"

// ChannelTypeSES is the type of a channel backed by Amazon SES.
const ChannelTypeSES = "ses"

// Classifier maps an error from Send onto what the engine does next: retry, slow down, stop for the
// day, pause the campaign, or count the message as failed.
type Classifier interface {
	Classify(err error) rules.ErrorAction
}

// Channel is one way out: an SES account today, Resend or SendGrid later (design
// 2026-10-12-multi-channel-sending-design.md §3.5). It is the Sender together with everything the
// engine and the pre-send checks need to know about the account behind it.
type Channel interface {
	Sender
	QuotaReader
	IdentityReader
	Classifier
	// Name is how routes and messages refer to the channel; the SES_* settings are "default".
	Name() string
	// Type is the kind of provider: "ses" for now.
	Type() string
}

// ClassifySES reads an error the way SES writes it: by the API error code and by the message, which
// is where "Sending is off", "not verified" and the daily quota are told apart. err must be non-nil.
func ClassifySES(err error) rules.ErrorAction {
	return rules.SendErrorAction(errorCode(err), err.Error())
}

// NewChannel assembles a channel from its parts. sender, quota and identities must be non-nil. A nil
// classify means SES's rules (ClassifySES), which suit only SES-shaped errors; other providers pass
// their own.
func NewChannel(name, kind string, sender Sender, quota QuotaReader, identities IdentityReader,
	classify func(error) rules.ErrorAction) Channel {
	if classify == nil {
		classify = ClassifySES
	}
	return &composedChannel{Sender: sender, QuotaReader: quota, IdentityReader: identities,
		name: name, kind: kind, classify: classify}
}

type composedChannel struct {
	Sender
	QuotaReader
	IdentityReader
	name, kind string
	classify   func(error) rules.ErrorAction
}

func (c *composedChannel) Name() string                         { return c.name }
func (c *composedChannel) Type() string                         { return c.kind }
func (c *composedChannel) Classify(err error) rules.ErrorAction { return c.classify(err) }
