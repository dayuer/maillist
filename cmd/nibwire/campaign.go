package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/checks"
	"nibwire/internal/core/content"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/validate"
	"nibwire/internal/mailer"
	"nibwire/internal/store"
)

func newCampaignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "campaign",
		Short: "Manage campaigns",
	}
	cmd.AddCommand(
		newCampaignCreateCmd(), newCampaignListCmd(), newCampaignSendCmd(),
		newCampaignUpdateCmd(), newCampaignShowCmd(), newCampaignPreviewCmd(),
		newCampaignCheckCmd(), newCampaignTestCmd(), newCampaignScheduleCmd(),
		newCampaignPauseCmd(), newCampaignResumeCmd(), newCampaignReportCmd(),
	)
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
		// The archive's card image, recorded when the draft is made so phase 7 does not have to walk
		// every body (upstream's firstImageUrl).
		campaign.ThumbnailURL = content.FirstImageOf(campaign.Format, campaign.Body)
		if err := a.Store.CreateCampaign(ctx, campaign); err != nil {
			return err
		}
		note(cmd, a, "campaign.create", campaign.Subject,
			map[string]any{"id": campaign.ID, "subject": campaign.Subject, "format": string(campaign.Format)})
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
				campaigns, err := a.Store.Campaigns(ctx, newsletterID, 50, 0)
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
	send.Flags().Bool("dry-run", false, "report what would go out, and write nothing")
	return send
}

func runCampaignSend(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	subjectFlag, _ := cmd.Flags().GetString("subject")
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

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
		if dryRun {
			// The same checklist and the same audience, and nothing written: the point is to answer
			// "what would this do" before it does it.
			return reportDryRun(ctx, a, cmd, campaign, newsletter, limit)
		}
		// The pre-send checks are the ones the engine enforces; running them here turns a rejected
		// send into a readable error instead of a queued campaign that never starts.
		report, _, err := runChecks(ctx, a, campaign, newsletter)
		if err != nil {
			return err
		}
		if blocked, ok := checks.Blocked(report); ok {
			return fmt.Errorf("%s: %s", blocked.ID, blocked.Message)
		}
		// SEND_APPROVAL=manual parks the send for `nibwire approve` instead of doing it now.
		if jobID, held, err := queueOrHold(ctx, a, campaign, limit); err != nil {
			return err
		} else if held {
			if jsonOutput(cmd) {
				return printJSON(cmd, map[string]any{"jobId": jobID, "holding": true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "held for approval: approve %s\n", jobID)
			return nil
		}
		if !yes && !confirm(cmd) {
			return errors.New("aborted")
		}

		serverIsSending, err := a.Store.ServeIsRunning(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		queued, err := queueCampaign(ctx, a, campaign, limit)
		if err != nil {
			return err
		}
		if !queued.Queued {
			return errors.New(queued.Reason)
		}
		if !serverIsSending {
			// Nothing else is sending, so the command drains its own queue — and reports what that
			// left behind rather than assuming it finished.
			if err := drainQueue(ctx, a); err != nil {
				return err
			}
			if err := sendOutcome(ctx, a, campaign.ID, queued.Recipients); err != nil {
				return err
			}
		}
		note(cmd, a, "campaign.send", campaign.ID, map[string]any{
			"id": campaign.ID, "subject": campaign.Subject,
			"recipients": queued.Recipients, "limit": limit, "sentByServer": serverIsSending,
		})
		if jsonOutput(cmd) {
			return printJSON(cmd, map[string]any{
				"recipients": queued.Recipients, "sentByServer": serverIsSending,
			})
		}
		if serverIsSending {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"queued %d recipients; the running server is sending them\n", queued.Recipients)
			return nil
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "queued %d recipients\n", queued.Recipients)
		return nil
	})
}

func queueCampaign(ctx context.Context, a *app.App, campaign model.Campaign, limit int) (mailer.QueueResult, error) {
	if limit > 0 {
		return a.Mailer.QueueStep(ctx, campaign.ID, limit, mailer.Charge{})
	}
	return a.Mailer.Queue(ctx, campaign.ID, mailer.Charge{})
}

// The "domain" check asks SES whether the sending domain is verified. Phase 2 skipped it because
// there was no way to ask; phase 4 wired the SES identity API (see domains.go), so the check blocks
// again, exactly as it does upstream.
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
	campaigns, err := a.Store.Campaigns(ctx, "", 100, 0)
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
