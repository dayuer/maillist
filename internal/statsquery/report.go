// Package statsquery builds the stats report: the subscriber curve, the hourly send volume and the
// two-period email figures, read out of the store and turned into the shape the CLI prints and the
// MCP get_stats tool returns.
//
// It is a package rather than a function in cmd/mailcheap because there are two callers now (the
// `stats` command and the tool), and the report is arithmetic on rows: a second copy is how the two
// would start disagreeing about the same newsletter.
package statsquery

import (
	"context"
	"time"

	"mailcheap/internal/core/model"
	"mailcheap/internal/core/stats"
	"mailcheap/internal/store"
)

// Report is the whole of `stats --json` and of get_stats: the curve, the hourly sends and the
// two-period email figures, with the labels the terminal table prints.
type Report struct {
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

// Build reads the rows and does the arithmetic. now is taken once so the curve, the hours and the
// periods all describe the same instant.
func Build(ctx context.Context, db *store.DB, newsletter model.Newsletter,
	zone *time.Location) (Report, error) {
	now := time.Now().UTC()
	todayKey := stats.DateKey(now, zone)
	dayKeys := stats.PeriodDayKeys(todayKey, stats.PeriodDays)
	rangeStart := stats.StartOfDateKey(dayKeys[0], zone)

	dayAgo := stats.StartOfDateKey(stats.ShiftDateKey(todayKey, -1), zone)
	currentSince := now.AddDate(0, 0, -stats.PeriodDays)
	previousSince := now.AddDate(0, 0, -2*stats.PeriodDays)

	activeBefore, err := db.ActiveBefore(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return Report{}, err
	}
	joins, err := db.JoiningContactsSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return Report{}, err
	}
	unsubscribes, err := db.UnsubscribingContactsSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return Report{}, err
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

	suppressed, err := db.SuppressionLeavesSince(ctx, newsletter.ID, rangeStart)
	if err != nil {
		return Report{}, err
	}
	sentTimes, err := db.SentTimes(ctx, newsletter.ID, dayAgo)
	if err != nil {
		return Report{}, err
	}
	current, previous, err := db.EmailTotalsSince(ctx, newsletter.ID, currentSince, previousSince)
	if err != nil {
		return Report{}, err
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

	report := Report{Hours: stats.BuildHourlySeries(hourRows, todayKey, stats.HourIn(now, zone))}
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
