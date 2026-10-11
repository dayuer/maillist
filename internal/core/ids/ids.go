// Package ids issues the 24-hex-character IDs used as primary keys. The format matches the
// MongoDB ObjectIds of the original, so signed links keep the same shape.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var pattern = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

// New returns 12 random bytes as 24 lowercase hex characters.
func New() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Valid reports whether s is a 24-hex-character ID, in either case.
func Valid(s string) bool { return pattern.MatchString(s) }

// Normalize lowercases an ID.
func Normalize(s string) string { return strings.ToLower(s) }
