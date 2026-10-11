package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/stats"
	"mailcheap/internal/store"
)

// statsReport is the whole of `stats --json`: the curve, the hourly sends and the two-period email
// figures, with the labels the terminal table prints.
type statsReport struct {
	Newsletter struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"newsletter"`
	TimeZone struct {
		Name    string `json:"name"`
		Label   string `json:"label"`
		Today   string `json:"today"`
		FromKey string `json:"fromKey"`
	} `json:"timezone"`
	Subscribers struct {
		Start          int              `json:"start"`
		Total          int              `json:"total"`
		Growth         int              `json:"growth"`
		PreviousGrowth int              `json:"previousGrowth"`
		Days           []stats.DayPoint `json:"days"`
	} `json:"subscribers"`
	Hours []stats.HourPoint `json:"hours"`
	Email struct {
		Current  store.EmailTotals `json:"current"`
		Previous store.EmailTotals `json:"previous"`
	} `json:"email"`
}

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
		reports := make([]statsReport, 0, len(newsletters))
		for _, newsletter := range newsletters {
			report, err := buildStats(ctx, a, newsletter, zone)
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

// buildStats reads the rows and does the arithmetic. now is taken once so the curve, the hours and
// the periods all describe the same instant.
func buildStats(ctx context.Context, a *app.App, newsletter model.Newsletter,
	zone *time.Location) (statsReport, error) {
	now := time.Now().UTC()
	todayKey := stats.DateKey(now, zone)
	dayKeys := stats.PeriodDayKeys(todayKey, stats.PeriodDays)
	rangeStart := stats.StartOfDateKey(dayKeys[0], zone)

	dayAgo := stats.StartOfDateKey(stats.ShiftDateKey(todayKey, -1), zone)
	currentSince := now.AddDate(0, 0, -stats.PeriodDays)
	previousSince := now.AddDate(0, 0, -2*stats.PeriodDays)

	activeBefore, err := a.Store.ActiveBefore(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return statsReport{}, err
	}
	joins, err := a.Store.JoiningContactsSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return statsReport{}, err
	}
	unsubscribes, err := a.Store.UnsubscribingContactsSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return statsReport{}, err
	}
	// A leave recorded as the contact was created belongs to whoever imported them, on a day nobody
	// knows: upstream filters it in the query, this port filters it here because comparing two dates
	// with a minute's slack is not something a TEXT comparison can do (see the store method).
	leavers := unsubscribes[:0]
	for _, row := range unsubscribes {
		if stats.HasLeftInMailcheap(row.UnsubscribedAt, row.CreatedAt) {
			leavers = append(leavers, row)
		}
	}
	unsubscribes = leavers

	suppressed, err := a.Store.SuppressionLeavesSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return statsReport{}, err
	}
	sentTimes, err := a.Store.SentTimes(ctx, newsletter.ID, dayAgo)
	if err != nil {
		return statsReport{}, err
	}
	current, previous, err := a.Store.EmailTotalsSince(ctx, newsletter.ID, currentSince, previousSince)
	if err != nil {
		return statsReport{}, err
	}

	contribution := stats.SubscriberContributions(stats.SubscriberInput{
		DayKeys: dayKeys, RangeStart: rangeStart, Loc: zone, ActiveBefore: activeBefore,
		Joins: joins, Unsubscribes: unsubscribes, Suppressed: suppressed,
	})
	series := stats.BuildSubscriberSeries(dayKeys, contribution.Baseline, contribution.Joins, contribution.Leaves)
	periods := stats.SplitPeriods(series, stats.PeriodDays)

	hourRows := make([]stats.HourRow, 0, len(sentTimes))
	byBucket := map[[2]int]int{}
	yesterdayKey := stats.ShiftDateKey(todayKey, -1)
	for _, sentAt := range sentTimes {
		key := stats.DateKey(sentAt, zone)
		if key != todayKey && key != yesterdayKey {
			continue
		}
		day := 1
		if key == yesterdayKey {
			day = 0
		}
		byBucket[[2]int{day, stats.HourIn(sentAt, zone)}]++
	}
	for day := 0; day <= 1; day++ {
		for hour := 0; hour < 24; hour++ {
			if count := byBucket[[2]int{day, hour}]; count > 0 {
				key := todayKey
				if day == 0 {
					key = yesterdayKey
				}
				hourRows = append(hourRows, stats.HourRow{Day: key, Hour: hour, Count: count})
			}
		}
	}

	report := statsReport{Hours: stats.BuildHourlySeries(hourRows, todayKey, stats.HourIn(now, zone))}
	report.Newsletter.ID, report.Newsletter.Slug, report.Newsletter.Name =
		newsletter.ID, newsletter.Slug, newsletter.Name
	report.TimeZone.Name, report.TimeZone.Label = zone.String(), stats.TimeZoneLabel(zone, now)
	report.TimeZone.Today, report.TimeZone.FromKey = todayKey, dayKeys[0]
	report.Subscribers.Start, report.Subscribers.Total = series.Start, periods.Total
	report.Subscribers.Growth, report.Subscribers.PreviousGrowth = periods.Growth, periods.PreviousGrowth
	report.Subscribers.Days = series.Days
	report.Email.Current, report.Email.Previous = current, previous
	return report, nil
}

// printStats is the terminal table: the same numbers, in the order an operator reads them.
func printStats(cmd *cobra.Command, r statsReport) {
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
