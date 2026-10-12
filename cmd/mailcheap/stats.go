package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/stats"
	"mailcheap/internal/statsquery"
)

func newStatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show subscriber growth, send volume and email rates",
		Long: "The overview the dashboard drew as charts, as a table. Days and hours follow --tz, " +
			"which defaults to UTC; an unknown zone falls back to UTC rather than failing.\n\n" +
			"Without --newsletter every newsletter is reported in turn.",
		Args: cobra.NoArgs,
		RunE: runStats,
	}
	cmd.Flags().String("newsletter", "", "newsletter slug or id (default: all of them)")
	cmd.Flags().String("tz", stats.DefaultTimeZoneName, "IANA time zone for the days and hours")
	return cmd
}

func runStats(cmd *cobra.Command, _ []string) error {
	ref, _ := cmd.Flags().GetString("newsletter")
	tzName, _ := cmd.Flags().GetString("tz")
	zone := stats.ResolveTimeZone(tzName)

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletters, err := newslettersForStats(ctx, a, ref)
		if err != nil {
			return err
		}
		reports := make([]statsquery.Report, 0, len(newsletters))
		for _, newsletter := range newsletters {
			report, err := statsquery.Build(ctx, a.Store, newsletter, zone)
			if err != nil {
				return err
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
			printStats(cmd, report)
		}
		return nil
	})
}

func newslettersForStats(ctx context.Context, a *app.App, ref string) ([]model.Newsletter, error) {
	if ref != "" {
		newsletter, err := newsletterByRef(ctx, a, ref)
		if err != nil {
			return nil, err
		}
		return []model.Newsletter{newsletter}, nil
	}
	return a.Store.Newsletters(ctx)
}

// printStats is the terminal table: the same numbers, in the order an operator reads them.
func printStats(cmd *cobra.Command, r statsquery.Report) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "%s (%s)\n", r.Newsletter.Name, r.Newsletter.Slug)
	_, _ = fmt.Fprintf(out, "%s (%s)   %s .. %s\n\n",
		r.TimeZone.Name, r.TimeZone.Label, r.TimeZone.FromKey, r.TimeZone.Today)

	change := stats.FormatChange(stats.CompareCount(r.Subscribers.Total, r.Subscribers.Total-r.Subscribers.Growth), 1)
	_, _ = fmt.Fprintf(out, "subscribers  %s   growth %s   previous period %s",
		stats.FormatNumber(r.Subscribers.Total), stats.FormatSigned(r.Subscribers.Growth), stats.FormatSigned(r.Subscribers.PreviousGrowth))
	if change != "" {
		_, _ = fmt.Fprintf(out, "   (%s)", change)
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintf(out, "  %-12s %8s %6s %6s %8s\n", "date", "joined", "left", "total", "")
	for _, day := range r.Subscribers.Days {
		_, _ = fmt.Fprintf(out, "  %-12s %8d %6d %6s\n",
			day.Key, day.Joined, day.Left, stats.FormatNumber(day.Total))
	}

	_, _ = fmt.Fprintln(out, "\nsends by hour (today / yesterday)")
	for hour := 0; hour < 24; hour++ {
		point := r.Hours[hour]
		today := "-"
		if point.Today != nil {
			today = stats.FormatNumber(*point.Today)
		}
		_, _ = fmt.Fprintf(out, "  %02d  %s / %s", hour, today, stats.FormatNumber(point.Yesterday))
		if hour%3 == 2 {
			_, _ = fmt.Fprintln(out)
		}
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "email, last 30 days vs the 30 before")
	_, _ = fmt.Fprintf(out, "  %-12s %9s %9s %9s\n", "metric", "current", "previous", "change")
	row := func(label string, current, previous int) {
		_, _ = fmt.Fprintf(out, "  %-12s %9s %9s %9s\n", label,
			stats.FormatNumber(current), stats.FormatNumber(previous),
			stats.FormatChange(stats.CompareCount(current, previous), 1))
	}
	row("sent", r.Email.Current.Sent, r.Email.Previous.Sent)
	row("delivered", r.Email.Current.Delivered, r.Email.Previous.Delivered)
	row("opened", r.Email.Current.Opened, r.Email.Previous.Opened)
	row("clicked", r.Email.Current.Clicked, r.Email.Previous.Clicked)
	row("bounced", r.Email.Current.Bounced, r.Email.Previous.Bounced)
	row("complained", r.Email.Current.Complained, r.Email.Previous.Complained)
	rate := func(label string, current, currentTotal, previous, previousTotal int) {
		_, _ = fmt.Fprintf(out, "  %-12s %9s %9s %9s\n", label,
			stats.FormatRate(current, currentTotal, 1), stats.FormatRate(previous, previousTotal, 1),
			stats.FormatChange(stats.CompareRate(current, currentTotal, previous, previousTotal), 1)+" pp")
	}
	rate("open rate", r.Email.Current.Opened, r.Email.Current.Sent, r.Email.Previous.Opened, r.Email.Previous.Sent)
	rate("click rate", r.Email.Current.Clicked, r.Email.Current.Sent, r.Email.Previous.Clicked, r.Email.Previous.Sent)
}
