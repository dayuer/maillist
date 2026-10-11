// Package providers maps an email address onto the mailbox provider that runs it. `campaign report`
// groups by it, and every message records it when it is created (lib/sending.js toMessages).
package providers

import "strings"

// Other is the bucket every address we cannot place lands in.
const Other = "other"

// domains is lib/providers.js DOMAINS, in declaration order so a listing of the map is stable.
var domains = []struct {
	Name    string
	Domains []string
}{
	{"gmail", []string{"gmail.com", "googlemail.com"}},
	{"outlook", []string{"outlook.com", "hotmail.com", "live.com", "msn.com", "hotmail.co.uk",
		"hotmail.fr", "live.fr", "outlook.fr"}},
	{"yahoo", []string{"yahoo.com", "ymail.com", "rocketmail.com", "yahoo.fr", "yahoo.co.uk", "aol.com"}},
	{"icloud", []string{"icloud.com", "me.com", "mac.com"}},
	{"proton", []string{"proton.me", "protonmail.com", "pm.me"}},
}

// labels is lib/providers.js PROVIDER_LABELS.
var labels = map[string]string{
	"gmail": "Gmail", "outlook": "Outlook", "yahoo": "Yahoo",
	"icloud": "iCloud", "proton": "Proton", Other: "Other",
}

// byDomain is PROVIDER_BY_DOMAIN: the first provider to claim a domain owns it.
var byDomain = func() map[string]string {
	out := map[string]string{}
	for _, provider := range domains {
		for _, domain := range provider.Domains {
			if _, taken := out[domain]; !taken {
				out[domain] = provider.Name
			}
		}
	}
	return out
}()

// Mailbox is upstream mailboxProvider: the domain after the first "@", lowercased, or "other".
//
// It looks at the first "@" only, exactly as upstream does — `a@b@c` is provider "b" there, and here
// too, because the two implementations are compared against the same addresses.
func Mailbox(email string) string {
	address := strings.TrimSpace(email)
	at := strings.Index(address, "@")
	if at < 0 {
		return Other
	}
	domain := strings.ToLower(strings.TrimSpace(address[at+1:]))
	if provider, known := byDomain[domain]; known {
		return provider
	}
	return Other
}

// Name is the label a report prints for a provider key. An unknown key is itself: the stored value
// is the truth, and an unrecognised one is worth seeing rather than hiding behind "Other".
func Name(provider string) string {
	if label, ok := labels[provider]; ok {
		return label
	}
	return provider
}

// Known lists the provider keys the report can group by, for documentation and tests.
func Known() []string {
	out := make([]string, 0, len(domains)+1)
	for _, provider := range domains {
		out = append(out, provider.Name)
	}
	return append(out, Other)
}
