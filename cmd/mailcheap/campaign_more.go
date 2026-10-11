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
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/render"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/core/schedule"
	"mailcheap/internal/jobs"
)

// campaignRefFlags are the two ways an operator names a campaign: its id, or the subject of the
// draft. `campaignByRef` accepts either, so every subcommand takes both.
func campaignRefFlags(cmd *cobra.Command) {
	cmd.Flags().String("subject", "", "pick the campaign by subject instead of by id")
}

func newCampaignUpdateCmd() *cobra.Command {
	update := &cobra.Command{
		Use:   "update [campaign-id]",
		Short: "Change a campaign's draft",
		Long: "Only the flags you pass are written. A campaign that has already gone out can still be " +
			"edited, but the change reaches nobody: it only affects the archive page and any step that " +
			"has not been sent yet.",
		Args: cobra.MaximumNArgs(1),
		RunE: runCampaignUpdate,
	}
	flags := update.Flags()
	// --subject comes from campaignRefFlags: it both names the campaign and sets the new subject,
	// which is one flag doing one obvious thing rather than two that disagree.
	campaignRefFlags(update)
	flags.String("newsletter", "", "move the campaign to another newsletter")
	flags.String("preview-text", "", "inbox preview text")
	flags.String("file", "", "Markdown or HTML file holding the body")
	flags.String("format", "", "markdown or html")
	flags.Int("limit", 0, "cap the audience at the next N contacts (0 = everyone)")
	return update
}

func runCampaignUpdate(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	subjectFlag, _ := flags.GetString("subject")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		var changed []string
		if flags.Changed("newsletter") {
			ref, _ := flags.GetString("newsletter")
			newsletter, err := newsletterByRef(ctx, a, ref)
			if err != nil {
				return err
			}
			campaign.NewsletterID = newsletter.ID
			changed = append(changed, "newsletter")
		}
		if flags.Changed("subject") {
			campaign.Subject = strings.TrimSpace(subjectFlag)
			changed = append(changed, "subject")
		}
		if flags.Changed("preview-text") {
			campaign.PreviewText, _ = flags.GetString("preview-text")
			changed = append(changed, "preview-text")
		}
		if flags.Changed("format") {
			format, _ := flags.GetString("format")
			if format != string(model.FormatMarkdown) && format != string(model.FormatHTML) {
				return fmt.Errorf("--format must be markdown or html, not %q", format)
			}
			campaign.Format = model.Format(format)
			changed = append(changed, "format")
		}
		if flags.Changed("file") {
			path, _ := flags.GetString("file")
			body, err := readBodyFile(path)
			if err != nil {
				return err
			}
			campaign.Body = body
			changed = append(changed, "file")
		}
		if flags.Changed("limit") {
			limit, _ := flags.GetInt("limit")
			if limit < 0 {
				return errors.New("--limit cannot be negative")
			}
			campaign.AudienceLimit = limit
			changed = append(changed, "limit")
		}
		if len(changed) == 0 {
			return errors.New("nothing to change: pass at least one flag")
		}
		if campaign.Subject == "" {
			return errors.New("a campaign needs a subject")
		}
		campaign.UpdatedAt = time.Now().UTC()
		if err := a.Store.UpdateCampaign(ctx, campaign); err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, campaign)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "updated %s (%s)\n", campaign.ID, strings.Join(changed, ", "))
		return nil
	})
}

func newCampaignShowCmd() *cobra.Command {
	show := &cobra.Command{
		Use:   "show [campaign-id]",
		Short: "Show one campaign",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subjectFlag, _ := cmd.Flags().GetString("subject")
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				campaign, err := campaignByRef(ctx, a, args, subjectFlag)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, campaign)
				}
				printCampaign(cmd, campaign)
				return nil
			})
		},
	}
	campaignRefFlags(show)
	return show
}

// printCampaign is the one-screen summary: what it is, where it is, and how it did.
func printCampaign(cmd *cobra.Command, c model.Campaign) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "id           %s\n", c.ID)
	_, _ = fmt.Fprintf(out, "subject      %s\n", c.Subject)
	_, _ = fmt.Fprintf(out, "status       %s\n", c.Status)
	_, _ = fmt.Fprintf(out, "format       %s\n", c.Format)
	_, _ = fmt.Fprintf(out, "body         %d bytes\n", len(c.Body))
	_, _ = fmt.Fprintf(out, "audience     %d\n", c.AudienceLimit)
	if c.ScheduledAt != nil {
		_, _ = fmt.Fprintf(out, "scheduled    %s\n", c.ScheduledAt.UTC().Format(time.RFC3339))
	}
	if c.StartedAt != nil {
		_, _ = fmt.Fprintf(out, "started      %s\n", c.StartedAt.UTC().Format(time.RFC3339))
	}
	if c.SentAt != nil {
		_, _ = fmt.Fprintf(out, "sent         %s\n", c.SentAt.UTC().Format(time.RFC3339))
	}
	if c.TestSentAt != nil {
		_, _ = fmt.Fprintf(out, "test sent    %s\n", c.TestSentAt.UTC().Format(time.RFC3339))
	}
	_, _ = fmt.Fprintf(out, "archive      slug %q listed %t pinned %t views %d\n",
		c.ArchiveSlug, c.ArchiveListed, c.ArchivePinned, c.WebViews)
}

func newCampaignPreviewCmd() *cobra.Command {
	preview := &cobra.Command{
		Use:   "preview [campaign-id]",
		Short: "Render the email without sending it",
		Long: "Writes the exact HTML and text a reader would get — tracking pixel, rewritten links, " +
			"footer and all — to two files, and prints where they are.",
		Args: cobra.MaximumNArgs(1),
		RunE: runCampaignPreview,
	}
	campaignRefFlags(preview)
	preview.Flags().String("out", "preview", "base path for the two files (preview.html / preview.txt)")
	return preview
}

func runCampaignPreview(cmd *cobra.Command, args []string) error {
	subjectFlag, _ := cmd.Flags().GetString("subject")
	base, _ := cmd.Flags().GetString("out")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		newsletter, err := a.Store.Newsletter(ctx, campaign.NewsletterID)
		if err != nil {
			return err
		}
		rendered := renderEmail(campaign, newsletter)
		if err := os.WriteFile(base+".html", []byte(rendered.HTML), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(base+".txt", []byte(rendered.Text), 0o600); err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, map[string]any{
				"html": base + ".html", "text": base + ".txt", "bytes": rendered.Bytes,
			})
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s.html and %s.txt  (%d bytes, Gmail clips over %d)\n",
			base, base, rendered.Bytes, a.Config.Limits.MaxEmailBytes)
		return nil
	})
}

func newCampaignCheckCmd() *cobra.Command {
	check := &cobra.Command{
		Use:   "check [campaign-id]",
		Short: "Run the pre-send checklist",
		Long: "The same checks the send runs, as a list you can read. A blocking failure exits " +
			"non-zero, which is what makes `check && send` a usable pair.",
		Args: cobra.MaximumNArgs(1),
		RunE: runCampaignCheck,
	}
	campaignRefFlags(check)
	return check
}

func runCampaignCheck(cmd *cobra.Command, args []string) error {
	subjectFlag, _ := cmd.Flags().GetString("subject")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		newsletter, err := a.Store.Newsletter(ctx, campaign.NewsletterID)
		if err != nil {
			return err
		}
		report, err := runChecks(ctx, a, campaign, newsletter)
		if err != nil {
			return err
		}
		if jsonOutput(cmd) {
			if err := printJSON(cmd, report); err != nil {
				return err
			}
		} else {
			for _, item := range report {
				mark := "ok  "
				if !item.OK {
					mark = "FAIL"
					if !item.Blocking {
						mark = "warn"
					}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  %-15s %s\n", mark, item.ID, item.Message)
			}
		}
		if blocked, bad := checks.Blocked(report); bad {
			return fmt.Errorf("%s: %s", blocked.ID, blocked.Message)
		}
		return nil
	})
}

func newCampaignTestCmd() *cobra.Command {
	test := &cobra.Command{
		Use:   "test [campaign-id]",
		Short: "Send a test copy to one address",
		Long: "The subject gets a [Test] prefix, and the message is filed under a sentinel contact so " +
			"it never counts toward the campaign's report.",
		Args: cobra.MaximumNArgs(1),
		RunE: runCampaignTest,
	}
	campaignRefFlags(test)
	test.Flags().String("to", "", "address to send the test to (required)")
	if err := test.MarkFlagRequired("to"); err != nil {
		panic(err)
	}
	return test
}

func runCampaignTest(cmd *cobra.Command, args []string) error {
	subjectFlag, _ := cmd.Flags().GetString("subject")
	to, _ := cmd.Flags().GetString("to")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		result, err := a.Mailer.Test(ctx, campaign.ID, to)
		if err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, result)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "test sent to %s\n", to)
		return nil
	})
}

func newCampaignScheduleCmd() *cobra.Command {
	scheduleCmd := &cobra.Command{
		Use:   "schedule [campaign-id]",
		Short: "Send a draft at a chosen time",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runCampaignSchedule,
	}
	campaignRefFlags(scheduleCmd)
	scheduleCmd.Flags().String("at", "", "RFC3339 time, or 'in 2h' (required)")
	if err := scheduleCmd.MarkFlagRequired("at"); err != nil {
		panic(err)
	}
	return scheduleCmd
}

func runCampaignSchedule(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	subjectFlag, _ := flags.GetString("subject")
	atFlag, _ := flags.GetString("at")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		at, err := parseWhen(atFlag, time.Now().UTC())
		if err != nil {
			return err
		}
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		if problem := schedule.Problem(at, time.Now().UTC()); problem != "" {
			return errors.New(problem)
		}
		newsletter, err := a.Store.Newsletter(ctx, campaign.NewsletterID)
		if err != nil {
			return err
		}
		report, err := runChecks(ctx, a, campaign, newsletter)
		if err != nil {
			return err
		}
		if blocked, bad := checks.Blocked(report); bad {
			return fmt.Errorf("%s: %s", blocked.ID, blocked.Message)
		}
		moved, err := a.Store.SetCampaignStatus(ctx, campaign.ID,
			[]model.CampaignStatus{model.CampaignDraft, model.CampaignScheduled}, model.CampaignScheduled)
		if err != nil {
			return err
		}
		if !moved {
			return fmt.Errorf("campaign is %s, only a draft or a scheduled one can be scheduled", campaign.Status)
		}
		if err := a.Store.SetCampaignSchedule(ctx, campaign.ID, &at); err != nil {
			return err
		}
		jobID, held, err := scheduleOrHold(ctx, a, campaign, at)
		if err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, map[string]any{"campaignId": campaign.ID, "scheduledAt": at, "jobId": jobID})
		}
		if held {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"scheduled %s for %s; waiting for approval (approve %s)\n",
				campaign.ID, at.Format(time.RFC3339), jobID)
			return nil
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "scheduled %s for %s\n", campaign.ID, at.Format(time.RFC3339))
		return nil
	})
}

func newCampaignPauseCmd() *cobra.Command {
	pause := &cobra.Command{
		Use:   "pause [campaign-id]",
		Short: "Stop a campaign that is sending",
		Long: "Already-claimed messages come back to the queue and nothing new is claimed: the " +
			"sender only takes messages of a campaign that is sending.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subjectFlag, _ := cmd.Flags().GetString("subject")
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				campaign, err := campaignByRef(ctx, a, args, subjectFlag)
				if err != nil {
					return err
				}
				moved, err := a.Store.SetCampaignStatus(ctx, campaign.ID,
					[]model.CampaignStatus{model.CampaignSending}, model.CampaignPaused)
				if err != nil {
					return err
				}
				if !moved {
					return fmt.Errorf("campaign is %s, only a sending one can be paused", campaign.Status)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "paused %s\n", campaign.ID)
				return nil
			})
		},
	}
	campaignRefFlags(pause)
	return pause
}

func newCampaignResumeCmd() *cobra.Command {
	resume := &cobra.Command{
		Use:   "resume [campaign-id]",
		Short: "Send the rest of a paused campaign",
		Long: "Resuming re-enters the send loop with the audience the campaign already has, exactly " +
			"as upstream's resumeCampaign does.",
		Args: cobra.MaximumNArgs(1),
		RunE: runCampaignResume,
	}
	campaignRefFlags(resume)
	return resume
}

func runCampaignResume(cmd *cobra.Command, args []string) error {
	subjectFlag, _ := cmd.Flags().GetString("subject")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		if campaign.Status != model.CampaignPaused {
			return fmt.Errorf("campaign is %s, only a paused one can resume", campaign.Status)
		}
		result, err := a.Mailer.Queue(ctx, campaign.ID)
		if err != nil {
			return err
		}
		if !result.Queued {
			return errors.New(result.Reason)
		}
		// Sending off the back of the same command keeps the CLI usable without a server; with one
		// running, the engine is already picking these messages up.
		for {
			batch, err := a.Mailer.SendBatch(ctx, nil)
			if err != nil {
				return err
			}
			if batch.Retry {
				return errors.New("sending stopped early (a retryable SES error); run the command again")
			}
			if batch.Done() == 0 {
				break
			}
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, result)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "resumed %s\n", campaign.ID)
		return nil
	})
}

// runChecks builds the pre-send checklist: the same input the send path uses, so `check` and `send`
// can never disagree about whether a campaign is sendable.
func runChecks(ctx context.Context, a *app.App, campaign model.Campaign,
	newsletter model.Newsletter) ([]checks.Check, error) {
	verified, err := verifiedDomains(ctx, a)
	if err != nil {
		return nil, err
	}
	audience, err := a.Store.CountActiveContacts(ctx, newsletter.ID)
	if err != nil {
		return nil, err
	}
	return checks.Run(checks.Input{
		Campaign: campaign, Newsletter: newsletter,
		Rendered:       renderEmail(campaign, newsletter),
		SendingOn:      a.Config.SendingOn,
		Limits:         a.Config.Limits,
		DomainVerified: rules.VerifiedFromDomain(verified, newsletter.FromEmail),
		Audience:       audience,
	}), nil
}

// renderEmail renders a campaign's Markdown or HTML into the email a reader would get, without a
// message id (so no tracking pixel): the preview and the checklist both want that view.
func renderEmail(campaign model.Campaign, newsletter model.Newsletter) render.Rendered {
	return render.Email(render.EmailInput{
		Format: campaign.Format, Body: campaign.Body, Subject: campaign.Subject,
		PreviewText: campaign.PreviewText, Newsletter: newsletter,
	})
}

// parseWhen reads --at: an RFC3339 instant, or a relative "in 30m" / "in 2h" / "in 3d".
func parseWhen(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if rest, ok := strings.CutPrefix(value, "in "); ok {
		amount, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil || amount <= 0 {
			return time.Time{}, fmt.Errorf("--at: %q is not an RFC3339 time or 'in <duration>'", value)
		}
		return now.Add(amount).UTC().Truncate(time.Minute), nil
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--at: %q is not an RFC3339 time or 'in <duration>'", value)
	}
	return at.UTC(), nil
}

// queueOrHold is the manual-approval gate on a direct send: with SEND_APPROVAL=manual the campaign
// is not queued now, it is queued for a human to release.
func queueOrHold(ctx context.Context, a *app.App, campaign model.Campaign,
	limit int) (jobID string, held bool, err error) {
	if a.Config.SendApproval != "manual" {
		return "", false, nil
	}
	payload := map[string]any{"campaignId": campaign.ID}
	if limit > 0 {
		payload["additional"] = limit
	}
	jobID, err = a.Jobs.Enqueue(ctx, jobs.KindSendCampaign, payload, time.Now().UTC())
	if err != nil {
		return "", false, err
	}
	if err := a.Jobs.Hold(ctx, jobID); err != nil {
		return "", false, err
	}
	return jobID, true, nil
}

// scheduleOrHold queues the scheduled send, or parks it for approval under SEND_APPROVAL=manual.
func scheduleOrHold(ctx context.Context, a *app.App, campaign model.Campaign,
	at time.Time) (jobID string, held bool, err error) {
	payload := map[string]any{"campaignId": campaign.ID, "scheduledAt": at.Format(time.RFC3339)}
	jobID, err = a.Jobs.Enqueue(ctx, jobs.KindSendCampaign, payload, at)
	if err != nil {
		return "", false, err
	}
	if a.Config.SendApproval != "manual" {
		return jobID, false, nil
	}
	if err := a.Jobs.Hold(ctx, jobID); err != nil {
		return "", false, err
	}
	return jobID, true, nil
}
