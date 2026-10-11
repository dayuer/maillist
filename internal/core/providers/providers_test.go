package providers

import "testing"

// tests/ has no providers.test.mjs: lib/providers.js is a lookup table with one exported function.
// The cases below are the table's edges — every alias, the case folding, and the addresses that are
// not addresses.
func TestMailbox(t *testing.T) {
	cases := map[string]string{
		"someone@gmail.com":       "gmail",
		"someone@googlemail.com":  "gmail",
		"someone@outlook.com":     "outlook",
		"someone@hotmail.co.uk":   "outlook",
		"someone@live.fr":         "outlook",
		"someone@yahoo.com":       "yahoo",
		"someone@aol.com":         "yahoo",
		"someone@icloud.com":      "icloud",
		"someone@mac.com":         "icloud",
		"someone@proton.me":       "proton",
		"someone@pm.me":           "proton",
		"SOMEONE@GMAIL.COM":       "gmail",
		"someone@Gmail.Com":       "gmail",
		" someone@gmail.com ":     "gmail",
		"someone@example.com":     Other,
		"someone@mail.gmail.com":  Other, // the subdomain is its own domain
		"someone@gmail.com.au":    Other,
		"gmail.com":               Other, // no local part, no domain
		"":                        Other,
		"@gmail.com":              "gmail",
		"a@gmail.com@outlook.com": Other, // upstream reads index 1: "gmail.com@outlook.com"
	}
	for email, want := range cases {
		if got := Mailbox(email); got != want {
			t.Errorf("Mailbox(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestName(t *testing.T) {
	if Name("gmail") != "Gmail" || Name(Other) != "Other" {
		t.Fatalf("labels = %q / %q", Name("gmail"), Name(Other))
	}
	// An unexpected stored value is shown as it is rather than folded into Other.
	if Name("fastmail") != "fastmail" {
		t.Fatalf("Name(fastmail) = %q", Name("fastmail"))
	}
}

func TestKnownCoversEveryDomain(t *testing.T) {
	seen := map[string]bool{}
	for _, provider := range Known() {
		seen[provider] = true
	}
	for email, want := range map[string]string{"a@pm.me": "proton", "a@msn.com": "outlook"} {
		if provider := Mailbox(email); !seen[provider] {
			t.Errorf("%s → %s is not in Known()", email, provider)
		} else if provider != want {
			t.Errorf("Mailbox(%q) = %q, want %q", email, provider, want)
		}
	}
}
