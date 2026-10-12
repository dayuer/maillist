package slug

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	combiningMarks  = regexp.MustCompile(`[\x{0300}-\x{036f}]`)
	nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)
)

// Slug is upstream lib/slug.js slugify: accent-folded, lowercased, hyphenated, at most maxLength
// characters and never ending in a hyphen. It names the files an operator uploads and the archive's
// author pages.
//
// It lives in its own package because both internal/core/rules and internal/core/archive need it,
// and one of them imports the other.
func Slug(value string, maxLength int) string {
	s := norm.NFKD.String(strings.ToLower(value))
	s = combiningMarks.ReplaceAllString(s, "")
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxLength {
		s = s[:maxLength]
	}
	return strings.TrimRight(s, "-")
}
