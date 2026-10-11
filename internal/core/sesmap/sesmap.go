// Package sesmap turns one SES event into the updates that follow from it. It is pure: the caller
// applies the plan.
package sesmap

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/validate"
)

// EventType is an SES event kind, as eventType or notificationType gives it.
type EventType string

const (
	EventSend             EventType = "Send"
	EventDelivery         EventType = "Delivery"
	EventBounce           EventType = "Bounce"
	EventComplaint        EventType = "Complaint"
	EventOpen             EventType = "Open"
	EventClick            EventType = "Click"
	EventReject           EventType = "Reject"
	EventDeliveryDelay    EventType = "DeliveryDelay"
	EventRenderingFailure EventType = "RenderingFailure"
)

// detailKeys maps an event type to the top-level key holding its detail object.
var detailKeys = map[string]string{
	"Send": "send", "Delivery": "delivery", "Bounce": "bounce", "Complaint": "complaint",
	"Open": "open", "Click": "click", "Reject": "reject", "DeliveryDelay": "deliveryDelay",
	"RenderingFailure": "failure",
}

// SES never un-delivers: later events only move a message to a worse status. Upstream's "queued" is
// the Go model's pending/claimed pair (design D14).
var (
	beforeDelivery = []model.MessageStatus{
		model.MessagePending, model.MessageClaimed, model.MessageSent, model.MessageFailed,
	}
	beforeBounce    = append(append([]model.MessageStatus{}, beforeDelivery...), model.MessageDelivered)
	beforeComplaint = append(append([]model.MessageStatus{}, beforeBounce...),
		model.MessageBounced)
	beforeFailure = []model.MessageStatus{model.MessagePending, model.MessageClaimed, model.MessageSent}
)

// FirstWrite sets a message timestamp field only if it is still empty; Stat and ContactStat are the
// message and contact counters to bump only when that first write happens.
type FirstWrite struct {
	Field       string
	At          time.Time
	Stat        string
	ContactStat string
}

// Engagement is the last-open and last-click timestamps an event updates.
type Engagement struct{ LastOpenAt, LastClickAt *time.Time }

// Plan is everything one SES event asks for.
type Plan struct {
	Type         EventType
	SESMessageID string // mail.messageId (SES's own id); "" when absent
	MessageID    string // from the "message_id" tag; "" when not ours
	Email        string // normalized mail.destination[0]; "" when absent
	OccurredAt   time.Time
	// Kind comes from the Mailcheap-written "kind" tag; "" when the tag is absent or unknown.
	// Upstream instead loads it from the stored message.
	Kind   model.MessageKind
	Firsts []FirstWrite
	// NewStatus is "" when unchanged; StatusFrom lists the statuses it may move forward from. The
	// message stat to bump is NewStatus itself, as it is upstream.
	NewStatus         model.MessageStatus
	StatusFrom        []model.MessageStatus
	Error             string
	Suppress          []string
	SuppressAs        model.SuppressionReason
	SuppressDetail    string
	ContactStatus     model.ContactStatus // "" = unchanged
	ContactStatusFrom []model.ContactStatus
	Engagement        *Engagement
	Store             json.RawMessage // event data to keep, without raw mail headers
}

// Type reads the event type of a raw SES event, or "" when it isn't one we map.
func Type(raw json.RawMessage) EventType {
	top, ok := objectOf(raw)
	if !ok {
		return ""
	}
	return eventTypeOf(top)
}

// eventTypeOf prefers eventType (configuration-set publishing) and falls back to notificationType
// (identity notifications), as upstream's ?? does.
func eventTypeOf(top map[string]json.RawMessage) EventType {
	for _, key := range []string{"eventType", "notificationType"} {
		raw, present := top[key]
		if !present || string(raw) == "null" {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return "" // present but not a string: no fallback, and no valid type
		}
		// Upstream's replace(" ", "") drops the first space only.
		candidate := EventType(strings.Replace(value, " ", "", 1))
		if _, known := detailKeys[string(candidate)]; !known {
			return ""
		}
		return candidate
	}
	return ""
}

// TrackingPlan is the plan for an open or click on Mailcheap's own pixel and links.
func TrackingPlan(t EventType, at time.Time) Plan {
	p := Plan{Type: t, OccurredAt: at}
	applyEngagement(&p, t, at)
	return p
}

// EventPlan maps one raw SES event. ok is false for an unknown or malformed event, which is ignored.
func EventPlan(raw json.RawMessage, now time.Time) (Plan, bool) {
	top, ok := objectOf(raw)
	if !ok {
		return Plan{}, false
	}
	eventType := eventTypeOf(top)
	if eventType == "" {
		return Plan{}, false
	}
	mail, _ := objectOf(top["mail"])
	detail, _ := objectOf(top[detailKeys[string(eventType)]])

	email := ""
	if destinations, ok := stringArray(mail["destination"]); ok && len(destinations) > 0 {
		email = validate.NormalizeAddress(destinations[0])
	}
	tags := mail["tags"]

	plan := Plan{
		Type:         eventType,
		SESMessageID: stringField(mail, "messageId"),
		MessageID:    readObjectIDTag(tags, "message_id"),
		Email:        email,
		OccurredAt:   firstDate(stringField(detail, "timestamp"), stringField(mail, "timestamp"), now),
		Kind:         readKindTag(tags),
		Store:        withoutMailHeaders(raw),
	}

	switch eventType {
	case EventDelivery:
		plan.Firsts = []FirstWrite{{Field: "deliveredAt", Stat: "delivered"}}
		plan.NewStatus = model.MessageDelivered
		plan.StatusFrom = beforeDelivery

	case EventBounce:
		kind := "Soft"
		if stringField(detail, "bounceType") == "Permanent" {
			kind = "Hard"
		}
		var reason []string
		if v := stringField(detail, "bounceSubType"); v != "" {
			reason = append(reason, v)
		}
		if v := firstDiagnosticCode(detail); v != "" {
			reason = append(reason, v)
		}
		plan.Error = kind + " bounce: " + orDefault(strings.Join(reason, ", "), "unknown")
		if kind == "Hard" {
			plan.Firsts = []FirstWrite{{Field: "bouncedAt", Stat: "bounced"}}
			plan.NewStatus = model.MessageBounced
			plan.StatusFrom = beforeBounce
			plan.Suppress = readRecipients(detail["bouncedRecipients"], email)
			plan.SuppressAs = model.SuppressBounce
			plan.SuppressDetail = stringField(detail, "bounceType") + "/" +
				orDefault(stringField(detail, "bounceSubType"), "General")
			plan.ContactStatus = model.ContactBounced
			plan.ContactStatusFrom = []model.ContactStatus{
				model.ContactActive, model.ContactDormant, model.ContactUnsubscribed,
			}
		}

	case EventComplaint:
		// "not-spam" is a recipient taking the email out of spam, not a complaint.
		if stringField(detail, "complaintFeedbackType") != "not-spam" {
			plan.Firsts = []FirstWrite{{Field: "complainedAt", Stat: "complained"}}
			plan.NewStatus = model.MessageComplained
			plan.StatusFrom = beforeComplaint
			plan.Suppress = readRecipients(detail["complainedRecipients"], email)
			plan.SuppressAs = model.SuppressComplaint
			plan.SuppressDetail = orDefault(stringField(detail, "complaintFeedbackType"), "abuse")
			plan.ContactStatus = model.ContactComplained
			plan.ContactStatusFrom = []model.ContactStatus{
				model.ContactActive, model.ContactDormant, model.ContactUnsubscribed, model.ContactBounced,
			}
		}

	case EventReject, EventRenderingFailure:
		plan.NewStatus = model.MessageFailed
		plan.StatusFrom = beforeFailure
		if eventType == EventReject {
			plan.Error = "Rejected by SES: " + orDefault(stringField(detail, "reason"), "unknown")
		} else {
			plan.Error = "Rendering failed: " + orDefault(stringField(detail, "errorMessage"), "unknown")
		}
	}

	// Unsubscribe clicks aren't engagement.
	if eventType == EventOpen ||
		(eventType == EventClick && !isUnsubscribeLink(stringField(detail, "link"))) {
		applyEngagement(&plan, eventType, plan.OccurredAt)
	}
	return plan, true
}

// applyEngagement records an open, and counts a click as an open too: the reader may have images
// blocked.
func applyEngagement(p *Plan, t EventType, at time.Time) {
	opened := at
	open := FirstWrite{Field: "openedAt", At: at, Stat: "opened", ContactStat: "opens"}
	if t == EventClick {
		clicked := at
		p.Firsts = []FirstWrite{
			open,
			{Field: "clickedAt", At: at, Stat: "clicked", ContactStat: "clicks"},
		}
		p.Engagement = &Engagement{LastOpenAt: &opened, LastClickAt: &clicked}
		return
	}
	p.Firsts = []FirstWrite{open}
	p.Engagement = &Engagement{LastOpenAt: &opened}
}

// isUnsubscribeLink is upstream's isUnsubscribeLink: the unsubscribe path (dot segments removed, as
// a URL parser does — see validate.UnsubscribePath) plus the token parameter, because /unsubscribe
// is also a page readers visit by hand.
//
// It has to agree with render.isUnsubscribeURL: a link the renderer leaves direct arrives here as
// /unsubscribe, and a click on it must not be counted as engagement (the phase 1-3 audit's FI-A3).
func isUnsubscribeLink(link string) bool {
	if !validate.UnsubscribePath(link) {
		return false
	}
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	return u.Query().Has("token")
}

func objectOf(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func stringField(object map[string]json.RawMessage, key string) string {
	raw, present := object[key]
	if !present {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func stringArray(raw json.RawMessage) ([]string, bool) {
	var values []string
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil {
		return nil, false
	}
	return values, true
}

// readObjectIDTag reads a tag that names one of our own records. It must be a string, or a
// single-element array of strings, and a valid 24-hex ID.
func readObjectIDTag(tags json.RawMessage, name string) string {
	value, ok := tagValue(tags, name)
	if !ok || !ids.Valid(value) {
		return ""
	}
	return strings.ToLower(value)
}

func readKindTag(tags json.RawMessage) model.MessageKind {
	value, ok := tagValue(tags, "kind")
	if !ok {
		return ""
	}
	switch kind := model.MessageKind(value); kind {
	case model.KindCampaign, model.KindLastChance, model.KindWelcome, model.KindConfirmation, model.KindTest:
		return kind
	default:
		return ""
	}
}

func tagValue(tags json.RawMessage, name string) (string, bool) {
	object, ok := objectOf(tags)
	if !ok {
		return "", false
	}
	raw, present := object[name]
	if !present {
		return "", false
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single, true
	}
	if list, ok := stringArray(raw); ok && len(list) > 0 {
		return list[0], true
	}
	return "", false
}

func firstDiagnosticCode(detail map[string]json.RawMessage) string {
	recipients, ok := objectList(detail["bouncedRecipients"])
	if !ok || len(recipients) == 0 {
		return ""
	}
	return stringField(recipients[0], "diagnosticCode")
}

// readRecipients normalizes the addresses an event names, falling back to the event's own email.
func readRecipients(raw json.RawMessage, fallback string) []string {
	var emails []string
	if recipients, ok := objectList(raw); ok {
		for _, recipient := range recipients {
			if email := validate.NormalizeAddress(stringField(recipient, "emailAddress")); email != "" {
				emails = append(emails, email)
			}
		}
	}
	if len(emails) > 0 {
		return dedupe(emails)
	}
	if fallback != "" {
		return []string{fallback}
	}
	return nil
}

func objectList(raw json.RawMessage) ([]map[string]json.RawMessage, bool) {
	var items []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return nil, false
	}
	objects := make([]map[string]json.RawMessage, 0, len(items))
	for _, item := range items {
		object, ok := objectOf(item)
		if !ok {
			continue
		}
		objects = append(objects, object)
	}
	return objects, true
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func firstDate(values string, fallback string, now time.Time) time.Time {
	for _, value := range []string{values, fallback} {
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed
		}
	}
	return now
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// withoutMailHeaders copies the event without mail.headers: raw headers are not worth storing, and
// never overwrite the original.
func withoutMailHeaders(raw json.RawMessage) json.RawMessage {
	top, ok := objectOf(raw)
	if !ok {
		return raw
	}
	mail, ok := objectOf(top["mail"])
	if !ok {
		return raw
	}
	delete(mail, "headers")
	encodedMail, err := json.Marshal(mail)
	if err != nil {
		return raw
	}
	top["mail"] = encodedMail
	encoded, err := json.Marshal(top)
	if err != nil {
		return raw
	}
	return encoded
}
