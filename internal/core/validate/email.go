// Package validate holds the input checks shared by every entry point.
package validate

import (
	"regexp"
	"strings"
)

// MaxEmailLength is the longest address SES accepts as a single recipient.
const MaxEmailLength = 254

const maxLocalPartLength = 64

// MaxBodyBytes caps issue bodies from the CLI, the API and MCP.
const MaxBodyBytes = 500_000

// Dot-atom local part and hostname labels: rejects quoted strings, commas, and display names.
var emailPattern = regexp.MustCompile(`^[a-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+(?:\.[a-z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+)*@(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

var addressPattern = regexp.MustCompile(`<([^<>]+)>\s*$`)

// NormalizeEmail trims and lowercases value, returning "" when SES couldn't take it as one recipient.
func NormalizeEmail(value string) string {
	email := strings.ToLower(strings.TrimSpace(value))
	if len(email) > MaxEmailLength || strings.Index(email, "@") > maxLocalPartLength {
		return ""
	}
	if !emailPattern.MatchString(email) {
		return ""
	}
	return email
}

// NormalizeAddress accepts "Dana <dana@example.com>" or a bare address, as mail headers and SES events give them.
func NormalizeAddress(value string) string {
	if m := addressPattern.FindStringSubmatch(value); m != nil {
		return NormalizeEmail(m[1])
	}
	return NormalizeEmail(value)
}
