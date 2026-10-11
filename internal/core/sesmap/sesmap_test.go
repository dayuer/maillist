package sesmap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/model"
)

const (
	testMessageID = "64f1a2b3c4d5e6f7a8b9c0d1"
	sesMessageID  = "0100019000000abc-11112222-3333-4444-5555-666677778888-000000"
)

// mailJSON is the "mail" fixture of tests/ses-events.test.mjs.
const mailJSON = `{
	"timestamp": "2026-10-04T10:00:00.000Z",
	"messageId": "` + sesMessageID + `",
	"destination": ["Reader@Example.com"],
	"headers": [{"name": "List-Unsubscribe", "value": "<https://mail.example.com/api/unsubscribe>"}],
	"tags": {"message_id": ["` + testMessageID + `"], "kind": ["campaign"], "ses:configuration-set": ["mailcheap"]}
}`

var detailKey = map[string]string{
	"Send": "send", "Delivery": "delivery", "Bounce": "bounce", "Complaint": "complaint",
	"Open": "open", "Click": "click", "Reject": "reject", "DeliveryDelay": "deliveryDelay",
}

// sesEvent is upstream sesEvent: an event with the shared mail block.
func sesEvent(t *testing.T, eventType, detail string, overrides ...string) json.RawMessage {
	t.Helper()
	mail := json.RawMessage(mailJSON)
	var extra string
	if len(overrides) > 0 {
		mail = json.RawMessage(overrides[0])
	}
	if len(overrides) > 1 {
		extra = overrides[1]
	}
	key := detailKey[eventType]
	if key == "" {
		key = "failure"
	}
	body := `{"eventType": ` + quote(eventType) + `, "mail": ` + string(mail)
	if detail != "" {
		body += `, "` + key + `": ` + detail
	}
	if extra != "" {
		body += ", " + extra
	}
	return json.RawMessage(body + "}")
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func planOf(t *testing.T, raw json.RawMessage) Plan {
	t.Helper()
	plan, ok := EventPlan(raw, time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatalf("EventPlan(%s) did not map", raw)
	}
	return plan
}

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func firstFields(plan Plan) []string {
	fields := make([]string, len(plan.Firsts))
	for i, f := range plan.Firsts {
		fields[i] = f.Field
	}
	return fields
}

func equalStrings(a []string, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

func statuses(values []model.MessageStatus) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

func contactStatuses(values []model.ContactStatus) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

func assertStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !equalStrings(got, want) {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// tests/ses-events.test.mjs "deliveries set deliveredAt once and only move sent messages forward".
func TestDeliveriesSetDeliveredAtOnce(t *testing.T) {
	plan := planOf(t, sesEvent(t, "Delivery", `{"timestamp": "2026-10-04T10:00:05.000Z", "recipients": ["reader@example.com"]}`))
	if plan.Type != EventDelivery {
		t.Errorf("type = %s", plan.Type)
	}
	if plan.SESMessageID != sesMessageID {
		t.Errorf("sesMessageId = %q", plan.SESMessageID)
	}
	if plan.MessageID != testMessageID {
		t.Errorf("messageId = %q", plan.MessageID)
	}
	if plan.Email != "reader@example.com" {
		t.Errorf("email = %q", plan.Email)
	}
	if !plan.OccurredAt.Equal(at(t, "2026-10-04T10:00:05.000Z")) {
		t.Errorf("occurredAt = %s", plan.OccurredAt)
	}
	assertStrings(t, "firsts", firstFields(plan), []string{"deliveredAt"})
	if plan.Firsts[0].Stat != "delivered" {
		t.Errorf("stat = %q", plan.Firsts[0].Stat)
	}
	if plan.NewStatus != model.MessageDelivered {
		t.Errorf("status = %s", plan.NewStatus)
	}
	assertStrings(t, "status from", statuses(plan.StatusFrom),
		[]string{"pending", "claimed", "sent", "failed"})
	if len(plan.Suppress) != 0 || plan.Engagement != nil {
		t.Errorf("unexpected suppression or engagement: %+v", plan)
	}
}

// tests/ses-events.test.mjs "stored event data drops the raw mail headers".
func TestStoredEventDataDropsRawMailHeaders(t *testing.T) {
	raw := sesEvent(t, "Delivery", "")
	plan := planOf(t, raw)
	var stored, original struct {
		Mail struct {
			Headers []any          `json:"headers"`
			Tags    map[string]any `json:"tags"`
		} `json:"mail"`
	}
	if err := json.Unmarshal(plan.Store, &stored); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	if stored.Mail.Headers != nil {
		t.Errorf("headers kept: %d", len(stored.Mail.Headers))
	}
	if len(original.Mail.Headers) != 1 {
		t.Errorf("the input was mutated: %d headers", len(original.Mail.Headers))
	}
	if len(stored.Mail.Tags) != 3 {
		t.Errorf("tags = %v", stored.Mail.Tags)
	}
}

// tests/ses-events.test.mjs "hard bounces suppress the bounced address and mark contacts bounced".
func TestHardBouncesSuppressTheAddressAndMarkContactsBounced(t *testing.T) {
	plan := planOf(t, sesEvent(t, "Bounce", `{
		"bounceType": "Permanent", "bounceSubType": "General",
		"bouncedRecipients": [{"emailAddress": "Reader <READER@example.com>", "diagnosticCode": "smtp; 550 5.1.1 unknown"}],
		"timestamp": "2026-10-04T10:00:03.000Z"
	}`))
	assertStrings(t, "firsts", firstFields(plan), []string{"bouncedAt"})
	assertStrings(t, "status from", statuses(plan.StatusFrom),
		[]string{"pending", "claimed", "sent", "failed", "delivered"})
	assertStrings(t, "suppressed", plan.Suppress, []string{"reader@example.com"})
	if plan.SuppressAs != model.SuppressBounce || plan.SuppressDetail != "Permanent/General" {
		t.Errorf("suppression = %s %q", plan.SuppressAs, plan.SuppressDetail)
	}
	if plan.ContactStatus != model.ContactBounced {
		t.Errorf("contact status = %s", plan.ContactStatus)
	}
	assertStrings(t, "contact status from", contactStatuses(plan.ContactStatusFrom),
		[]string{"active", "dormant", "unsubscribed"})
	if plan.Error != "Hard bounce: General, smtp; 550 5.1.1 unknown" {
		t.Errorf("error = %q", plan.Error)
	}
}

// tests/ses-events.test.mjs "soft bounces only record the reason".
func TestSoftBouncesOnlyRecordTheReason(t *testing.T) {
	for _, bounceType := range []string{"Transient", "Undetermined"} {
		plan := planOf(t, sesEvent(t, "Bounce", `{
			"bounceType": `+quote(bounceType)+`, "bounceSubType": "MailboxFull",
			"bouncedRecipients": [{"emailAddress": "reader@example.com"}]
		}`))
		if len(plan.Firsts) != 0 || plan.NewStatus != "" || len(plan.Suppress) != 0 {
			t.Errorf("%s: %+v", bounceType, plan)
		}
		if plan.Error != "Soft bounce: MailboxFull" {
			t.Errorf("%s: error = %q", bounceType, plan.Error)
		}
	}
}

// tests/ses-events.test.mjs "complaints suppress the address and outrank an earlier bounce".
func TestComplaintsSuppressTheAddressAndOutrankABounce(t *testing.T) {
	plan := planOf(t, sesEvent(t, "Complaint", `{
		"complainedRecipients": [{"emailAddress": "reader@example.com"}],
		"complaintFeedbackType": "abuse", "timestamp": "2026-10-04T12:00:00.000Z"
	}`))
	assertStrings(t, "firsts", firstFields(plan), []string{"complainedAt"})
	if plan.NewStatus != model.MessageComplained {
		t.Errorf("status = %s", plan.NewStatus)
	}
	from := statuses(plan.StatusFrom)
	if !contains(from, "bounced") {
		t.Errorf("status from = %v", from)
	}
	if plan.SuppressAs != model.SuppressComplaint || plan.SuppressDetail != "abuse" {
		t.Errorf("suppression = %s %q", plan.SuppressAs, plan.SuppressDetail)
	}
	if plan.ContactStatus != model.ContactComplained {
		t.Errorf("contact status = %s", plan.ContactStatus)
	}
	contactFrom := contactStatuses(plan.ContactStatusFrom)
	if !contains(contactFrom, "bounced") || contains(contactFrom, "complained") {
		t.Errorf("contact status from = %v", contactFrom)
	}

	withoutRecipients := planOf(t, sesEvent(t, "Complaint", `{}`))
	assertStrings(t, "fallback recipients", withoutRecipients.Suppress, []string{"reader@example.com"})
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// tests/ses-events.test.mjs "not-spam feedback is not a complaint".
func TestNotSpamFeedbackIsNotAComplaint(t *testing.T) {
	plan := planOf(t, sesEvent(t, "Complaint", `{
		"complainedRecipients": [{"emailAddress": "reader@example.com"}],
		"complaintFeedbackType": "not-spam"
	}`))
	if len(plan.Firsts) != 0 || plan.NewStatus != "" || len(plan.Suppress) != 0 {
		t.Errorf("not-spam was treated as a complaint: %+v", plan)
	}
}

// tests/ses-events.test.mjs "opens update engagement and count once per message".
func TestOpensUpdateEngagement(t *testing.T) {
	plan := planOf(t, sesEvent(t, "Open", `{"timestamp": "2026-10-04T11:00:00.000Z"}`))
	assertStrings(t, "firsts", firstFields(plan), []string{"openedAt"})
	if plan.Engagement == nil || plan.Engagement.LastOpenAt == nil ||
		!plan.Engagement.LastOpenAt.Equal(at(t, "2026-10-04T11:00:00.000Z")) ||
		plan.Engagement.LastClickAt != nil {
		t.Errorf("engagement = %+v", plan.Engagement)
	}
	if plan.NewStatus != "" {
		t.Errorf("status = %s", plan.NewStatus)
	}
}

// tests/ses-events.test.mjs "a click also counts as an open, since images may be blocked".
func TestAClickAlsoCountsAsAnOpen(t *testing.T) {
	plan := planOf(t, sesEvent(t, "Click", `{"timestamp": "2026-10-04T11:05:00.000Z", "link": "https://example.com/post"}`))
	assertStrings(t, "firsts", firstFields(plan), []string{"openedAt", "clickedAt"})
	clickedAt := at(t, "2026-10-04T11:05:00.000Z")
	if plan.Engagement == nil || plan.Engagement.LastOpenAt == nil || plan.Engagement.LastClickAt == nil ||
		!plan.Engagement.LastOpenAt.Equal(clickedAt) || !plan.Engagement.LastClickAt.Equal(clickedAt) {
		t.Errorf("engagement = %+v", plan.Engagement)
	}
}

// tests/ses-events.test.mjs "clicks on unsubscribe links are not engagement".
func TestClicksOnUnsubscribeLinksAreNotEngagement(t *testing.T) {
	for _, link := range []string{
		"https://mail.example.com/unsubscribe?token=abc.def",
		"https://mail.example.com/api/unsubscribe?token=abc.def",
	} {
		plan := planOf(t, sesEvent(t, "Click", `{"link": `+quote(link)+`}`))
		if len(plan.Firsts) != 0 || plan.Engagement != nil {
			t.Errorf("%s: %+v", link, plan)
		}
	}
	article := planOf(t, sesEvent(t, "Click", `{"link": "https://example.com/unsubscribe-guide"}`))
	assertStrings(t, "article click", firstFields(article), []string{"openedAt", "clickedAt"})
}

// tests/ses-events.test.mjs "rejects and rendering failures mark the message failed".
func TestRejectsAndRenderingFailuresMarkTheMessageFailed(t *testing.T) {
	reject := planOf(t, sesEvent(t, "Reject", `{"reason": "Bad content"}`))
	if reject.NewStatus != model.MessageFailed {
		t.Errorf("status = %s", reject.NewStatus)
	}
	assertStrings(t, "status from", statuses(reject.StatusFrom), []string{"pending", "claimed", "sent"})
	if reject.Error != "Rejected by SES: Bad content" {
		t.Errorf("error = %q", reject.Error)
	}
	if !reject.OccurredAt.Equal(at(t, "2026-10-04T10:00:00.000Z")) {
		t.Errorf("occurredAt = %s", reject.OccurredAt)
	}

	rendering := planOf(t, sesEvent(t, "Rendering Failure", `{
		"errorMessage": "Attribute 'name' is not present", "templateName": "welcome"
	}`))
	if rendering.Type != EventRenderingFailure {
		t.Errorf("type = %s", rendering.Type)
	}
	if rendering.NewStatus != model.MessageFailed {
		t.Errorf("status = %s", rendering.NewStatus)
	}
	if rendering.Error != "Rendering failed: Attribute 'name' is not present" {
		t.Errorf("error = %q", rendering.Error)
	}
}

// tests/ses-events.test.mjs "send and delivery delay events are only recorded".
func TestSendAndDeliveryDelayEventsAreOnlyRecorded(t *testing.T) {
	for _, raw := range []json.RawMessage{
		sesEvent(t, "Send", ""),
		sesEvent(t, "DeliveryDelay", `{"delayType": "MailboxFull"}`),
	} {
		plan := planOf(t, raw)
		if len(plan.Firsts) != 0 || plan.NewStatus != "" || plan.Error != "" || len(plan.Suppress) != 0 {
			t.Errorf("%s: %+v", plan.Type, plan)
		}
	}
}

// tests/ses-events.test.mjs "identity notifications (notificationType) map like event publishing".
func TestIdentityNotificationsMapLikeEventPublishing(t *testing.T) {
	var mail json.RawMessage
	if err := json.Unmarshal([]byte(mailJSON), &mail); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"notificationType": "Bounce", "mail": ` + string(mail) + `, "bounce": {
		"bounceType": "Permanent", "bounceSubType": "NoEmail",
		"bouncedRecipients": [{"emailAddress": "reader@example.com"}]
	}}`)
	plan := planOf(t, raw)
	if plan.Type != EventBounce {
		t.Errorf("type = %s", plan.Type)
	}
	if plan.SuppressDetail != "Permanent/NoEmail" {
		t.Errorf("suppression detail = %q", plan.SuppressDetail)
	}
}

// tests/ses-events.test.mjs "message ids from tags must be own, valid ObjectId strings".
func TestMessageIDsFromTagsMustBeValidIDs(t *testing.T) {
	withTags := func(tags string) string {
		return `{"timestamp": "2026-10-04T10:00:00.000Z", "messageId": "` + sesMessageID + `",
			"destination": ["Reader@Example.com"], "tags": ` + tags + `}`
	}
	for tags, want := range map[string]string{
		`{"message_id": ["../../etc/passwd"]}`: "",
		`{"message_id": [{"$ne": null}]}`:      "",
		`{}`:                                   "",
		`{"message_id": ["` + strings.ToUpper(testMessageID) + `"]}`: testMessageID,
		`{"message_id": "` + testMessageID + `"}`:                    testMessageID,
	} {
		raw := sesEvent(t, "Open", `{}`, withTags(tags))
		if got := planOf(t, raw).MessageID; got != want {
			t.Errorf("tags %s: messageId = %q, want %q", tags, got, want)
		}
	}
	// Upstream's Object.create({...}) case has no JSON equivalent: a tag is either in the object or
	// absent.
	if _, ok := EventPlan(sesEvent(t, "Open", `{}`, `{"timestamp": "x", "destination": []}`), time.Now()); !ok {
		t.Error("a mail object without tags must still produce a plan")
	}
}

// tests/ses-events.test.mjs "missing or invalid timestamps fall back to the current time".
func TestMissingOrInvalidTimestampsFallBackToNow(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	raw := sesEvent(t, "Open", `{"timestamp": "nope"}`, `{"timestamp": "garbage",
		"messageId": "`+sesMessageID+`", "destination": ["Reader@Example.com"]}`)
	plan, ok := EventPlan(raw, now)
	if !ok {
		t.Fatal("must still map")
	}
	if !plan.OccurredAt.Equal(now) {
		t.Errorf("occurredAt = %s", plan.OccurredAt)
	}

	withoutMail, ok := EventPlan(json.RawMessage(`{"eventType": "Open", "open": {}}`), now)
	if !ok {
		t.Fatal("must still map")
	}
	if withoutMail.SESMessageID != "" || withoutMail.Email != "" {
		t.Errorf("sesMessageId = %q, email = %q", withoutMail.SESMessageID, withoutMail.Email)
	}
}

// tests/ses-events.test.mjs "unknown or malformed events are ignored".
func TestUnknownOrMalformedEventsAreIgnored(t *testing.T) {
	var mail json.RawMessage
	if err := json.Unmarshal([]byte(mailJSON), &mail); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(`"Bounce"`),
		json.RawMessage(`42`),
		json.RawMessage(`[]`),
		json.RawMessage(`{}`),
		json.RawMessage(`null`),
		sesEvent(t, "Send", "", string(mail), `"eventType": "Subscription"`),
		sesEvent(t, "Send", "", string(mail), `"eventType": "constructor"`),
		sesEvent(t, "Send", "", string(mail), `"eventType": "__proto__"`),
		json.RawMessage(`{"notificationType": "AmazonSnsSubscriptionSucceeded"}`),
	} {
		if plan, ok := EventPlan(raw, time.Now()); ok {
			t.Errorf("%s mapped to %+v", raw, plan)
		}
		if got := Type(raw); got != "" {
			t.Errorf("Type(%s) = %q", raw, got)
		}
	}
}

// tests/tracking.test.mjs "tracked clicks also count as opens".
func TestTrackingPlanCountsClicksAsOpens(t *testing.T) {
	at := time.Date(2026, 10, 6, 10, 5, 0, 0, time.UTC)
	assertStrings(t, "open firsts", firstFields(TrackingPlan(EventOpen, at)), []string{"openedAt"})
	click := TrackingPlan(EventClick, at)
	assertStrings(t, "click firsts", firstFields(click), []string{"openedAt", "clickedAt"})
	if click.Engagement == nil || click.Engagement.LastOpenAt == nil || click.Engagement.LastClickAt == nil ||
		!click.Engagement.LastOpenAt.Equal(at) || !click.Engagement.LastClickAt.Equal(at) {
		t.Errorf("engagement = %+v", click.Engagement)
	}
	if click.NewStatus != "" || click.Error != "" {
		t.Errorf("a tracking plan changes no status: %+v", click)
	}
}

// The MessageTags upstream writes (lib/sending.js getMessageTags) let an event name its own kind.
func TestKindComesFromTheKindTag(t *testing.T) {
	raw := sesEvent(t, "Open", `{}`, `{"timestamp": "2026-10-04T10:00:00.000Z",
		"messageId": "`+sesMessageID+`", "destination": [],
		"tags": {"message_id": ["`+testMessageID+`"], "kind": ["last-chance"]}}`)
	if got := planOf(t, raw).Kind; got != model.KindLastChance {
		t.Errorf("kind = %q", got)
	}
	unknown := sesEvent(t, "Open", `{}`, `{"timestamp": "2026-10-04T10:00:00.000Z",
		"messageId": "`+sesMessageID+`", "destination": [],
		"tags": {"kind": ["unheard-of"]}}`)
	if got := planOf(t, unknown).Kind; got != "" {
		t.Errorf("kind = %q", got)
	}
}
