// Package route decides which channel a message goes out through. Rules are tried in order and the
// first one that matches wins; a rule with several targets splits traffic between them by weight.
// Everything here is a function of its arguments — no database, no clock — so the same contact
// lands in the same share of a split every time.
package route

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"nibwire/internal/core/providers"
)

// maxWeight bounds a target's weight, so that adding up a rule's weights cannot overflow.
const maxWeight = 1_000_000

// domainPattern is what a domain in a rule may look like. It is deliberately not DNS: the point is
// to catch a wildcard or a stray space where the operator typed a domain, not to decide whether a
// domain exists.
var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// Target is one channel a rule can send through, with its share of the split. A rule with a single
// target carries weight 1: there, the weight is not a share of anything.
//
// The field names are the stored form (design 2026-10-12-multi-channel-sending §7), which is the
// one place that decides them.
type Target struct {
	Channel string `json:"channel"`
	Weight  int    `json:"weight"`
}

// Match is what a rule matches on: everything (the default rule), a list of domains, or a list of
// mailbox providers. Exactly one of the three is set.
type Match struct {
	Default   bool
	Domains   []string
	Mailboxes []string
}

// ParseMatch reads the stored form of a match: "default", "domain:example.org,example.net" or
// "mailbox:gmail,outlook". Domains and mailbox names are folded to lower case, and a name that
// appears twice is kept once, so that one condition has one spelling in the database.
func ParseMatch(text string) (Match, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "default" {
		return Match{Default: true}, nil
	}
	kind, rest, found := strings.Cut(trimmed, ":")
	if !found {
		return Match{}, fmt.Errorf("match %q: write default, domain:a.b or mailbox:gmail", text)
	}
	items, err := splitList(rest)
	if err != nil {
		return Match{}, fmt.Errorf("match %q: %w", text, err)
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "domain":
		for _, domain := range items {
			if !domainPattern.MatchString(domain) {
				return Match{}, fmt.Errorf("match %q: %q is not a domain name", text, domain)
			}
		}
		return Match{Domains: items}, nil
	case "mailbox":
		for _, mailbox := range items {
			if !slices.Contains(providers.Known(), mailbox) {
				return Match{}, fmt.Errorf("match %q: mailbox %q: use %s",
					text, mailbox, strings.Join(providers.Known(), ", "))
			}
		}
		return Match{Mailboxes: items}, nil
	default:
		return Match{}, fmt.Errorf("match %q: %q is not domain or mailbox", text, kind)
	}
}

// String is the stored form, what route list prints and what ParseMatch reads back. A match that
// was never filled in prints as nothing at all rather than as an empty list.
func (m Match) String() string {
	switch {
	case m.Default:
		return "default"
	case len(m.Domains) > 0:
		return "domain:" + strings.Join(m.Domains, ",")
	case len(m.Mailboxes) > 0:
		return "mailbox:" + strings.Join(m.Mailboxes, ",")
	default:
		return ""
	}
}

// ParseSplit reads "aws2:80,resend:20": every target is a channel and a positive weight. The weights
// are read as proportions, so they need not add up to anything in particular.
func ParseSplit(text string) ([]Target, error) {
	var targets []Target
	for _, item := range strings.Split(text, ",") {
		name, weightText, found := strings.Cut(strings.TrimSpace(item), ":")
		if !found {
			return nil, fmt.Errorf("split %q: write channel:weight, as in aws2:80,resend:20", text)
		}
		name = strings.TrimSpace(name)
		weight, err := strconv.Atoi(strings.TrimSpace(weightText))
		if err != nil || weight < 1 || weight > maxWeight {
			return nil, fmt.Errorf("split %q: the weight for %q must be a whole number from 1 to %d",
				text, name, maxWeight)
		}
		if name == "" {
			return nil, fmt.Errorf("split %q: a target has no channel name", text)
		}
		if slices.ContainsFunc(targets, func(t Target) bool { return t.Channel == name }) {
			return nil, fmt.Errorf("split %q: %s appears twice", text, name)
		}
		targets = append(targets, Target{Channel: name, Weight: weight})
	}
	return targets, nil
}

// splitList reads a comma-separated list: trimmed, lower-cased, without repeats, never empty.
func splitList(value string) ([]string, error) {
	var out []string
	for _, item := range strings.Split(value, ",") {
		folded := strings.ToLower(strings.TrimSpace(item))
		if folded == "" || slices.Contains(out, folded) {
			continue
		}
		out = append(out, folded)
	}
	if len(out) == 0 {
		return nil, errors.New("the list is empty")
	}
	return out, nil
}
