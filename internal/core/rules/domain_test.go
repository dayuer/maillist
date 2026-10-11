package rules

import "testing"

// Translated from the domain check on campaigns/[id]/page.js:
//
//	const fromDomain = newsletter.fromEmail.split("@")[1];
//	const isDomainVerified = Array.isArray(domains) &&
//		domains.some(({ domain, verified }) => domain === fromDomain && verified);
//
// The comparison there is `===`, so a subdomain of a verified domain is not verified. The tests keep
// that: "mail.example.net" is not "example.net".
func TestVerifiedFromDomain(t *testing.T) {
	verified := []string{"example.net", "other.example"}

	cases := []struct {
		name     string
		domains  []string
		from     string
		expected bool
	}{
		{"an exact domain", verified, "jane@example.net", true},
		{"a second verified domain", verified, "jane@other.example", true},
		{"mixed case on both sides", verified, "JANE@EXAMPLE.NET", true},
		{"padding around the entries", []string{" example.net "}, "jane@example.net", true},
		{"a domain that is not verified", verified, "jane@unverified.example", false},
		{"an empty identity list", nil, "jane@example.net", false},
		{"a subdomain of a verified domain", verified, "jane@mail.example.net", false},
		{"a suffix match", verified, "jane@notexample.net", false},
		{"no @ at all", verified, "jane", false},
		{"an empty address", verified, "", false},
		{"an empty domain", verified, "jane@", false},
		{"whitespace after the @", verified, "jane@ example.net", true},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := VerifiedFromDomain(test.domains, test.from); got != test.expected {
				t.Errorf("VerifiedFromDomain(%v, %q) = %v, want %v", test.domains, test.from, got, test.expected)
			}
		})
	}
}
