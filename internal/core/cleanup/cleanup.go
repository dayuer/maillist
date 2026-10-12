// Package cleanup is the list cleanup: contacts who ignored several issues in a row and showed no
// sign of life for months get one last-chance email instead of the issue. Whoever hasn't answered by a
// send LAST_CHANCE_DAYS later becomes dormant: never unsubscribed, never deleted.
//
// It is upstream lib/cleanup.js, and it is all decisions: the walking, the writing and the two
// counting queries live in the store and the sender, and read their rules from here.
package cleanup

import (
	"time"

	"mailcheap/internal/core/model"
)

// LastChanceDays is how long a warned contact has to answer before a later send makes them dormant.
const LastChanceDays = 14

// The defaults: six ignored issues and six months without a sign of life. A weekly newsletter can go
// lower on days, which is what the per-newsletter setting is for.
const (
	DefaultIssues = 6
	DefaultDays   = 180
)

// Limits are the bounds the settings are kept in, so a typo cannot stop a list from being written to.
var Limits = struct{ MinIssues, MaxIssues, MinDays, MaxDays int }{
	MinIssues: 2, MaxIssues: 100, MinDays: 30, MaxDays: 3650,
}

// Settings is one newsletter's cleanup configuration.
type Settings struct {
	Enabled bool
	Issues  int
	Days    int
}

// SettingsOf is upstream getCleanupSettings: the newsletter's own numbers when they are usable, the
// defaults otherwise. A newsletter with no cleanup row at all is a newsletter with it switched off.
func SettingsOf(n model.Newsletter) Settings {
	settings := Settings{Enabled: n.CleanupEnabled, Issues: DefaultIssues, Days: DefaultDays}
	if n.CleanupIssues > 0 {
		settings.Issues = n.CleanupIssues
	}
	if n.CleanupDays > 0 {
		settings.Days = n.CleanupDays
	}
	return settings
}

// Action is what a send does with one contact.
type Action string

const (
	// Issue: they get this issue.
	Issue Action = "issue"
	// LastChance: cold, so they get the last-chance email instead.
	LastChance Action = "lastChance"
	// Waiting: they got that email and haven't answered yet.
	Waiting Action = "waiting"
	// Dormant: they didn't answer by a send LastChanceDays or more after it.
	Dormant Action = "dormant"
	// Received: this campaign already reached them (an earlier step, or an attempt that stopped
	// halfway). They are not judged again.
	Received Action = "received"
)

// LastSignOfLife is upstream getLastSignOfLife: an open, a click, the "keep sending it" click, the
// confirmation click, or signing up — whichever is most recent. Someone who never engaged at all is
// dated by the day they joined, which is the only thing Mailcheap knows about them.
func LastSignOfLife(c model.Contact) time.Time {
	times := []time.Time{c.CreatedAt}
	if c.SubscribedAt != nil {
		times = append(times, *c.SubscribedAt)
	}
	for _, at := range []*time.Time{c.LastEngagedAt, c.LastOpenAt, c.LastClickAt, c.ConfirmedAt} {
		if at != nil {
			times = append(times, *at)
		}
	}
	latest := time.Time{}
	for _, at := range times {
		if at.After(latest) {
			latest = at
		}
	}
	return latest
}

// ActionOf is upstream getCleanupAction.
//
// Note what IgnoredIssues counts: issues *sent* since the last sign of life (the sender increments it
// on every campaign message), not issues the contact was seen to ignore. A sign of life resets it, so
// the pair "six issues in a row" and "nothing for six months" is what makes someone cold.
func ActionOf(c model.Contact, settings Settings, now time.Time) Action {
	if !settings.Enabled {
		return Issue
	}
	lastSignOfLife := LastSignOfLife(c)
	if c.LastChanceAt != nil && !c.LastChanceAt.Before(lastSignOfLife) {
		if now.Sub(*c.LastChanceAt) >= LastChanceDays*24*time.Hour {
			return Dormant
		}
		return Waiting
	}
	cold := c.IgnoredIssues >= settings.Issues && now.Sub(lastSignOfLife) >= time.Duration(settings.Days)*24*time.Hour
	if cold {
		return LastChance
	}
	return Issue
}

// SplitInput is what a walk needs besides the contacts themselves.
type SplitInput struct {
	Settings Settings
	// Limit caps the number of issue recipients; 0 means everyone. It counts contacts who already got
	// this campaign too, and the walk stops there — cold contacts past the limit are left for a later
	// step rather than sent to out of turn.
	Limit int
	// Previous is the kind of message this campaign already sent each contact, by contact id.
	Previous map[string]model.MessageKind
	Now      time.Time
}

// Split is one stretch of the audience, sorted into what happens to whom.
type Split struct {
	Issue      []model.Contact
	LastChance []model.Contact
	Waiting    []model.Contact
	Dormant    []model.Contact
	// Received is how many already had this campaign. They count toward the limit and need no message.
	Received int
}

// classify is upstream classify: what this send does with one contact.
func classify(c model.Contact, in SplitInput) (Action, bool) {
	if kind, ok := in.Previous[c.ID]; ok {
		if kind == model.KindCampaign {
			return Received, true
		}
		// They already got the last-chance email (or this is a resume): not judged again.
		return "", false
	}
	return ActionOf(c, in.Settings, in.Now), true
}

// SplitAudience is upstream splitAudience: the send-time split, over contacts already ordered most
// engaged first.
func SplitAudience(contacts []model.Contact, in SplitInput) Split {
	split := Split{}
	reach := in.Limit
	if reach <= 0 {
		reach = len(contacts) + 1 // no cap: the walk never breaks on its own
	}
	for _, contact := range contacts {
		if len(split.Issue)+split.Received >= reach {
			break
		}
		action, ok := classify(contact, in)
		if !ok {
			continue
		}
		switch action {
		case Received:
			split.Received++
		case Issue:
			split.Issue = append(split.Issue, contact)
		case LastChance:
			split.LastChance = append(split.LastChance, contact)
		case Waiting:
			split.Waiting = append(split.Waiting, contact)
		case Dormant:
			split.Dormant = append(split.Dormant, contact)
		}
	}
	return split
}

// Preview is what the cleanup would do at a send, accumulated over the whole list: each skipped
// contact is stored as the number of issue recipients before it, so a send capped at any limit can be
// counted without walking the list again.
type Preview struct {
	Issue      int
	LastChance []int
	Waiting    []int
	Dormant    []int
}

// EmptyPreview is upstream emptyPreview.
func EmptyPreview() Preview { return Preview{} }

// Add is upstream addToPreview: one chunk of the audience, folded in.
func (p *Preview) Add(contacts []model.Contact, in SplitInput) {
	for _, contact := range contacts {
		action, ok := classify(contact, in)
		if !ok {
			continue
		}
		switch action {
		case Issue, Received:
			p.Issue++
		case LastChance:
			p.LastChance = append(p.LastChance, p.Issue)
		case Waiting:
			p.Waiting = append(p.Waiting, p.Issue)
		case Dormant:
			p.Dormant = append(p.Dormant, p.Issue)
		}
	}
}

// Counts is what a preview summarizes to, at one limit.
type Counts struct {
	Issue      int `json:"issue"`
	LastChance int `json:"lastChance"`
	Waiting    int `json:"waiting"`
	Dormant    int `json:"dormant"`
}

// Summarize is upstream summarizePreview: the same numbers SplitAudience would produce at this limit.
func (p Preview) Summarize(limit int) Counts {
	within := func(ranks []int) int {
		if limit <= 0 {
			return len(ranks)
		}
		count := 0
		for _, rank := range ranks {
			if rank < limit {
				count++
			}
		}
		return count
	}
	issue := p.Issue
	if limit > 0 && issue > limit {
		issue = limit
	}
	return Counts{
		Issue: issue, LastChance: within(p.LastChance),
		Waiting: within(p.Waiting), Dormant: within(p.Dormant),
	}
}

// Totals is the settings screen's summary, over the contacts who ever got a last-chance email.
type Totals struct {
	Waiting int `json:"waiting"`
	Kept    int `json:"kept"`
	Dormant int `json:"dormant"`
}
