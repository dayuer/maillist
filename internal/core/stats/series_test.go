package stats

import (
	"math"
	"testing"
)

// Translated from tests/overview.test.mjs:
//
//	test("the subscriber curve starts from the earlier events and splits into two periods", …)
//
// The assertions are upstream's, with one change of notation: upstream builds the series from an
// object literal, this port passes the three maps as arguments. The values, the expected start, the
// expected totals and the expected period growth are all upstream's.
//
// The chart helpers in the same file (niceScale, linePath) are not translated: the terminal table
// replaced the charts (design §13).
func TestTheSubscriberCurveStartsFromTheEarlierEventsAndSplitsIntoTwoPeriods(t *testing.T) {
	dayKeys := PeriodDayKeys("2026-10-08", 2)
	want := []string{"2026-10-05", "2026-10-06", "2026-10-07", "2026-10-08"}
	if len(dayKeys) != len(want) {
		t.Fatalf("dayKeys = %v, want %v", dayKeys, want)
	}
	for i := range want {
		if dayKeys[i] != want[i] {
			t.Fatalf("dayKeys = %v, want %v", dayKeys, want)
		}
	}

	series := BuildSubscriberSeries(dayKeys, 100,
		map[string]int{"2026-10-01": 5, "2026-10-07": 3},
		map[string]int{"2026-10-06": 1, "2026-10-09": 2})
	if series.Start != 105 {
		t.Errorf("start = %d, want 105", series.Start)
	}
	totals := make([]int, 0, len(series.Days))
	for _, day := range series.Days {
		totals = append(totals, day.Total)
	}
	if len(totals) != 4 || totals[0] != 105 || totals[1] != 104 || totals[2] != 107 || totals[3] != 107 {
		t.Fatalf("totals = %v, want [105 104 107 107]", totals)
	}

	periods := SplitPeriods(series, 2)
	if periods.Total != 107 {
		t.Errorf("total = %d, want 107", periods.Total)
	}
	if periods.Growth != 3 {
		t.Errorf("growth = %d, want 3", periods.Growth)
	}
	if periods.PreviousGrowth != -1 {
		t.Errorf("previousGrowth = %d, want -1", periods.PreviousGrowth)
	}
	if len(periods.Current) != 2 || periods.Current[0].Key != "2026-10-07" || periods.Current[1].Key != "2026-10-08" {
		t.Fatalf("current = %+v", periods.Current)
	}
}

// Translated from tests/overview.test.mjs:
//
//	test("hours still to come today have no bar, yesterday's always do", …)
func TestHoursStillToComeTodayHaveNoBarYesterdaysAlwaysDo(t *testing.T) {
	hours := BuildHourlySeries([]HourRow{
		{Day: "2026-10-08", Hour: 9, Count: 5},
		{Day: "2026-10-07", Hour: 9, Count: 7},
		{Day: "2026-10-07", Hour: 20, Count: 2},
	}, "2026-10-08", 16)

	if len(hours) != 24 {
		t.Fatalf("len = %d, want 24", len(hours))
	}
	check := func(hour, today, yesterday int, todayPresent bool) {
		t.Helper()
		point := hours[hour]
		if point.Hour != hour || point.Yesterday != yesterday {
			t.Fatalf("hours[%d] = %+v", hour, point)
		}
		if todayPresent {
			if point.Today == nil || *point.Today != today {
				t.Fatalf("hours[%d].Today = %v, want %d", hour, point.Today, today)
			}
		} else if point.Today != nil {
			t.Fatalf("hours[%d].Today = %v, want nil", hour, *point.Today)
		}
	}
	check(9, 5, 7, true)
	check(16, 0, 0, true) // 16 is not past 16: the current hour is shown, at zero
	check(20, 0, 2, false)
}

// Translated from tests/overview.test.mjs:
//
//	test("changes need something to compare with; rates change by their difference, shown in %", …)
//
// Only the arithmetic half: the spacing and the sign characters are format_test.go's, which carries
// the rest of that same test.
func TestChangesNeedSomethingToCompareWith(t *testing.T) {
	if got := CompareCount(12, 0); got != nil {
		t.Errorf("CompareCount(12, 0) = %v, want nil", *got)
	}
	if got := CompareCount(15, 10); got == nil || *got != 50 {
		t.Errorf("CompareCount(15, 10) = %v, want 50", got)
	}
	if got := CompareRate(1, 0, 1, 10); got != nil {
		t.Errorf("CompareRate(1, 0, 1, 10) = %v, want nil", *got)
	}
	if got := CompareRate(99, 100, 98, 100); got == nil || math.Abs(*got-1) > 1e-9 {
		t.Errorf("CompareRate(99, 100, 98, 100) = %v, want 1", got)
	}
}
