package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/cleanup"
	"mailcheap/internal/core/model"
)

// newCleanupCmd is the list cleanup of design §13's mapping table: the contacts who ignored several
// issues in a row and showed no sign of life for months.
//
// The cleanup runs itself at send time — a cold contact gets the last-chance email instead of the
// issue, and one who never answers becomes dormant at a later send. This command is the other half:
// what it would do, and moving the ones who are past the last-chance window now rather than waiting
// for the next issue.
func newCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Show what the list cleanup would do, or do it",
		Long: "Without --run nothing is written: it counts what the next send would do and what the " +
			"list looks like now. With --run it performs the part a send would perform anyway — " +
			"contacts warned 14 days ago or more become dormant. Nobody is ever unsubscribed or " +
			"deleted.\n\n" +
			"Cleanup is off unless the newsletter switches it on, so an untouched deployment reports " +
			"zeroes rather than quietly dropping people. `newsletter update --cleanup-enabled` is " +
			"where it is switched on.",
		Args: cobra.NoArgs,
		RunE: runCleanup,
	}
	flags := cmd.Flags()
	flags.String("newsletter", "", "newsletter slug or id (default: all of them)")
	flags.Bool("run", false, "write the changes instead of only reporting them")
	return cmd
}

// cleanupReport is what the command prints and what --json gives.
type cleanupReport struct {
	Newsletter struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"newsletter"`
	Settings cleanup.Settings `json:"settings"`
	// Totals are the list as it stands: who is waiting on the last-chance email, who answered it, and
	// who has gone dormant.
	Totals cleanup.Totals `json:"totals"`
	// NextSend is what a send of the whole list would do.
	NextSend cleanup.Counts `json:"nextSend"`
	Ran      bool           `json:"ran"`
	Moved    int            `json:"moved"`
}

func runCleanup(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	ref, _ := flags.GetString("newsletter")
	run, _ := flags.GetBool("run")

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletters, err := newslettersForStats(ctx, a, ref)
		if err != nil {
			return err
		}
		reports := make([]cleanupReport, 0, len(newsletters))
		for _, newsletter := range newsletters {
			report, err := buildCleanupReport(ctx, a, newsletter, run)
			if err != nil {
				return err
			}
			if run {
				note(cmd, a, "cleanup.run", newsletter.Slug, map[string]any{
					"newsletter": newsletter.Slug, "moved": report.Moved,
				})
			}
			reports = append(reports, report)
		}
		if jsonOutput(cmd) {
			if len(reports) == 1 {
				return printJSON(cmd, reports[0])
			}
			return printJSON(cmd, reports)
		}
		for i, report := range reports {
			if i > 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout())
			}
			printCleanupReport(cmd, report)
		}
		if !run {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(),
				"\nnothing was written: run with --run to move the expired contacts")
		}
		return nil
	})
}

// buildCleanupReport walks the list exactly as a send does, reports what it found, and with run=true
// puts the ones past the last-chance window to sleep.
func buildCleanupReport(ctx context.Context, a *app.App, newsletter model.Newsletter,
	run bool) (cleanupReport, error) {
	settings := cleanup.SettingsOf(newsletter)
	report := cleanupReport{Settings: settings, Ran: run}
	report.Newsletter.ID, report.Newsletter.Slug, report.Newsletter.Name =
		newsletter.ID, newsletter.Slug, newsletter.Name

	totals, err := a.Store.CleanupTotals(ctx, newsletter.ID)
	if err != nil {
		return cleanupReport{}, err
	}
	report.Totals = totals

	// The whole list, most engaged first, judged as the next send would judge it.
	audience, err := a.Store.SelectAudience(ctx, newsletter.ID, 0)
	if err != nil {
		return cleanupReport{}, err
	}
	now := time.Now().UTC()
	split := cleanup.SplitAudience(audience, cleanup.SplitInput{Settings: settings, Now: now})
	report.NextSend = cleanup.Counts{
		Issue: len(split.Issue) + split.Received, LastChance: len(split.LastChance),
		Waiting: len(split.Waiting), Dormant: len(split.Dormant),
	}
	if !run || len(split.Dormant) == 0 {
		return report, nil
	}

	ids := make([]string, 0, len(split.Dormant))
	for _, contact := range split.Dormant {
		ids = append(ids, contact.ID)
	}
	// Nobody is moved twice and nobody who has since shown a sign of life is moved at all: the store
	// decides, and the number it reports is the one that actually changed.
	moved, err := a.Store.MakeDormant(ctx, ids)
	if err != nil {
		return cleanupReport{}, err
	}
	report.Moved = moved
	return report, nil
}

// printCleanupReport is the terminal table.
func printCleanupReport(cmd *cobra.Command, r cleanupReport) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "%s (%s)\n", r.Newsletter.Name, r.Newsletter.Slug)
	if !r.Settings.Enabled {
		_, _ = fmt.Fprintln(out, "cleanup is off: everyone gets every issue")
		return
	}
	_, _ = fmt.Fprintf(out, "settings  %d issues, %d days\n\n", r.Settings.Issues, r.Settings.Days)
	_, _ = fmt.Fprintln(out, "the list")
	_, _ = fmt.Fprintf(out, "  waiting on the last-chance email  %d\n", r.Totals.Waiting)
	_, _ = fmt.Fprintf(out, "  answered it                       %d\n", r.Totals.Kept)
	_, _ = fmt.Fprintf(out, "  dormant                           %d\n\n", r.Totals.Dormant)
	_, _ = fmt.Fprintln(out, "the next send")
	_, _ = fmt.Fprintf(out, "  recipients     %d\n", r.NextSend.Issue)
	_, _ = fmt.Fprintf(out, "  last-chance    %d\n", r.NextSend.LastChance)
	_, _ = fmt.Fprintf(out, "  waiting        %d\n", r.NextSend.Waiting)
	_, _ = fmt.Fprintf(out, "  dormant        %d\n", r.NextSend.Dormant)
	if r.Ran {
		_, _ = fmt.Fprintf(out, "\nmoved %d to dormant\n", r.Moved)
	}
}
