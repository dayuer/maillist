package rules

import "strings"

// VerifiedFromDomain is upstream's isDomainVerified: the sending address's domain has to appear in
// SES's verified identities. campaigns/[id]/page.js compares the newsletter's from address, not the
// campaign's, because the From header is what SES sees on the wire.
func VerifiedFromDomain(verifiedDomains []string, fromEmail string) bool {
	at := strings.LastIndex(fromEmail, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(fromEmail[at+1:]))
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
