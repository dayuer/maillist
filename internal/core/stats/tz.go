package stats

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	// The zone database travels in the binary: the container image is distroless and has no
	// /usr/share/zoneinfo, so without this every --tz would silently fall back to UTC.
	_ "time/tzdata"
)

// DefaultTimeZoneName is lib/time-zone.js DEFAULT_TIME_ZONE.
const DefaultTimeZoneName = "UTC"

// zoneNamePattern is what a --tz may look like. LoadLocation joins the name onto a search path, so
// ".." has to be refused here rather than left to the filesystem.
var zoneNamePattern = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)

// ResolveTimeZone is upstream resolveTimeZone: a name that is empty, too long, oddly shaped or
// unknown falls back to UTC rather than failing the command. The browser's zone list can lag the
// server's, which is why upstream falls back instead of erroring, and the CLI keeps that.
func ResolveTimeZone(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || !zoneNamePattern.MatchString(name) {
		return time.UTC
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

// DateKey is the calendar day of an instant in a zone, the format Mongo's $dateToString produced.
func DateKey(at time.Time, loc *time.Location) string {
	return at.In(loc).Format("2006-01-02")
}

// HourIn is the hour of an instant in a zone.
func HourIn(at time.Time, loc *time.Location) int {
	return at.In(loc).Hour()
}

// ShiftDateKey moves a calendar key by whole days. The arithmetic is on the UTC date, which has no
// DST: the day before 2026-03-01 is 2026-02-28 whatever the zone the key was made in.
func ShiftDateKey(key string, days int) string {
	at, err := time.Parse("2006-01-02", key)
	if err != nil {
		return key
	}
	return at.AddDate(0, 0, days).Format("2006-01-02")
}

// StartOfDateKey is the instant a calendar day begins in a zone, DST included: time.Date resolves
// midnight against the offset in force that day, so the day Paris leaves summer time starts an hour
// earlier in its own terms than the day before.
func StartOfDateKey(key string, loc *time.Location) time.Time {
	at, err := time.Parse("2006-01-02", key)
	if err != nil {
		return time.Time{}
	}
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, loc)
}

// TimeZoneLabel is upstream timeZoneLabel: "GMT+3", "GMT+5:30" or "UTC".
func TimeZoneLabel(loc *time.Location, at time.Time) string {
	_, offset := at.In(loc).Zone()
	if offset == 0 {
		return "UTC"
	}
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	hours, minutes := offset/3600, (offset%3600)/60
	if minutes == 0 {
		return "GMT" + sign + strconv.Itoa(hours)
	}
	return fmt.Sprintf("GMT%s%d:%02d", sign, hours, minutes)
}
