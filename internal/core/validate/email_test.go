package validate

import (
	"strings"
	"testing"
)

func TestEmailsAreTrimmedAndLowercased(t *testing.T) {
	for in, want := range map[string]string{
		" Reader@Example.COM ":                   "reader@example.com",
		"first.last+news@sub.example.co.uk":      "first.last+news@sub.example.co.uk",
		"o'brien@example.com":                    "o'brien@example.com",
		"user@xn--e1afmkfd.xn--80akhbyknj4f":     "user@xn--e1afmkfd.xn--80akhbyknj4f",
		strings.Repeat("a", 64) + "@example.com": strings.Repeat("a", 64) + "@example.com",
	} {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddressesSESCantTakeAreRejected(t *testing.T) {
	for _, in := range []string{
		"", "plain", "@example.com", "user@", "user@example", "user@@example.com", "a,b@example.com",
		"a b@example.com", "<a@example.com>", "Name <a@example.com>", `"quoted"@example.com`,
		".start@example.com", "end.@example.com", "dou..ble@example.com", "user@-example.com",
		"user@example-.com", "user@example.c", "user@example.123", "user@exa_mple.com",
		"user\r\n@example.com", strings.Repeat("a", 65) + "@example.com",
		"a@" + strings.Repeat(strings.Repeat("b", 60)+".", 5) + "com",
	} {
		if got := NormalizeEmail(in); got != "" {
			t.Errorf("NormalizeEmail(%q) = %q, want rejection", in, got)
		}
	}
}

func TestNormalizeAddressReadsDisplayNames(t *testing.T) {
	if got := NormalizeAddress("Dana <Dana@Example.com>"); got != "dana@example.com" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeAddress("dana@example.com"); got != "dana@example.com" {
		t.Fatalf("got %q", got)
	}
}
