// Package stats is the arithmetic behind `mailcheap stats`: the subscriber curve, the hourly send
// series and the period-over-period comparisons. It is pure — the days and hours are bucketed here,
// in Go, because SQLite has no IANA time zones (see the phase 4 plan, §0.3) — and the SQL that
// fetches the rows lives in internal/store.
package stats

// PeriodDays is upstream PERIOD_DAYS: the table covers the last 30 days and the 30 before.
const PeriodDays = 30

// PeriodDayKeys is upstream periodDayKeys: the previous period's days then the current one's, so the
// last key is today. With days = 30 that is 60 keys.
func PeriodDayKeys(todayKey string, days int) []string {
	out := make([]string, 0, days*2)
	for index := 0; index < days*2; index++ {
		out = append(out, ShiftDateKey(todayKey, index-days*2+1))
	}
	return out
}

// DayPoint is one day of the subscriber curve.
type DayPoint struct {
	Key    string
	Total  int
	Joined int
	Left   int
}

// Series is upstream buildSubscriberSeries: the count when the range starts, then a point per day.
type Series struct {
	Start int
	Days  []DayPoint
}

// BuildSubscriberSeries walks the days. Events dated before the range fold into the starting count;
// events dated after it are ignored, exactly as upstream does — the range is what is being shown.
func BuildSubscriberSeries(dayKeys []string, baseline int, joins, leaves map[string]int) Series {
	firstKey := ""
	if len(dayKeys) > 0 {
		firstKey = dayKeys[0]
	}
	start := baseline
	for key, count := range joins {
		if key < firstKey {
			start += count
		}
	}
	for key, count := range leaves {
		if key < firstKey {
			start -= count
		}
	}

	total := start
	days := make([]DayPoint, 0, len(dayKeys))
	for _, key := range dayKeys {
		joined, left := joins[key], leaves[key]
		total += joined - left
		days = append(days, DayPoint{Key: key, Total: total, Joined: joined, Left: left})
	}
	return Series{Start: start, Days: days}
}

// Periods is upstream splitPeriods: the curve cut in two, with the changes between the ends.
type Periods struct {
	Previous       []DayPoint
	Current        []DayPoint
	Total          int
	Growth         int
	PreviousGrowth int
}

// SplitPeriods cuts a Series at periodDays. Growth is this period's change; PreviousGrowth is the
// one before it, so a dip can be told from a slowdown.
func SplitPeriods(series Series, periodDays int) Periods {
	cut := min(periodDays, len(series.Days))
	previous := series.Days[:cut]
	current := series.Days[cut:]

	previousEnd := series.Start
	if len(previous) > 0 {
		previousEnd = previous[len(previous)-1].Total
	}
	total := previousEnd
	if len(current) > 0 {
		total = current[len(current)-1].Total
	}
	return Periods{
		Previous: previous, Current: current,
		Total: total, Growth: total - previousEnd, PreviousGrowth: previousEnd - series.Start,
	}
}

// HourRow is one (day, hour) bucket the store read back.
type HourRow struct {
	Day   string
	Hour  int
	Count int
}

// HourPoint is one hour of today and yesterday. Today is nil for an hour that has not happened yet:
// a bar that is not there yet is not a zero.
type HourPoint struct {
	Hour      int
	Today     *int
	Yesterday int
}

// BuildHourlySeries is upstream buildHourlySeries: 24 buckets, today's newest hour the current one.
func BuildHourlySeries(rows []HourRow, todayKey string, currentHour int) []HourPoint {
	yesterdayKey := ShiftDateKey(todayKey, -1)
	type bucket struct {
		day  string
		hour int
	}
	counts := make(map[bucket]int, len(rows))
	for _, row := range rows {
		counts[bucket{row.Day, row.Hour}] = row.Count
	}

	out := make([]HourPoint, 0, 24)
	for hour := 0; hour < 24; hour++ {
		point := HourPoint{Hour: hour, Yesterday: counts[bucket{yesterdayKey, hour}]}
		if hour <= currentHour {
			today := counts[bucket{todayKey, hour}]
			point.Today = &today
		}
		out = append(out, point)
	}
	return out
}

// CompareCount is the change of a count in percent, or nil when there is nothing to compare with —
// a ratio against zero is not a number worth printing.
func CompareCount(current, previous int) *float64 {
	if previous == 0 {
		return nil
	}
	value := float64(current-previous) / float64(previous) * 100
	return &value
}

// CompareRate is the difference between two rates in percentage points, or nil when either side has
// no denominator.
func CompareRate(part, total, previousPart, previousTotal int) *float64 {
	if total == 0 || previousTotal == 0 {
		return nil
	}
	value := (float64(part)/float64(total) - float64(previousPart)/float64(previousTotal)) * 100
	return &value
}
