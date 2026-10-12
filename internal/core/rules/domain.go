package rules

import "strings"

// DomainOf is the domain SES knows an identity by: the part after the @ of a From address, trimmed
// and lower-cased. An address with no @, or nothing after it, has no domain — "" — and an empty
// domain is never verified.
func DomainOf(fromEmail string) string {
	at := strings.LastIndex(fromEmail, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(fromEmail[at+1:]))
}

// DomainVerified is whether one domain appears in SES's verified identities. The comparison is a
// case-folded equality, so a subdomain of a verified domain is not verified: SES verifies the name it
// was given.
func DomainVerified(verifiedDomains []string, domain string) bool {
	if domain == "" {
		return false
	}
	for _, verified := range verifiedDomains {
		if strings.EqualFold(strings.TrimSpace(verified), domain) {
			return true
		}
	}
	return false
}

// VerifiedFromDomain is upstream's isDomainVerified: the sending address's domain has to appear in
// SES's verified identities. campaigns/[id]/page.js compares the newsletter's from address, not the
// campaign's, because the From header is what SES sees on the wire.
func VerifiedFromDomain(verifiedDomains []string, fromEmail string) bool {
	return DomainVerified(verifiedDomains, DomainOf(fromEmail))
}
