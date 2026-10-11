package store

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"mailcheap/internal/core/audience"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
)

// 200 random contacts: the SQL and the pure comparator must agree on every position. This is the
// test the plan's audience task exists for — it is what proves the two implementations of
// AUDIENCE_SORT never drift.
func TestAudienceSQLMatchesComparator(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	// A fixed seed keeps the fixture reproducible; this is test data, not a secret.
	random := rand.New(rand.NewSource(20261011)) //nolint:gosec // G404: deterministic test fixture

	contacts := make([]model.Contact, 0, 200)
	for i := 0; i < 200; i++ {
		c := model.Contact{
			ID:           ids.New(),
			NewsletterID: newsletter.ID,
			Email:        ids.New() + "@example.com",
			Status:       model.ContactActive,
			CreatedAt:    randomTime(random),
		}
		if random.Intn(2) == 0 {
			c.LastClickAt = ptr(randomTime(random))
		}
		if random.Intn(2) == 0 {
			c.LastOpenAt = ptr(randomTime(random))
		}
		if random.Intn(3) == 0 {
			c.ClickRate = ptr(random.Float64())
		}
		if random.Intn(3) == 0 {
			c.OpenRate = ptr(random.Float64())
		}
		if random.Intn(10) == 0 {
			c.Status = model.ContactUnsubscribed
		}
		contacts = append(contacts, c)
		if err := db.CreateContact(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// A few suppressions, naming contacts the audience would otherwise include.
	suppressed := map[string]bool{}
	for _, c := range contacts[:3] {
		suppressed[c.Email] = true
		if err := db.AddSuppression(ctx, c.Email, model.SuppressManual, "test"); err != nil {
			t.Fatal(err)
		}
	}

	fromSQL, err := db.SelectAudience(ctx, newsletter.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fromCore := audience.Select(contacts, suppressed, 0)
	if len(fromSQL) != len(fromCore) {
		t.Fatalf("sql %d rows, core %d rows", len(fromSQL), len(fromCore))
	}
	for i := range fromCore {
		if fromSQL[i].ID != fromCore[i].ID {
			t.Fatalf("position %d: sql %s, core %s", i, fromSQL[i].ID, fromCore[i].ID)
		}
	}
}

func TestAudienceSQLRespectsTheLimit(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	for i := 0; i < 10; i++ {
		seedContact(t, db, newsletter.ID, ids.New()+"@example.com")
	}
	for _, tc := range []struct{ limit, want int }{{0, 10}, {3, 3}, {-1, 10}} {
		got, err := db.SelectAudience(ctx, newsletter.ID, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.want {
			t.Errorf("limit %d → %d rows, want %d", tc.limit, len(got), tc.want)
		}
	}
}

// An empty suppression list must not narrow the audience — the classic NOT IN (NULL) trap.
func TestAudienceSQLWithNoSuppressions(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "a@example.com")
	seedContact(t, db, newsletter.ID, "b@example.com")

	got, err := db.SelectAudience(ctx, newsletter.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("audience = %d rows, want 2", len(got))
	}
	emails, err := db.SuppressedEmails(ctx)
	if err != nil || len(emails) != 0 {
		t.Fatalf("suppressions = %v %v", emails, err)
	}
}

// Only active contacts are recipients; everyone else is invisible to a send.
func TestAudienceSQLTakesOnlyActiveContacts(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	for _, status := range []model.ContactStatus{
		model.ContactActive, model.ContactPending, model.ContactUnsubscribed,
		model.ContactBounced, model.ContactComplained, model.ContactDormant,
	} {
		contact := seedContact(t, db, newsletter.ID, string(status)+"@example.com")
		contact.Status = status
		if err := db.UpdateContact(ctx, contact); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.SelectAudience(ctx, newsletter.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Email != "active@example.com" {
		t.Fatalf("audience = %v", emailsOf(got))
	}
}

// The most engaged contact comes first even when it subscribed last.
func TestAudienceSQLPutsTheMostEngagedFirst(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)

	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	quiet := seedContact(t, db, newsletter.ID, "quiet@example.com")
	quiet.CreatedAt = old
	if err := db.UpdateContact(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	engaged := seedContact(t, db, newsletter.ID, "engaged@example.com")
	engaged.LastClickAt = &recent
	if err := db.UpdateContact(ctx, engaged); err != nil {
		t.Fatal(err)
	}

	got, err := db.SelectAudience(ctx, newsletter.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Email != "engaged@example.com" {
		t.Fatalf("audience = %v", emailsOf(got))
	}
}

func randomTime(random *rand.Rand) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).
		Add(time.Duration(random.Intn(300*24)) * time.Hour)
}
