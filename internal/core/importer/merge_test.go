package importer

import (
	"testing"
	"time"

	"nibwire/internal/core/model"
)

var (
	subscribedAt = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	rate50       = 0.5
	zero         = 0.0
	nine         = 9
)

// Rows as NormalizeRow hands them over, one per status a CSV file can map to.
func importRows() []Row {
	return []Row{
		{Email: "new@example.com", Status: model.ContactActive, SubscribedAt: &subscribedAt, OpenRate: &rate50},
		{Email: "left@example.com", Status: model.ContactUnsubscribed},
		{Email: "left-too@example.com", Status: model.ContactUnsubscribed},
		{Email: "bounced@example.com", Status: model.ContactBounced},
		{Email: "cold@example.com", Status: model.ContactDormant, EmailsReceived: &nine, OpenRate: &zero, ClickRate: &zero},
	}
}

// tests/contact-import.test.mjs "rows add the contacts a newsletter doesn't have and only make
// statuses stricter". Upstream asserts the Mongo bulkWrite document shape, which the store task
// covers instead; this asserts the same decisions in Merge's vocabulary.
func TestRowsAddMissingContactsAndOnlyMakeStatusesStricter(t *testing.T) {
	rows := importRows()
	writes := Merge(rows, nil)
	if len(writes) != len(rows) {
		t.Fatalf("writes = %d, want %d", len(writes), len(rows))
	}
	for i, write := range writes {
		if write.Insert == nil {
			t.Errorf("%d: a contact the newsletter doesn't have must be inserted", i)
			continue
		}
		if write.Insert.Email != rows[i].Email || write.Insert.Status != rows[i].Status {
			t.Errorf("%d: insert = %+v", i, write.Insert)
		}
		if write.UpdateID != "" {
			t.Errorf("%d: a new contact has no id to update", i)
		}
	}
	if writes[0].Insert.OpenRate == nil || *writes[0].Insert.OpenRate != 0.5 {
		t.Errorf("imported history lost: %+v", writes[0].Insert)
	}

	// The same rows against contacts the newsletter already has, all active: only the stricter
	// statuses move.
	existing := map[string]model.Contact{}
	for _, row := range rows {
		existing[row.Email] = model.Contact{ID: "id-" + row.Email, Email: row.Email, Status: model.ContactActive}
	}
	for i, write := range Merge(rows, existing) {
		if write.Insert != nil {
			t.Errorf("%d: an existing contact must not be inserted", i)
			continue
		}
		want := model.ContactStatus("")
		switch rows[i].Status {
		case model.ContactActive: // never moves a contact to active
			want = ""
		default:
			want = rows[i].Status
		}
		if write.NewStatus != want {
			t.Errorf("%d (%s): newStatus = %q, want %q", i, rows[i].Email, write.NewStatus, want)
		}
		if write.NewStatus != "" && write.UpdateID != "id-"+rows[i].Email {
			t.Errorf("%d: updateID = %q", i, write.UpdateID)
		}
	}
}

// tests/contact-import.test.mjs "existing contacts never become active and contacts waiting to
// confirm stay as they are".
func TestExistingContactsNeverBecomeActiveAndPendingStays(t *testing.T) {
	rows := importRows()
	existing := map[string]model.Contact{
		"new@example.com":     {ID: "1", Email: "new@example.com", Status: model.ContactComplained},
		"left@example.com":    {ID: "2", Email: "left@example.com", Status: model.ContactBounced},
		"cold@example.com":    {ID: "4", Email: "cold@example.com", Status: model.ContactPending},
		"bounced@example.com": {ID: "5", Email: "bounced@example.com", Status: model.ContactDormant},
	}
	// Merge returns one write per row, in row order.
	writes := Merge(rows, existing)
	if len(writes) != len(rows) {
		t.Fatalf("writes = %d, want %d", len(writes), len(rows))
	}
	byEmail := map[string]Write{}
	for i, write := range writes {
		byEmail[rows[i].Email] = write
	}

	for email, write := range byEmail {
		if write.NewStatus == model.ContactActive {
			t.Errorf("%s moved back to active", email)
		}
	}
	// new@ is complained, the strictest there is: nothing moves it.
	if w := byEmail["new@example.com"]; w.NewStatus != "" {
		t.Errorf("complained moved to %q", w.NewStatus)
	}
	if w := byEmail["left@example.com"]; w.NewStatus != "" {
		t.Errorf("bounced moved to %q", w.NewStatus)
	}
	// cold@ waits to confirm: an import must leave it alone.
	if w := byEmail["cold@example.com"]; w.NewStatus != "" {
		t.Errorf("pending moved to %q", w.NewStatus)
	}
	// bounced@ is dormant, so bounced is stricter.
	if w := byEmail["bounced@example.com"]; w.NewStatus != model.ContactBounced {
		t.Errorf("dormant→bounced did not move: %+v", w)
	}
}

// Added by the plan: importing bounced or complained rows also upserts the global suppression.
func TestBouncedAndComplainedImportsAlsoSuppress(t *testing.T) {
	rows := []Row{
		{Email: "bounced@example.com", Status: model.ContactBounced},
		{Email: "angry@example.com", Status: model.ContactComplained},
		{Email: "quiet@example.com", Status: model.ContactDormant},
		{Email: "new@example.com", Status: model.ContactActive},
	}
	want := map[string]model.SuppressionReason{
		"bounced@example.com": model.SuppressBounce,
		"angry@example.com":   model.SuppressComplaint,
		"quiet@example.com":   "",
		"new@example.com":     "",
	}
	for _, write := range Merge(rows, nil) {
		if write.Insert == nil {
			t.Fatal("every row is new here")
		}
		if write.Suppress != want[write.Insert.Email] {
			t.Errorf("%s: suppress = %q, want %q", write.Insert.Email, write.Suppress, want[write.Insert.Email])
		}
	}
}
