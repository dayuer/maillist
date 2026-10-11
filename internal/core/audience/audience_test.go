package audience

import (
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/model"
)

// The fixture of tests/audience.test.mjs. "imported.clickRate"/"imported.openRate" are the flattened
// model.Contact.ClickRate/OpenRate.
var (
	defaultCreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	testContacts = []model.Contact{
		contact("never-engaged"),
		contact("early-signup", func(c *model.Contact) { c.CreatedAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }),
		contact("imported-opener", func(c *model.Contact) { c.OpenRate = ptr(0.6) }),
		contact("imported-clicker", func(c *model.Contact) { c.ClickRate = ptr(0.1); c.OpenRate = ptr(0.2) }),
		contact("imported-top-clicker", func(c *model.Contact) { c.ClickRate = ptr(0.3); c.OpenRate = ptr(0.1) }),
		contact("opened-recently", func(c *model.Contact) { c.LastOpenAt = at("2026-09-30") }),
		contact("opened-in-march", func(c *model.Contact) { c.LastOpenAt = at("2026-03-01") }),
		contact("clicked-in-february", func(c *model.Contact) {
			c.LastClickAt = at("2026-02-01")
			c.LastOpenAt = at("2026-02-01")
		}),
		contact("clicked-recently", func(c *model.Contact) { c.LastClickAt = at("2026-09-01") }),
		contact("unsubscribed", func(c *model.Contact) {
			c.Status = model.ContactUnsubscribed
			c.LastClickAt = at("2026-10-01")
		}),
		contact("bounced", func(c *model.Contact) { c.Status = model.ContactBounced }),
		contact("dormant", func(c *model.Contact) { c.Status = model.ContactDormant }),
		contact("suppressed", func(c *model.Contact) { c.LastClickAt = at("2026-10-02") }),
	}
	suppressedEmails = map[string]bool{"suppressed@example.com": true}
)

func contact(id string, fields ...func(*model.Contact)) model.Contact {
	c := model.Contact{
		ID: id, Email: id + "@example.com", Status: model.ContactActive, CreatedAt: defaultCreatedAt,
	}
	for _, f := range fields {
		f(&c)
	}
	return c
}

func at(day string) *time.Time {
	v, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return &v
}

func ptr(f float64) *float64 { return &f }

func ids(contacts []model.Contact) []string {
	out := make([]string, len(contacts))
	for i, c := range contacts {
		out[i] = c.ID
	}
	return out
}

func assertIDs(t *testing.T, got []model.Contact, want ...string) {
	t.Helper()
	if strings.Join(ids(got), ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids(got), want)
	}
}

// tests/audience.test.mjs "the audience is active, unsuppressed contacts, most engaged first".
func TestTheAudienceIsActiveUnsuppressedMostEngagedFirst(t *testing.T) {
	assertIDs(t, Select(testContacts, suppressedEmails, 0),
		"clicked-recently",
		"clicked-in-february",
		"opened-recently",
		"opened-in-march",
		"imported-top-clicker",
		"imported-clicker",
		"imported-opener",
		"early-signup",
		"never-engaged",
	)
}

// tests/audience.test.mjs "audienceLimit keeps the most engaged contacts".
func TestAudienceLimitKeepsTheMostEngagedContacts(t *testing.T) {
	assertIDs(t, Select(testContacts, suppressedEmails, 3),
		"clicked-recently", "clicked-in-february", "opened-recently")
	// Upstream also covers undefined, null, 0, -1, 2.5 and "abc"; only 0 and -1 are ints.
	for _, limit := range []int{0, -1} {
		if got := Select(testContacts, suppressedEmails, limit); len(got) != 9 {
			t.Errorf("limit %d: %d contacts, want 9", limit, len(got))
		}
	}
}

// tests/audience.test.mjs "contacts with equal engagement keep a stable order".
func TestContactsWithEqualEngagementKeepAStableOrder(t *testing.T) {
	twins := []model.Contact{contact("b"), contact("a"), contact("c")}
	assertIDs(t, Select(twins, nil, 0), "a", "b", "c")
	// The caller's slice is left alone, as upstream's toSorted leaves it.
	if got := ids(twins); strings.Join(got, ",") != "b,a,c" {
		t.Errorf("input reordered: %v", got)
	}
}
