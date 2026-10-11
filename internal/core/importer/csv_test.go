package importer

import (
	"testing"
	"time"

	"mailcheap/internal/core/model"
)

// tests/csv-import.test.mjs "maps common export headers". Upstream returns the header strings;
// Columns holds their indexes instead.
func TestMapsCommonExportHeaders(t *testing.T) {
	cols := MapColumns([]string{"Email", "Status", "Created At", "Open Rate", "Click Through Rate"})
	want := Columns{Email: 0, Status: 1, SubscribedAt: 2, EmailsReceived: -1, OpenRate: 3, ClickRate: 4}
	if cols != want {
		t.Fatalf("columns = %+v, want %+v", cols, want)
	}
}

// tests/csv-import.test.mjs "maps statuses and never guesses active for unknown values".
func TestMapsStatusesAndNeverGuessesActive(t *testing.T) {
	for in, want := range map[string]model.ContactStatus{
		"active":        model.ContactActive,
		"inactive":      model.ContactUnsubscribed,
		"Invalid":       model.ContactBounced,
		"pending":       model.ContactDormant,
		"something-new": model.ContactDormant,
		"":              model.ContactActive,
	} {
		if got := NormalizeStatus(in); got != want {
			t.Errorf("NormalizeStatus(%q) = %s, want %s", in, got, want)
		}
	}
}

// tests/csv-import.test.mjs "normalizes rates written as percents or fractions".
func TestNormalizesRatesWrittenAsPercentsOrFractions(t *testing.T) {
	for in, want := range map[string]float64{"45.5%": 0.455, "45.5": 0.455, "0.3": 0.3, "250%": 1} {
		got, ok := NormalizeRate(in)
		if !ok || got != want {
			t.Errorf("NormalizeRate(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc"} {
		if got, ok := NormalizeRate(in); ok {
			t.Errorf("NormalizeRate(%q) = %v, want not ok", in, got)
		}
	}
}

// tests/csv-import.test.mjs "normalizes a row and applies a stricter status override only".
func TestNormalizesARowAndAppliesAStricterOverrideOnly(t *testing.T) {
	cols := MapColumns([]string{"email", "status", "created_at"})

	active, ok := NormalizeRow([]string{" Jane@Example.com ", "active", "2024-01-02"}, cols, model.ContactDormant)
	if !ok {
		t.Fatal("row rejected")
	}
	if active.Email != "jane@example.com" {
		t.Errorf("email = %q", active.Email)
	}
	if active.Status != model.ContactDormant {
		t.Errorf("status = %s", active.Status)
	}
	if active.SubscribedAt == nil || !active.SubscribedAt.Equal(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("subscribedAt = %v", active.SubscribedAt)
	}

	unsubscribed, ok := NormalizeRow([]string{"reader@example.com", "inactive"}, cols, model.ContactDormant)
	if !ok {
		t.Fatal("row rejected")
	}
	if unsubscribed.Status != model.ContactUnsubscribed {
		t.Errorf("status = %s", unsubscribed.Status)
	}

	if _, ok := NormalizeRow([]string{"not-an-email"}, cols, ""); ok {
		t.Error("an invalid email must be rejected")
	}
}
