package render

import (
	"net/url"
	"strings"
	"testing"
)

// tests/confirmation.test.mjs "the check-your-inbox button opens the subscriber's webmail".
func TestTheCheckYourInboxButtonOpensTheSubscribersWebmail(t *testing.T) {
	want := map[string]string{
		"googlemail.com": "Gmail",
		"hotmail.co.uk":  "Outlook",
		"proton.me":      "Proton Mail",
	}
	for domain, name := range want {
		gotName, gotURL := Inbox("reader@"+domain, "hi@news.example.org")
		if gotName != name {
			t.Errorf("%s: name = %q, want %q", domain, gotName, name)
		}
		if gotURL == "" {
			t.Errorf("%s: no url", domain)
		}
	}

	_, gmail := Inbox("reader@googlemail.com", "hi@news.example.org")
	if !strings.HasPrefix(gmail, "https://mail.google.com/mail/u/0/#search/") {
		t.Errorf("gmail url = %q", gmail)
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(gmail, "https://mail.google.com/mail/u/0/#search/"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded, "from:hi@news.example.org in:anywhere") {
		t.Errorf("search = %q", decoded)
	}
	if strings.Contains(gmail, "+") {
		t.Errorf("encodeURIComponent writes a space as %%20, not +: %q", gmail)
	}

	if name, link := Inbox("founder@acme.example", ""); name != "" || link != "" {
		t.Errorf("unknown provider = %q %q", name, link)
	}
}
