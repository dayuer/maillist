package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/checks"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/render"
	"mailcheap/internal/core/validate"
	"mailcheap/internal/mailer"
	"mailcheap/internal/store"
)

func newCampaignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "campaign",
		Short: "Manage campaigns",
	}
	cmd.AddCommand(newCampaignCreateCmd(), newCampaignListCmd(), newCampaignSendCmd())
	return cmd
}

func newCampaignCreateCmd() *cobra.Command {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a draft from a file",
		Args:  cobra.NoArgs,
		RunE:  runCampaignCreate,
	}
	flags := create.Flags()
	flags.String("newsletter", "", "newsletter slug or id (required)")
	flags.String("subject", "", "subject line (required)")
	flags.String("file", "", "Markdown or HTML file holding the body (required)")
	flags.String("format", string(model.FormatMarkdown), "markdown or html")
	flags.String("preview-text", "", "inbox preview text")
	for _, required := range []string{"newsletter", "subject", "file"} {
		if err := create.MarkFlagRequired(required); err != nil {
			panic(err)
		}
	}
	return create
}

func runCampaignCreate(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	ref, _ := flags.GetString("newsletter")
	subject, _ := flags.GetString("subject")
	path, _ := flags.GetString("file")
	format, _ := flags.GetString("format")
	preview, _ := flags.GetString("preview-text")

	if format != string(model.FormatMarkdown) && format != string(model.FormatHTML) {
		return fmt.Errorf("--format must be markdown or html, not %q", format)
	}
	body, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the file on the command line
	if err != nil {
		return err
	}
	if len(body) > validate.MaxBodyBytes {
		return fmt.Errorf("%s is %d bytes; the limit is %d", path, len(body), validate.MaxBodyBytes)
	}

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletter, err := newsletterByRef(ctx, a, ref)
		if err != nil {
			return err
		}
		// model.NewCampaign carries upstream's schema defaults, archiveListed among them.
		campaign := model.NewCampaign(newsletter.ID, strings.TrimSpace(subject), time.Now().UTC())
		campaign.PreviewText = preview
		campaign.Format = model.Format(format)
		campaign.Body = string(body)
		campaign.Source = "cli"
		if err := a.Store.CreateCampaign(ctx, campaign); err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, campaign)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), campaign.ID)
		return nil
	})
}

func newCampaignListCmd() *cobra.Command {
	list := &cobra.Command{
		Use:   "list",
		Short: "List campaigns",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ref, _ := cmd.Flags().GetString("newsletter")
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				newsletterID := ""
				if ref != "" {
					newsletter, err := newsletterByRef(ctx, a, ref)
					if err != nil {
						return err
					}
					newsletterID = newsletter.ID
				}
				campaigns, err := a.Store.Campaigns(ctx, newsletterID, 50)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, campaigns)
				}
				for _, campaign := range campaigns {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n",
						campaign.ID, campaign.Status, campaign.Subject)
				}
				return nil
			})
		},
	}
	list.Flags().String("newsletter", "", "only this newsletter's campaigns")
	return list
}

func newCampaignSendCmd() *cobra.Command {
	send := &cobra.Command{
		Use:   "send [campaign-id] [campaign-subject]",
		Short: "Send a campaign, or the next step of one",
		Args:  cobra.MaximumNArgs(2),
		RunE:  runCampaignSend,
	}
	send.Flags().Int("limit", 0, "send only the next N contacts (a stepped send)")
	send.Flags().String("subject", "", "pick the draft by subject instead of by id")
	return send
}

func runCampaignSend(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	subjectFlag, _ := cmd.Flags().GetString("subject")
	yes, _ := cmd.Flags().GetBool("yes")

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if !a.Config.SendingOn {
			return errors.New("Sending is off: set SENDING_ENABLED=true") //nolint:staticcheck // ST1005: upstream wording
		}
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		newsletter, err := a.Store.Newsletter(ctx, campaign.NewsletterID)
		if err != nil {
			return err
		}
		// The pre-send checks are the ones the engine enforces; running them here turns a rejected
		// send into a readable error instead of a queued campaign that never starts.
		report := checks.Run(checks.Input{
			Campaign: campaign, Newsletter: newsletter,
			Rendered: render.Email(render.EmailInput{
				Format: campaign.Format, Body: campaign.Body, Subject: campaign.Subject,
				PreviewText: campaign.PreviewText, Newsletter: newsletter,
			}),
			SendingOn: a.Config.SendingOn, Limits: a.Config.Limits,
		})
		if blocked, ok := blockingExceptDomain(report); ok {
			return fmt.Errorf("%s: %s", blocked.ID, blocked.Message)
		}
		if !domainChecked(report) {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
				"note: the SES domain check needs `mailcheap domains` (phase 4); not verified yet")
		}
		if !yes && !confirm(cmd) {
			return errors.New("aborted")
		}

		queued, err := queueCampaign(ctx, a, campaign, limit)
		if err != nil {
			return err
		}
		if !queued.Queued {
			return errors.New(queued.Reason)
		}
		// The CLI drains its own queue: there is no serve process to do it.
		for {
			result, err := a.Mailer.SendBatch(ctx, nil)
			if err != nil {
				return err
			}
			if result.Retry {
				return errors.New("sending stopped early (a retryable SES error); run the command again")
			}
			if result.Done() == 0 {
				break
			}
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, queued)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "queued %d recipients\n", queued.Recipients)
		return nil
	})
}

func queueCampaign(ctx context.Context, a *app.App, campaign model.Campaign, limit int) (mailer.QueueResult, error) {
	if limit > 0 {
		return a.Mailer.QueueStep(ctx, campaign.ID, limit)
	}
	return a.Mailer.Queue(ctx, campaign.ID)
}

// The "domain" check asks SES whether the sending domain is verified. Phase 2 has no way to ask —
// the domains command needs the SES identity API and arrives in phase 4 — so the CLI reports the
// row and does not let it block a send yet. Leaving the check in core untouched keeps its contract
// exactly upstream's; the gap lives here, where it is visible, and in the plan's notes.
const domainCheckID = "domain"

func blockingExceptDomain(report []checks.Check) (checks.Check, bool) {
	for _, check := range report {
		if check.ID == domainCheckID || check.OK || !check.Blocking {
			continue
		}
		return check, true
	}
	return checks.Check{}, false
}

func domainChecked(report []checks.Check) bool {
	for _, check := range report {
		if check.ID == domainCheckID {
			return check.OK
		}
	}
	return false
}

// campaignByRef finds the campaign the operator meant: an id when one is given, otherwise the draft
// with that subject.
func campaignByRef(ctx context.Context, a *app.App, args []string, subject string) (model.Campaign, error) {
	if len(args) > 0 && ids.Valid(args[0]) {
		return a.Store.Campaign(ctx, args[0])
	}
	if len(args) == 2 && subject == "" {
		subject = args[1]
	}
	if len(args) == 1 && subject == "" {
		subject = args[0]
	}
	if subject == "" {
		return model.Campaign{}, errors.New("name a campaign by id or by subject")
	}
	campaigns, err := a.Store.Campaigns(ctx, "", 100)
	if err != nil {
		return model.Campaign{}, err
	}
	for _, campaign := range campaigns {
		if campaign.Subject == subject {
			return campaign, nil
		}
	}
	return model.Campaign{}, fmt.Errorf("no campaign called %q: %w", subject, store.ErrNotFound)
}
