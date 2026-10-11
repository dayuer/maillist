package rules

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	combiningMarks  = regexp.MustCompile(`[\x{0300}-\x{036f}]`)
	nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)
)

// slugify is upstream lib/slug.js slugify: accent-folded, lowercased, hyphenated, at most
// maxLength characters and never ending in a hyphen.
func slugify(value string, maxLength int) string {
	s := norm.NFKD.String(strings.ToLower(value))
	s = combiningMarks.ReplaceAllString(s, "")
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxLength {
		s = s[:maxLength]
	}
	return strings.TrimRight(s, "-")
}
