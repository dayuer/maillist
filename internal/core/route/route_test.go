package route

import (
	"slices"
	"strings"
	"testing"
)

// The three forms a match is stored in, and what each one reads back as. The design's examples are
// the CLI's: --match domain:example.org,example.net and --match mailbox:gmail.
func TestParseMatch(t *testing.T) {
	cases := map[string]Match{
		"default":                        {Default: true},
		"  default  ":                    {Default: true},
		"domain:example.org":             {Domains: []string{"example.org"}},
		"domain:EXAMPLE.org":             {Domains: []string{"example.org"}},
		"domain: example.org , a.co":     {Domains: []string{"example.org", "a.co"}},
		"domain:example.org,example.org": {Domains: []string{"example.org"}},
		"mailbox:gmail":                  {Mailboxes: []string{"gmail"}},
		"mailbox:Gmail, other":           {Mailboxes: []string{"gmail", "other"}},
	}
	for text, want := range cases {
		got, err := ParseMatch(text)
		if err != nil {
			t.Errorf("ParseMatch(%q): %v", text, err)
			continue
		}
		if !equalMatch(got, want) {
			t.Errorf("ParseMatch(%q) = %+v, want %+v", text, got, want)
		}
		// What String gives back has to read back as the same match: it is what goes in the column
		// and what route list prints.
		back, err := ParseMatch(got.String())
		if err != nil || !equalMatch(back, got) {
			t.Errorf("ParseMatch(%q).String() = %q, which reads back as %+v (%v)", text, got.String(), back, err)
		}
	}
}

// String is the canonical stored form, so one condition has one spelling in the database.
func TestMatchString(t *testing.T) {
	for text, want := range map[string]string{
		"domain:EXAMPLE.org": "domain:example.org",
		"domain:a.co,a.co":   "domain:a.co",
		"mailbox:GMAIL":      "mailbox:gmail",
		"mailbox:proton":     "mailbox:proton",
		"domain:a.co":        "domain:a.co",
	} {
		match, err := ParseMatch(text)
		if err != nil {
			t.Fatalf("ParseMatch(%q): %v", text, err)
		}
		if got := match.String(); got != want {
			t.Errorf("ParseMatch(%q).String() = %q, want %q", text, got, want)
		}
	}
	if got := (Match{}).String(); got != "" {
		t.Errorf("an empty match prints as %q, want the empty string", got)
	}
}

// Everything that would silently match nothing, or match something the operator did not mean, is
// refused where it is typed.
func TestParseMatchRefusesWhatItCannotRead(t *testing.T) {
	for text, want := range map[string]string{
		"":                     "write default, domain:a.b or mailbox:gmail",
		"example.org":          "write default, domain:a.b or mailbox:gmail",
		"domain:":              "the list is empty",
		"mailbox: ,":           "the list is empty",
		"domain:exam ple.org":  "is not a domain name",
		"domain:*.example.org": "is not a domain name",
		"domain:a@b":           "is not a domain name",
		"domain:a_b.com":       "is not a domain name",
		"mailbox:fastmail":     "use gmail, outlook, yahoo, icloud, proton, other",
		"kind:x":               `"kind" is not domain or mailbox`,
	} {
		_, err := ParseMatch(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseMatch(%q) = %v, want it to say %q", text, err, want)
		}
	}
}

// --split a:80,b:20: a channel and its share, one at a time.
func TestParseSplit(t *testing.T) {
	got, err := ParseSplit("aws2:80, resend:20")
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{Channel: "aws2", Weight: 80}, {Channel: "resend", Weight: 20}}
	if !slices.Equal(got, want) {
		t.Errorf("ParseSplit = %+v, want %+v", got, want)
	}
	// A single target is how --to is stored.
	one, err := ParseSplit("aws2:1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(one, []Target{{Channel: "aws2", Weight: 1}}) {
		t.Errorf("ParseSplit(aws2:1) = %+v", one)
	}
}

func TestParseSplitRefusesWhatItCannotRead(t *testing.T) {
	for text, want := range map[string]string{
		"":                "write channel:weight, as in aws2:80,resend:20",
		"aws2":            "write channel:weight, as in aws2:80,resend:20",
		"aws2:0":          "from 1 to 1000000",
		"aws2:-1":         "from 1 to 1000000",
		"aws2:x":          "from 1 to 1000000",
		"aws2:1000001":    "from 1 to 1000000",
		":80":             "has no channel name",
		"aws2:80,aws2:20": "appears twice",
	} {
		_, err := ParseSplit(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseSplit(%q) = %v, want it to say %q", text, err, want)
		}
	}
}

// equalMatch compares two matches field by field; a missing list and an empty one are the same.
func equalMatch(a, b Match) bool {
	return a.Default == b.Default && slices.Equal(a.Domains, b.Domains) && slices.Equal(a.Mailboxes, b.Mailboxes)
}
