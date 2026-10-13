package route

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"nibwire/internal/core/model"
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

// Rules are tried in order and the first match wins; everything none of them claims goes through
// the default channel, which is what a deployment without any rules does.
func TestPickTakesTheFirstMatchingRule(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Match: Match{Domains: []string{"example.org"}}, Targets: single("aws2")},
		{ID: "r2", Match: Match{Domains: []string{"example.org"}}, Targets: single("back")},
		{ID: "r3", Match: Match{Default: true}, Targets: single(model.DefaultChannel)},
	}
	for email, want := range map[string]string{
		"someone@example.org": "aws2", // the first of the two, not the second
		"someone@other.org":   model.DefaultChannel,
	} {
		if got := Pick(rules, "64f1a2b3c4d5e6f7a8b9c0a1", email); got != want {
			t.Errorf("Pick(%s) = %s, want %s", email, got, want)
		}
	}
	if got := Pick(nil, "64f1a2b3c4d5e6f7a8b9c0a1", "someone@example.org"); got != model.DefaultChannel {
		t.Errorf("Pick with no rules = %s, want %s", got, model.DefaultChannel)
	}
	// A rule that was never filled in matches nothing at all.
	if got := Pick([]Rule{{ID: "empty", Targets: single("aws2")}}, "", "someone@example.org"); got != model.DefaultChannel {
		t.Errorf("Pick with an empty match = %s, want %s", got, model.DefaultChannel)
	}
}

// The part after the first "@", folded to lower case and matched exactly: mail.example.org is its
// own domain, not a subdomain of example.org.
func TestDomainMatchIsExactAndCaseInsensitive(t *testing.T) {
	rules := []Rule{{ID: "r1", Match: Match{Domains: []string{"example.org", "a.co"}}, Targets: single("aws2")}}
	for email, want := range map[string]string{
		"SOMEONE@EXAMPLE.ORG":      "aws2",
		" someone@example.org ":    "aws2",
		"someone@a.co":             "aws2",
		"someone@mail.example.org": model.DefaultChannel,
		"someone@notexample.org":   model.DefaultChannel,
		"someone@example.org.uk":   model.DefaultChannel,
		"someone@example.com":      model.DefaultChannel,
		"gmail.com":                model.DefaultChannel,
	} {
		if got := Pick(rules, "", email); got != want {
			t.Errorf("Pick(%q) = %s, want %s", email, got, want)
		}
	}
}

// mailbox: uses core/providers, the same table campaign report groups by, so the aliases
// (googlemail.com, hotmail.co.uk, …) mean what they mean everywhere else.
func TestMailboxMatchUsesTheProviderTable(t *testing.T) {
	rules := []Rule{
		{ID: "gmail", Match: Match{Mailboxes: []string{"gmail"}}, Targets: single("aws2")},
		{ID: "rest", Match: Match{Mailboxes: []string{"other"}}, Targets: single("back")},
	}
	for email, want := range map[string]string{
		"someone@gmail.com":      "aws2",
		"SOMEONE@GOOGLEMAIL.COM": "aws2",
		"someone@fastmail.com":   "back",
		"someone@example.org":    "back",
		"not-an-address":         "back", // providers.Mailbox has no provider for it
	} {
		if got := Pick(rules, "", email); got != want {
			t.Errorf("Pick(%q) = %s, want %s", email, got, want)
		}
	}
}

// A split is proportional to the weights (design §8: under 1% off on 100,000 samples) and stable:
// the same contact gets the same channel every time, whether or not there is a contact at all.
func TestSplitIsStableAndProportional(t *testing.T) {
	rules := []Rule{{ID: "r1", Match: Match{Default: true},
		Targets: []Target{{Channel: "aws2", Weight: 80}, {Channel: "back", Weight: 20}}}}

	const samples = 100_000
	shares := map[string]int{}
	for i := range samples {
		shares[Pick(rules, "contact-"+strconv.Itoa(i), "someone@example.com")]++
	}
	if shares["aws2"]+shares["back"] != samples {
		t.Fatalf("shares = %v, want every sample on one of the two channels", shares)
	}
	if share := float64(shares["aws2"]) / samples; math.Abs(share-0.8) > 0.01 {
		t.Errorf("aws2 took %.4f of %d samples, want 0.8 ± 0.01", share, samples)
	}

	// A contact's channel does not depend on the address it is sent to.
	first := Pick(rules, "contact-7", "someone@example.com")
	for i := 0; i < 100; i++ {
		if got := Pick(rules, "contact-7", "another@elsewhere.net"); got != first {
			t.Fatalf("contact-7 went to %s then %s", first, got)
		}
	}

	// With no contact — a test message, a confirmation — the address stands in for it, folded to
	// lower case so that one mailbox is one key.
	fromAddress := Pick(rules, "", "reader@example.com")
	for i := 0; i < 100; i++ {
		if got := Pick(rules, "", "reader@example.com"); got != fromAddress {
			t.Fatalf("reader@example.com went to %s then %s", fromAddress, got)
		}
	}
	if got := Pick(rules, "", "READER@example.com"); got != fromAddress {
		t.Errorf("READER@example.com = %s, want the same as reader@example.com (%s)", got, fromAddress)
	}
}

// A split is hashed from the rule's own id, so adding or removing another rule does not move a
// contact between the channels of this one.
func TestSplitIgnoresOtherRules(t *testing.T) {
	split := Rule{ID: "split", Match: Match{Default: true},
		Targets: []Target{{Channel: "aws2", Weight: 50}, {Channel: "back", Weight: 50}}}
	// A rule that the addresses below never match, so only the order of the list changes.
	above := Rule{ID: "above", Match: Match{Domains: []string{"example.net"}}, Targets: single("aws2")}
	for i := 0; i < 1000; i++ {
		contact := "contact-" + strconv.Itoa(i)
		alone := Pick([]Rule{split}, contact, "someone@example.com")
		withOneAbove := Pick([]Rule{above, split}, contact, "someone@example.com")
		if alone != withOneAbove {
			t.Fatalf("%s went to %s alone and %s with another rule above", contact, alone, withOneAbove)
		}
	}
}

// PickRule is Pick with the rule that decided it, which is what `route test` counts by; and a rule's
// own Channel is that answer once the rule has matched.
func TestPickRuleNamesTheRuleThatDecidedIt(t *testing.T) {
	first := Rule{ID: "r1", Match: Match{Domains: []string{"example.org"}}, Targets: single("aws2")}
	second := Rule{ID: "r2", Match: Match{Default: true}, Targets: single("back")}
	rules := []Rule{first, second}

	if rule, ok := PickRule(rules, "c1", "someone@example.org"); !ok || rule.ID != "r1" {
		t.Fatalf("PickRule = %+v %v, want r1", rule, ok)
	}
	if rule, ok := PickRule(rules, "c1", "someone@elsewhere.net"); !ok || rule.ID != "r2" {
		t.Fatalf("PickRule = %+v %v, want r2", rule, ok)
	}
	if _, ok := PickRule(nil, "c1", "someone@example.org"); ok {
		t.Fatal("PickRule found a rule where there are none")
	}
	if got := first.Channel("c1", "someone@example.org"); got != "aws2" {
		t.Fatalf("Channel = %q, want aws2", got)
	}
}

// Counts is where an audience goes, which is the number `route test` and the pre-send check both
// show: one pass, the same decision per contact as Pick.
func TestCountsAddsUpTheAudience(t *testing.T) {
	rules := []Rule{
		{ID: "org", Match: Match{Domains: []string{"example.org"}}, Targets: single("aws2")},
		{ID: "rest", Match: Match{Default: true}, Targets: single("back")},
	}
	contacts := []model.Contact{
		{ID: "c1", Email: "a@example.org"},
		{ID: "c2", Email: "b@example.org"},
		{ID: "c3", Email: "c@example.com"},
	}
	counts := Counts(rules, contacts)
	if counts["aws2"] != 2 || counts["back"] != 1 || len(counts) != 2 {
		t.Fatalf("Counts = %v, want aws2 2 and back 1", counts)
	}
}

// Rules have to be usable as they stand: something to match, somewhere to go, an id to hash with,
// and the one default rule — when there is one — last.
func TestValidateKeepsTheDefaultRuleLast(t *testing.T) {
	every := Rule{ID: "every", Match: Match{Default: true}, Targets: single(model.DefaultChannel)}
	domain := Rule{ID: "domain", Match: Match{Domains: []string{"example.org"}}, Targets: single("aws2")}
	for _, rules := range [][]Rule{{}, {domain}, {every}, {domain, every}} {
		if err := Validate(rules); err != nil {
			t.Errorf("Validate(%+v) = %v, want no error", rules, err)
		}
	}
	for want, rules := range map[string][]Rule{
		"the default rule is always last":    {every, domain},
		"there can be only one default rule": {every, every},
		"matches nothing":                    {{ID: "empty", Targets: single("aws2")}},
		"has no targets":                     {{ID: "bare", Match: domain.Match}},
		"needs a channel and a weight":       {{ID: "zero", Match: domain.Match, Targets: []Target{{Channel: "aws2"}}}},
		"has no id":                          {{Match: domain.Match, Targets: single("aws2")}},
	} {
		err := Validate(rules)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate(%+v) = %v, want it to say %q", rules, err, want)
		}
	}
}

// single is a one-target rule's targets: the stored weight is 1, the same thing --to stores.
func single(channel string) []Target { return []Target{{Channel: channel, Weight: 1}} }

// equalMatch compares two matches field by field; a missing list and an empty one are the same.
func equalMatch(a, b Match) bool {
	return a.Default == b.Default && slices.Equal(a.Domains, b.Domains) && slices.Equal(a.Mailboxes, b.Mailboxes)
}
