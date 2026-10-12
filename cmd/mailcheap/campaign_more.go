package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/checks"
	"mailcheap/internal/core/content"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/providers"
	"mailcheap/internal/core/ratectl"
	"mailcheap/internal/core/render"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/core/schedule"
	"mailcheap/internal/core/stats"
	"mailcheap/internal/jobs"
	"mailcheap/internal/mailer"
	"mailcheap/internal/store"
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
			if err := content.CheckRenderable(campaign.Format, body); err != nil {
				return fmt.Errorf("%s: %w", path, err)
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
		note(cmd, a, "campaign.update", campaign.ID,
			map[string]any{"id": campaign.ID, "changed": changed})
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

// describeRate says where the rate came from, because "1/s" reads very differently when it is the
// account's own number and when it is the floor this process falls back to before it has read one.
func describeRate(rate mailer.SendingRate) string {
	if !rate.Known {
		return trimRate(rate.PerSecond) + "/s (configured; no SES quota read)"
	}
	return trimRate(rate.PerSecond) + "/s (from the SES account)"
}

// describeRemaining is the account's 24-hour allowance, the ceiling D18 refuses a step against.
func describeRemaining(rate mailer.SendingRate) string {
	switch {
	case !rate.Known:
		return "unknown (no SES quota read)"
	case rate.Remaining24h >= ratectl.Unlimited:
		return "unlimited"
	default:
		return stats.FormatNumber(rate.Remaining24h)
	}
}

// trimRate prints 450 rather than 450.000000, and a fractional rate as itself.
func trimRate(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

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
		report, audience, err := runChecks(ctx, a, campaign, newsletter)
		if err != nil {
			return err
		}
		rate := a.Mailer.SendingRate()
		estimated := rate.Estimate(audience).Round(time.Second).String()
		if jsonOutput(cmd) {
			if err := printJSON(cmd, map[string]any{
				"checks": report, "audience": audience,
				"sending": rate, "estimated": estimated,
			}); err != nil {
				return err
			}
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "audience       %s\n", stats.FormatNumber(audience))
			// What the send costs in time, and which ceiling it spends (D17, D18): "queue 200,000
			// recipients" is a different decision at 450 a second than at 1 a second, and the operator
			// is the one who decides whether to split it.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rate           %s\n", describeRate(rate))
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "24h remaining  %s\n", describeRemaining(rate))
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "estimated      %s\n", estimated)
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
		note(cmd, a, "campaign.test", campaign.ID,
			map[string]any{"id": campaign.ID, "to": to, "messageId": result.MessageID})
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
		report, _, err := runChecks(ctx, a, campaign, newsletter)
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
		note(cmd, a, "campaign.schedule", campaign.ID,
			map[string]any{"id": campaign.ID, "at": at.Format(time.RFC3339), "jobId": jobID, "held": held})
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
				note(cmd, a, "campaign.pause", campaign.ID, map[string]any{"id": campaign.ID})
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
		result, err := a.Mailer.Queue(ctx, campaign.ID, mailer.Charge{})
		if err != nil {
			return err
		}
		if !result.Queued {
			return errors.New(result.Reason)
		}
		// With a server running, its sender picks these up and this command does not: two senders
		// would double the rate limit against SES and claim messages from under each other.
		if running, err := a.Store.ServeIsRunning(ctx, time.Now().UTC()); err != nil {
			return err
		} else if !running {
			if err := drainQueue(ctx, a); err != nil {
				return err
			}
		}
		note(cmd, a, "campaign.resume", campaign.ID,
			map[string]any{"id": campaign.ID, "recipients": result.Recipients})
		if jsonOutput(cmd) {
			return printJSON(cmd, result)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "resumed %s\n", campaign.ID)
		return nil
	})
}

// runChecks builds the pre-send checklist: the same input the send path uses, so `check` and `send`
// can never disagree about whether a campaign is sendable. The audience comes back with it because
// it is the number the operator sends by — "this goes to N" — and not a check of its own.
func runChecks(ctx context.Context, a *app.App, campaign model.Campaign,
	newsletter model.Newsletter) ([]checks.Check, int, error) {
	verified, err := verifiedDomains(ctx, a)
	if err != nil {
		return nil, 0, err
	}
	audience, err := a.Store.CountActiveContacts(ctx, newsletter.ID)
	if err != nil {
		return nil, 0, err
	}
	return checks.Run(checks.Input{
		Campaign: campaign, Newsletter: newsletter,
		Rendered:       renderEmail(campaign, newsletter),
		SendingOn:      a.Config.SendingOn,
		Limits:         a.Config.Limits,
		DomainVerified: rules.VerifiedFromDomain(verified, newsletter.FromEmail),
		Audience:       audience,
	}), audience, nil
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

func newCampaignReportCmd() *cobra.Command {
	report := &cobra.Command{
		Use:   "report [campaign-id]",
		Short: "Show how an issue did",
		Long: "The issue's own counters, the same figures split by mailbox provider, and — for a send " +
			"that was capped to the most engaged — how many contacts are still waiting. Test copies " +
			"are not in any of these numbers: they are sent under a sentinel contact and a kind of " +
			"their own (design §6.2).",
		Args: cobra.MaximumNArgs(1),
		RunE: runCampaignReport,
	}
	campaignRefFlags(report)
	return report
}

func runCampaignReport(cmd *cobra.Command, args []string) error {
	subjectFlag, _ := cmd.Flags().GetString("subject")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		campaign, err := campaignByRef(ctx, a, args, subjectFlag)
		if err != nil {
			return err
		}
		counters, lastChance, err := a.Store.CampaignCountersOf(ctx, campaign.ID)
		if err != nil {
			return err
		}
		providers, err := a.Store.ProviderBreakdown(ctx, campaign.ID)
		if err != nil {
			return err
		}
		remaining, err := remainingContacts(ctx, a, campaign, counters, lastChance)
		if err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, map[string]any{
				"campaign": campaign, "stats": counters, "lastChance": lastChance,
				"providers": providers, "remaining": remaining,
			})
		}
		printCampaignReport(cmd, campaign, counters, lastChance, providers, remaining)
		return nil
	})
}

// remainingContacts is upstream countRemaining: only a send capped to the most engaged has a next
// step to offer, and the count is how many of the active contacts have not had this issue.
func remainingContacts(ctx context.Context, a *app.App, campaign model.Campaign,
	counters store.CampaignCounters, lastChance store.LastChanceCounters) (int, error) {
	if campaign.Status != model.CampaignSent || campaign.AudienceLimit == 0 {
		return 0, nil
	}
	active, err := a.Store.CountActiveContacts(ctx, campaign.NewsletterID)
	if err != nil {
		return 0, err
	}
	reached := counters.Recipients + lastChance.Recipients
	if active <= reached {
		return 0, nil
	}
	return active - reached, nil
}

func printCampaignReport(cmd *cobra.Command, campaign model.Campaign, counters store.CampaignCounters,
	lastChance store.LastChanceCounters, providerRows []store.ProviderRow, remaining int) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "%s  (%s)\n", campaign.Subject, campaign.Status)
	if campaign.SentAt != nil {
		_, _ = fmt.Fprintf(out, "sent %s\n", campaign.SentAt.UTC().Format(time.RFC3339))
	}
	if campaign.AudienceLimit > 0 {
		_, _ = fmt.Fprintf(out, "capped at %d contacts\n", campaign.AudienceLimit)
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintf(out, "%-12s %9s %9s\n", "", "issue", "last-chance")
	pair := func(label string, issue, chance int) {
		_, _ = fmt.Fprintf(out, "%-12s %9s %9s\n", label, stats.FormatNumber(issue), stats.FormatNumber(chance))
	}
	pair("recipients", counters.Recipients, lastChance.Recipients)
	pair("sent", counters.Sent, lastChance.Sent)
	pair("delivered", counters.Delivered, lastChance.Delivered)
	pair("bounced", counters.Bounced, lastChance.Bounced)
	pair("complained", counters.Complained, lastChance.Complained)
	pair("failed", counters.Failed, lastChance.Failed)

	_, _ = fmt.Fprintf(out, "\nopened       %s  (%s of sent)\n",
		stats.FormatNumber(counters.Opened), stats.FormatRate(counters.Opened, counters.Sent, 1))
	_, _ = fmt.Fprintf(out, "clicked      %s  (%s of sent)\n",
		stats.FormatNumber(counters.Clicked), stats.FormatRate(counters.Clicked, counters.Sent, 1))
	_, _ = fmt.Fprintf(out, "unsubscribed %s  (%s of sent)\n",
		stats.FormatNumber(counters.Unsubscribed), stats.FormatRate(counters.Unsubscribed, counters.Sent, 1))
	if lastChance.Dormant > 0 {
		_, _ = fmt.Fprintf(out, "dormant      %s  (dropped by the list cleanup)\n", stats.FormatNumber(lastChance.Dormant))
	}

	if len(providerRows) > 0 {
		_, _ = fmt.Fprintf(out, "\n%-10s %8s %9s %8s %8s %8s %8s\n",
			"provider", "sent", "delivered", "opened", "clicked", "bounced", "complained")
		for _, row := range providerRows {
			_, _ = fmt.Fprintf(out, "%-10s %8s %9s %8s %8s %8s %8s\n",
				providers.Name(row.Provider),
				stats.FormatNumber(row.Sent), stats.FormatNumber(row.Delivered),
				stats.FormatNumber(row.Opened), stats.FormatNumber(row.Clicked),
				stats.FormatNumber(row.Bounced), stats.FormatNumber(row.Complained))
		}
	}
	if remaining > 0 {
		_, _ = fmt.Fprintf(out, "\n%s more of the most engaged have not had it: campaign send --limit %d\n",
			stats.FormatNumber(remaining), remaining)
	}
}

// sendOutcome reports what the CLI's own drain left behind.
//
// A campaign that paused part-way through is not a success: the engine pauses when the bounce or
// complaint rate crosses its limit, and saying "queued N recipients" over a half-sent issue hid
// that. The count of what did go out is the useful part of the message (the phase 1-3 audit's
// ST-I7).
func sendOutcome(ctx context.Context, a *app.App, campaignID string, recipients int) error {
	campaign, err := a.Store.Campaign(ctx, campaignID)
	if err != nil {
		return err
	}
	if campaign.Status != model.CampaignPaused {
		return nil
	}
	counters, _, err := a.Store.CampaignCountersOf(ctx, campaignID)
	if err != nil {
		return err
	}
	return fmt.Errorf("the campaign paused after %s of %s: the bounce or complaint rate crossed its limit; "+
		"campaign resume sends the rest", stats.FormatNumber(counters.Sent), stats.FormatNumber(recipients))
}

// reportDryRun is what `campaign send --dry-run` prints: the checklist, the audience, and how many
// of them would go out in this step. Nothing is written — no messages, no status change, no budget
// reservation — so it is safe to run against a live campaign.
func reportDryRun(ctx context.Context, a *app.App, cmd *cobra.Command, campaign model.Campaign,
	newsletter model.Newsletter, limit int) error {
	report, audience, err := runChecks(ctx, a, campaign, newsletter)
	if err != nil {
		return err
	}
	reached, err := a.Store.ReachedContacts(ctx, campaign.ID)
	if err != nil {
		return err
	}
	contacts, err := a.Store.SelectAudience(ctx, campaign.NewsletterID, campaign.AudienceLimit)
	if err != nil {
		return err
	}
	fresh := 0
	for _, contact := range contacts {
		if _, already := reached[contact.ID]; !already {
			fresh++
		}
	}
	step := fresh
	if limit > 0 && limit < step {
		step = limit
	}
	if jsonOutput(cmd) {
		return printJSON(cmd, map[string]any{
			"campaignId": campaign.ID, "status": campaign.Status, "checks": report,
			"audience": audience, "wouldSend": step, "dryRun": true,
		})
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "dry run: nothing was written")
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  status       %s\n", campaign.Status)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  audience     %s active contacts\n", stats.FormatNumber(audience))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  would send   %s\n", stats.FormatNumber(step))
	for _, item := range report {
		if !item.OK {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s: %s\n", "check", item.ID, item.Message)
		}
	}
	return nil
}

// drainQueue is the CLI's own sender: it drains queued messages until there are none left. It is
// what a command does when no serve process holds the heartbeat.
func drainQueue(ctx context.Context, a *app.App) error {
	// The rate follows the account (D17), and this process has no poller: without a reading the send
	// goes at one a second, which is the right answer to an account that cannot be asked and a very
	// wrong one to an account that simply was not. A failure here is not fatal — slow is the safe
	// direction — but it is worth saying.
	if _, err := a.ReadQuota(ctx); err != nil {
		a.Log.Warn("could not read the SES sending quota; sending at one a second", "error", err)
	}
	for {
		batch, err := a.Mailer.SendBatch(ctx, nil)
		if err != nil {
			return err
		}
		if batch.Retry {
			return errors.New("sending stopped early (a retryable SES error); run the command again")
		}
		if batch.Done() == 0 {
			return nil
		}
	}
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
