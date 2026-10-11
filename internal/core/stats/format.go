package stats

import (
	"math"
	"strconv"
	"strings"
)

// The format helpers lib/format.js defines, restricted to the ones `stats` prints. They return ""
// for a value that cannot be shown — upstream returns null and its components render nothing.
//
// Two characters are load-bearing and copied exactly: the minus is U+2212 (not a hyphen) and the
// thousands separator is a comma. upstream's tests assert "−2.7%" and "+1,204" byte for byte.

// FormatNumber groups thousands: 1204 → "1,204".
func FormatNumber(value int) string {
	digits := strconv.Itoa(abs(value))
	var out strings.Builder
	if value < 0 {
		out.WriteString("−")
	}
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	return out.String()
}

// FormatSigned is upstream formatSigned: "+1,204", "−32", "0".
func FormatSigned(value int) string {
	switch {
	case value > 0:
		return "+" + FormatNumber(value)
	case value < 0:
		return FormatNumber(value) // FormatNumber writes the sign itself
	default:
		return "0"
	}
}

// FormatChange is upstream formatChange: a count's change as a percentage, or a rate's change in
// percentage points, both signed. A nil value formats as "".
func FormatChange(value *float64, digits int) string {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return ""
	}
	rounded := round(*value, digits)
	sign := ""
	switch {
	case rounded > 0:
		sign = "+"
	case rounded < 0:
		sign = "−"
	}
	return sign + strconv.FormatFloat(math.Abs(rounded), 'f', digits, 64) + "%"
}

// FormatRate is upstream formatRate: "41.8%" of a total, or "" when the total is zero.
func FormatRate(part, total, digits int) string {
	if total == 0 {
		return ""
	}
	return strconv.FormatFloat(float64(part)/float64(total)*100, 'f', digits, 64) + "%"
}

// FormatHour is upstream formatHour: 14 → "2 PM", 0 → "12 AM".
func FormatHour(hour int) string {
	suffix := "AM"
	if hour >= 12 {
		suffix = "PM"
	}
	display := hour % 12
	if display == 0 {
		display = 12
	}
	return strconv.Itoa(display) + " " + suffix
}

// round rounds half away from zero, which is what JavaScript's toFixed does. Go's FormatFloat
// rounds half to even, so the rounding happens before the formatting.
func round(value float64, digits int) float64 {
	factor := math.Pow(10, float64(digits))
	return math.Round(value*factor) / factor
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
