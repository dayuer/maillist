package store

import (
	"context"
	"testing"

	"mailcheap/internal/core/importer"
	"mailcheap/internal/core/model"
)

func ptr[T any](value T) *T { return &value }

// 同一份 CSV 导两次：第二次既有的联系人 rate 被刷新，状态仍只前进。
func TestSecondImportRefreshesImportedRatesOnly(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)

	first := []importer.Row{{Email: "reader@example.com", Status: model.ContactActive, OpenRate: ptr(0.1)}}
	created, updated, err := db.ApplyImportRows(ctx, newsletter.ID, "import", first, nil)
	if err != nil || created != 1 || updated != 0 {
		t.Fatalf("first import = %d %d %v", created, updated, err)
	}
	contact, err := db.ContactByEmail(ctx, newsletter.ID, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if contact.OpenRate == nil || *contact.OpenRate != 0.1 {
		t.Fatalf("imported open rate = %v", contact.OpenRate)
	}

	// The reader unsubscribed in the meantime; an import may not undo that.
	contact.Status = model.ContactUnsubscribed
	if err := db.UpdateContact(ctx, contact); err != nil {
		t.Fatal(err)
	}
	existing := map[string]model.Contact{contact.Email: contact}

	second := []importer.Row{{
		Email: "reader@example.com", Status: model.ContactActive,
		OpenRate: ptr(0.9), ClickRate: ptr(0.4), EmailsReceived: ptr(37),
	}}
	created, updated, err = db.ApplyImportRows(ctx, newsletter.ID, "import", second, existing)
	if err != nil || created != 0 || updated != 1 {
		t.Fatalf("second import = %d %d %v", created, updated, err)
	}
	after, err := db.ContactByEmail(ctx, newsletter.ID, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if after.OpenRate == nil || *after.OpenRate != 0.9 {
		t.Fatalf("open rate not refreshed: %v", after.OpenRate)
	}
	if after.ClickRate == nil || *after.ClickRate != 0.4 {
		t.Fatalf("click rate not refreshed: %v", after.ClickRate)
	}
	if after.EmailsReceived != 37 {
		t.Fatalf("emails received not refreshed: %d", after.EmailsReceived)
	}
	if after.Status != model.ContactUnsubscribed {
		t.Fatalf("status = %s, want unsubscribed (never back to active)", after.Status)
	}
}

// 严格更严的状态仍然前进；更松的不动。
func TestImportOnlyMovesStatusesStricter(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)
	seedContact(t, db, newsletter.ID, "reader@example.com")
	contact, err := db.ContactByEmail(ctx, newsletter.ID, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]model.Contact{contact.Email: contact}

	rows := []importer.Row{{Email: contact.Email, Status: model.ContactDormant}}
	if _, updated, err := db.ApplyImportRows(ctx, newsletter.ID, "import", rows, existing); err != nil || updated != 1 {
		t.Fatalf("dormant import = %d %v", updated, err)
	}
	after, err := db.ContactByEmail(ctx, newsletter.ID, contact.Email)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactDormant {
		t.Fatalf("status = %s, want dormant", after.Status)
	}
}

// 一个地址在同一份 CSV 里出现两次：只建一行。
func TestImportIsIdempotentWithinOneCall(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)

	rows := []importer.Row{
		{Email: "reader@example.com", Status: model.ContactActive},
		{Email: "reader@example.com", Status: model.ContactActive},
	}
	if _, _, err := db.ApplyImportRows(ctx, newsletter.ID, "import", rows, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx,
		"SELECT count(*) FROM contacts WHERE newsletter_id = ?", newsletter.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("contacts = %d, want 1", count)
	}
}

// 导入 bounced/complained 行时，suppression 也要落。
func TestImportedBouncedContactsAlsoSuppress(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	newsletter := seedNewsletter(t, db)

	rows := []importer.Row{
		{Email: "bounced@example.com", Status: model.ContactBounced},
		{Email: "angry@example.com", Status: model.ContactComplained},
		{Email: "quiet@example.com", Status: model.ContactDormant},
	}
	if _, _, err := db.ApplyImportRows(ctx, newsletter.ID, "import", rows, nil); err != nil {
		t.Fatal(err)
	}
	for email, write := range map[string]model.ContactStatus{
		"bounced@example.com": model.ContactBounced,
		"angry@example.com":   model.ContactComplained,
	} {
		contact, err := db.ContactByEmail(ctx, newsletter.ID, email)
		if err != nil {
			t.Fatal(err)
		}
		if contact.Status != write {
			t.Errorf("%s: status = %s, want %s", email, contact.Status, write)
		}
	}
	if reason, ok, _ := db.Suppression(ctx, "bounced@example.com"); !ok || reason != model.SuppressBounce {
		t.Errorf("bounced suppression = %q %v", reason, ok)
	}
	if reason, ok, _ := db.Suppression(ctx, "angry@example.com"); !ok || reason != model.SuppressComplaint {
		t.Errorf("complaint suppression = %q %v", reason, ok)
	}
	if _, ok, _ := db.Suppression(ctx, "quiet@example.com"); ok {
		t.Error("a dormant import must not suppress")
	}
}
