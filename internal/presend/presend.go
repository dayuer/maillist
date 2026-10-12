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

	"nibwire/internal/config"
	"nibwire/internal/core/checks"
	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
	"nibwire/internal/core/rules"
	"nibwire/internal/mailer"
)

// Identities is where the sending identities come from: mailer.IdentityReader in production.
type Identities interface {
	Identities(ctx context.Context) ([]mailer.Identity, error)
}

// Audience counts the contacts a newsletter would reach: *store.DB in production. It is an interface
// so that assembling a checklist does not drag a database into the test of it.
type Audience interface {
	CountActiveContacts(ctx context.Context, newsletterID string) (int, error)
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
func Assemble(ctx context.Context, identities Identities, audience Audience, cfg config.Config,
	campaign model.Campaign, newsletter model.Newsletter) (Report, error) {
	verified, err := identities.Identities(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("ask SES for the sending identities: %w", err)
	}
	count, err := audience.CountActiveContacts(ctx, newsletter.ID)
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
			SendingOn:      cfg.SendingOn,
			Limits:         cfg.Limits,
			DomainVerified: rules.VerifiedFromDomain(mailer.VerifiedDomains(verified), newsletter.FromEmail),
		}),
	}, nil
}
