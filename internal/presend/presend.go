// Package presend assembles the pre-send checklist of a campaign.
//
// It exists so that there is exactly one answer to "would this campaign send": the CLI's
// `campaign check`, the MCP surface's `check_campaign` and its `send_campaign` gate all go through
// Assemble, which renders the email, asks SES which domains are verified and counts the audience in
// this one order. Two assemblies were how the phase 9 audit's #44 found room for the two surfaces to
// disagree about a campaign that is one bad render away from unsendable.
package presend

import (
	"context"
	"fmt"
	"sort"

	"nibwire/internal/config"
	"nibwire/internal/core/checks"
	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
	"nibwire/internal/core/rules"
)

// Identities answers the one identity question the checklist asks: is this domain verified for
// sending through this channel? mailer.SESIdentity implements it, with a tenant configured and
// without one — and with a tenant it asks SES about that domain alone instead of enumerating the
// account (phase 9 #57).
type Identities interface {
	VerifiedDomain(ctx context.Context, domain string) (bool, error)
}

// ChannelIdentities is the same question per channel. One account being verified says nothing about
// another: the domain a rule sends half the audience through has to be verified there too, or half
// the issue is accepted by the provider and dropped (design §4.6). A channel with no reader here is
// one this process cannot send through at all.
type ChannelIdentities interface {
	IdentitiesOf(channel string) (Identities, bool)
}

// Audience counts the contacts a newsletter would reach, and how the routing rules would split them:
// *store.DB in production. It is an interface so that assembling a checklist does not drag a database
// into the test of it.
type Audience interface {
	CountActiveContacts(ctx context.Context, newsletterID string) (int, error)
	// ChannelsForAudience is how many messages each channel would carry for that audience, which is
	// what the domain check is asked about.
	ChannelsForAudience(ctx context.Context, newsletterID string, limit int) (map[string]int, error)
}

// Report is the checklist and the number of people it concerns. The audience is not a check of its
// own — it is the number the operator sends by, "this goes to N" — which is why it comes back beside
// the rows rather than inside them.
type Report struct {
	Checks   []checks.Check
	Audience int
}

// Assemble builds the checklist for one campaign of one newsletter. Reading the identities can fail,
// and that failure is returned rather than swallowed: a send must not proceed on a domain nobody
// could confirm, because SES accepts the call and then drops the mail, which looks like a successful
// send and is not.
func Assemble(ctx context.Context, channels ChannelIdentities, audience Audience, cfg config.Config,
	campaign model.Campaign, newsletter model.Newsletter) (Report, error) {
	domain := rules.DomainOf(newsletter.FromEmail)
	count, err := audience.CountActiveContacts(ctx, newsletter.ID)
	if err != nil {
		return Report{}, err
	}
	unverified, err := unverifiedChannels(ctx, channels, audience, campaign, newsletter, domain)
	if err != nil {
		return Report{}, err
	}
	// The render has no message id, so no tracking pixel: this is the preview and the checklist's view
	// of the email, not one recipient's copy of it.
	rendered := render.Email(render.EmailInput{
		Format: campaign.Format, Body: campaign.Body, Subject: campaign.Subject,
		PreviewText: campaign.PreviewText, Newsletter: newsletter,
	})
	return Report{
		Audience: count,
		Checks: checks.Run(checks.Input{
			Campaign: campaign, Newsletter: newsletter,
			Rendered: checks.Rendered{
				HTML:        rendered.HTML,
				Bytes:       rendered.Bytes,
				Placeholder: render.UnsubscribePlaceholder,
				HasContent:  render.HasContent(campaign.Body),
			},
			SendingOn:          cfg.SendingOn,
			Limits:             cfg.Limits,
			UnverifiedChannels: unverified,
		}),
	}, nil
}

// unverifiedChannels is the channels this campaign's mail would go out through that have not verified
// the sending domain, in a stable order — the answer the domain check is built from.
//
// It asks about the channels the rules would actually use rather than about every channel that has a
// rule: a deployment that sends one list through a second account must not be blocked by a rule for a
// domain that is not in that list. A campaign with nobody to send to has no channels yet, and the
// question falls back to the default channel, which is where its mail would go.
func unverifiedChannels(ctx context.Context, channels ChannelIdentities, audience Audience,
	campaign model.Campaign, newsletter model.Newsletter, domain string) ([]string, error) {
	used, err := audience.ChannelsForAudience(ctx, newsletter.ID, campaign.AudienceLimit)
	if err != nil {
		return nil, err
	}
	if len(used) == 0 {
		used = map[string]int{model.DefaultChannel: 0}
	}
	unverified := make([]string, 0, len(used))
	for _, channel := range sortedChannels(used) {
		identities, ok := channels.IdentitiesOf(channel)
		if !ok {
			// A channel this process cannot send through: the queue would refuse the step, and there
			// is nothing to ask about its domains.
			unverified = append(unverified, channel)
			continue
		}
		verified, err := identities.VerifiedDomain(ctx, domain)
		if err != nil {
			return nil, fmt.Errorf("ask about the sending domain for channel %s: %w", channel, err)
		}
		if !verified {
			unverified = append(unverified, channel)
		}
	}
	return unverified, nil
}

// sortedChannels is the channels of a split, in a stable order: the checklist's message lists them,
// and a map has none.
func sortedChannels(used map[string]int) []string {
	out := make([]string, 0, len(used))
	for channel := range used {
		out = append(out, channel)
	}
	sort.Strings(out)
	return out
}
