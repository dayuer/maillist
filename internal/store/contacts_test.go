package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

func seedNewsletter(t *testing.T, db *DB) model.Newsletter {
	t.Helper()
	n := model.Newsletter{
		ID: ids.New(), Slug: "field-notes-" + ids.New(), Name: "Field notes",
		FromName: "Jane Doe", FromEmail: "jane@example.net", PostalAddress: "1 Main St, Paris",
		ArchiveEnabled: true,
	}
	if err := db.CreateNewsletter(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedContact(t *testing.T, db *DB, newsletterID, email string) model.Contact {
	t.Helper()
	c := model.Contact{
		ID: ids.New(), NewsletterID: newsletterID, Email: email,
		Status: model.ContactActive, CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateContact(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func emailsOf(contacts []model.Contact) []string {
	out := make([]string, len(contacts))
	for i, c := range contacts {
		out[i] = c.Email
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}

func TestNewsletterRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	want := seedNewsletter(t, db)
	want.SocialLinks = []string{"https://example.com/@jane"}

	if err := db.UpdateNewsletter(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := db.Newsletter(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != want.Slug || got.Name != want.Name || got.FromEmail != want.FromEmail ||
		got.PostalAddress != want.PostalAddress || !got.ArchiveEnabled ||
		len(got.SocialLinks) != 1 || got.SocialLinks[0] != want.SocialLinks[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	bySlug, err := db.NewsletterBySlug(ctx, want.Slug)
	if err != nil || bySlug.ID != want.ID {
		t.Fatalf("bySlug = %+v %v", bySlug, err)
	}
	if _, err := db.Newsletter(ctx, ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if list, err := db.Newsletters(ctx); err != nil || len(list) != 1 {
		t.Fatalf("list = %d %v", len(list), err)
	}
}

// Email is unique per newsletter; a second insert must fail instead of silently replacing.
func TestContactEmailIsUniquePerNewsletter(t *testing.T) {
	db := openTemp(t)
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "reader@example.com")

	duplicate := model.Contact{ID: ids.New(), NewsletterID: newsletter.ID, Email: "reader@example.com"}
	if err := db.CreateContact(context.Background(), duplicate); err == nil {
		t.Fatal("duplicate accepted")
	}
	// The same address in another newsletter is a different contact.
	other := seedNewsletter(t, db)
	seedContact(t, db, other.ID, "reader@example.com")
}

func TestContactRoundTripKeepsEngagement(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	contact := seedContact(t, db, newsletter.ID, "reader@example.com")

	clicked := time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC)
	rate := 0.45
	contact.Status = model.ContactDormant
	contact.LastClickAt = &clicked
	contact.ClickRate = &rate
	contact.Opens = 3
	contact.IgnoredIssues = 7
	if err := db.UpdateContact(ctx, contact); err != nil {
		t.Fatal(err)
	}

	got, err := db.ContactByEmail(ctx, newsletter.ID, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.ContactDormant || got.LastClickAt == nil || !got.LastClickAt.Equal(clicked) ||
		got.ClickRate == nil || *got.ClickRate != rate || got.Opens != 3 || got.IgnoredIssues != 7 {
		t.Fatalf("got %+v", got)
	}
	// A NULL engagement column round-trips as nil, not as the zero time.
	if got.LastOpenAt != nil || got.LastChanceAt != nil || got.OpenRate != nil {
		t.Fatalf("unset columns must stay nil: %+v", got)
	}
}

// A prefix search must not let LIKE wildcards through.
func TestContactPrefixSearchEscapesWildcards(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "a_b@example.com")
	seedContact(t, db, newsletter.ID, "axb@example.com")
	seedContact(t, db, newsletter.ID, "a%b@example.com")

	for prefix, want := range map[string][]string{
		"a_b": {"a_b@example.com"},
		"a%b": {"a%b@example.com"},
		"a":   {"a%b@example.com", "a_b@example.com", "axb@example.com"},
	} {
		got, err := db.Contacts(ctx, ContactFilter{NewsletterID: newsletter.ID, EmailPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		if !sameStrings(emailsOf(got), want) {
			t.Errorf("prefix %q → %v, want %v", prefix, emailsOf(got), want)
		}
	}
}

// A prefix search is case-insensitive, as the COLLATE NOCASE index decides.
func TestContactPrefixSearchIsCaseInsensitive(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "reader@example.com")

	got, err := db.Contacts(ctx, ContactFilter{NewsletterID: newsletter.ID, EmailPrefix: "REA"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v", emailsOf(got))
	}
	count, err := db.CountContacts(ctx, ContactFilter{NewsletterID: newsletter.ID, EmailPrefix: "rea"})
	if err != nil || count != 1 {
		t.Fatalf("count = %d %v", count, err)
	}
}

// The audience index must be the one the prefix search uses; a scan of the whole table means the
// index is not doing its job.
func TestContactPrefixSearchUsesAnIndex(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "reader@example.com")

	rows, err := db.sql.QueryContext(ctx,
		`EXPLAIN QUERY PLAN SELECT id FROM contacts WHERE newsletter_id = ? AND email LIKE ? ESCAPE '\' COLLATE NOCASE`,
		newsletter.ID, "re%")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "USING INDEX") && !strings.Contains(plan.String(), "USING COVERING INDEX") {
		t.Fatalf("prefix search is not index-backed:\n%s", plan.String())
	}
}

func TestContactFilterByStatusAndPaging(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	for i := 0; i < 5; i++ {
		c := seedContact(t, db, newsletter.ID, ids.New()+"@example.com")
		if i%2 == 0 {
			c.Status = model.ContactUnsubscribed
			if err := db.UpdateContact(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	active, err := db.Contacts(ctx, ContactFilter{NewsletterID: newsletter.ID, Status: model.ContactActive})
	if err != nil || len(active) != 2 {
		t.Fatalf("active = %d %v", len(active), err)
	}
	all, err := db.Contacts(ctx, ContactFilter{NewsletterID: newsletter.ID, Limit: 2})
	if err != nil || len(all) != 2 {
		t.Fatalf("paged = %d %v", len(all), err)
	}
	rest, err := db.Contacts(ctx, ContactFilter{NewsletterID: newsletter.ID, Limit: 2, Offset: 4})
	if err != nil || len(rest) != 1 {
		t.Fatalf("offset page = %d %v", len(rest), err)
	}
}

func TestUpdateContactStatusOnlyFromAllowedStatuses(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	contact := seedContact(t, db, newsletter.ID, "reader@example.com")

	moved, err := db.UpdateContactStatus(ctx, contact.ID, model.ContactBounced,
		[]model.ContactStatus{model.ContactActive, model.ContactDormant})
	if err != nil || !moved {
		t.Fatalf("moved = %v %v", moved, err)
	}
	// A second bounce from bounced is not a move, so no counter moves twice.
	again, err := db.UpdateContactStatus(ctx, contact.ID, model.ContactBounced,
		[]model.ContactStatus{model.ContactActive, model.ContactDormant})
	if err != nil || again {
		t.Fatalf("again = %v %v", again, err)
	}
}

func TestAddContactStatsAndEngagement(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	contact := seedContact(t, db, newsletter.ID, "reader@example.com")

	if err := db.AddContactStats(ctx, contact.ID, 2, 1, 0); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	if err := db.MarkContactEngaged(ctx, contact.ID, at); err != nil {
		t.Fatal(err)
	}
	got, err := db.Contact(ctx, contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sent != 2 || got.Opens != 1 || got.Clicks != 0 {
		t.Fatalf("stats = %+v", got)
	}
	if got.LastEngagedAt == nil || !got.LastEngagedAt.Equal(at) {
		t.Fatalf("lastEngagedAt = %v", got.LastEngagedAt)
	}
}

func TestSuppressionComplaintOutranksBounce(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	for _, tc := range []struct {
		first, second model.SuppressionReason
		want          model.SuppressionReason
	}{
		{model.SuppressBounce, model.SuppressComplaint, model.SuppressComplaint},
		{model.SuppressComplaint, model.SuppressBounce, model.SuppressComplaint},
		{model.SuppressManual, model.SuppressBounce, model.SuppressManual},
	} {
		email := ids.New() + "@example.com"
		if err := db.AddSuppression(ctx, email, tc.first, "first"); err != nil {
			t.Fatal(err)
		}
		if err := db.AddSuppression(ctx, email, tc.second, "second"); err != nil {
			t.Fatal(err)
		}
		reason, ok, err := db.Suppression(ctx, email)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", email, ok, err)
		}
		if reason != tc.want {
			t.Errorf("%s then %s → %s, want %s", tc.first, tc.second, reason, tc.want)
		}
	}

	removed, err := db.RemoveSuppression(ctx, "nobody@example.com")
	if err != nil || removed {
		t.Fatalf("remove missing = %v %v", removed, err)
	}
}
