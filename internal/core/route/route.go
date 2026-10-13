// Package route decides which channel a message goes out through. Rules are tried in order and the
// first one that matches wins; a rule with several targets splits traffic between them by weight.
// Everything here is a function of its arguments — no database, no clock — so the same contact
// lands in the same share of a split every time.
package route

import (
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"nibwire/internal/core/model"
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

// Rule is one routing rule. ID is what a split hashes together with the contact, so it has to stay
// put for as long as the rule exists: another rule appearing above this one must not move anybody.
type Rule struct {
	ID      string
	Match   Match
	Targets []Target
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

// Validate reports whether rules can be used as they stand: each one matches something, sends
// somewhere and can be hashed, and the single default rule — when there is one — is last.
func Validate(rules []Rule) error {
	defaults := 0
	lastDefault := -1
	for i, rule := range rules {
		if rule.ID == "" {
			return fmt.Errorf("rule %d has no id", i+1)
		}
		if len(rule.Match.Domains) == 0 && len(rule.Match.Mailboxes) == 0 && !rule.Match.Default {
			return fmt.Errorf("rule %s matches nothing", rule.ID)
		}
		if len(rule.Targets) == 0 {
			return fmt.Errorf("rule %s has no targets", rule.ID)
		}
		for _, target := range rule.Targets {
			if target.Channel == "" || target.Weight < 1 || target.Weight > maxWeight {
				return fmt.Errorf("rule %s: %q needs a channel and a weight from 1 to %d",
					rule.ID, target.Channel, maxWeight)
			}
		}
		if rule.Match.Default {
			defaults++
			lastDefault = i
		}
	}
	if defaults > 1 {
		return fmt.Errorf("%d default rules: there can be only one default rule", defaults)
	}
	if lastDefault >= 0 && lastDefault != len(rules)-1 {
		return fmt.Errorf("rule %s: the default rule is always last", rules[lastDefault].ID)
	}
	return nil
}

// Pick returns the channel a message goes out through: the first rule that matches, or the default
// channel when none does — which is where a deployment with no rules sends everything.
//
// contactID may be empty for one-off mail, a test message or a confirmation: there the address
// stands in for it, so the same recipient lands in the same share of a split every time.
func Pick(rules []Rule, contactID, email string) string {
	for _, rule := range rules {
		if !rule.Match.matches(email) {
			continue
		}
		return rule.pick(splitKey(contactID, email))
	}
	return model.DefaultChannel
}

// pick sends a one-target rule straight to its channel; several targets are split by weight, so
// which share a key lands in is stable for as long as the rule's targets are.
func (r Rule) pick(key string) string {
	total := 0
	for _, target := range r.Targets {
		if target.Weight > 0 {
			total += target.Weight
		}
	}
	if total == 0 {
		// No targets, or none of them with a weight: nothing to split between.
		return model.DefaultChannel
	}
	// The remainder is smaller than the weights it was taken from, so the whole walk stays in
	// uint64 and never has to be narrowed back to an int.
	remaining := weighted(key, r.ID) % uint64(total)
	channel := model.DefaultChannel
	for _, target := range r.Targets {
		if target.Weight <= 0 {
			continue
		}
		channel = target.Channel
		if remaining < uint64(target.Weight) {
			break
		}
		remaining -= uint64(target.Weight)
	}
	return channel
}

// matches reports whether a message to this address is the rule's business.
func (m Match) matches(email string) bool {
	switch {
	case m.Default:
		return true
	case len(m.Domains) > 0:
		return slices.Contains(m.Domains, domainOf(email))
	default:
		return slices.Contains(m.Mailboxes, providers.Mailbox(email))
	}
}

// domainOf is the part of an address after its first "@", lower-cased — the same reading of an
// address that providers.Mailbox uses, so that "a@b@c" means the same thing to both.
func domainOf(email string) string {
	address := strings.TrimSpace(email)
	at := strings.Index(address, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(address[at+1:]))
}

// splitKey is what a split hashes: the contact, or the address when there is no contact. Folding the
// address to lower case keeps one mailbox on one side of the split.
func splitKey(contactID, email string) string {
	if contactID != "" {
		return contactID
	}
	return strings.ToLower(strings.TrimSpace(email))
}

// weighted is fnv64a of "contact/rule": stable across runs, and independent for each rule because
// the rule's id is part of what is hashed (design §4.1).
func weighted(key, ruleID string) uint64 {
	sum := fnv.New64a()
	_, _ = sum.Write([]byte(key + "/" + ruleID))
	return sum.Sum64()
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
