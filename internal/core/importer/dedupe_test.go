package importer

import (
	"testing"
	"time"

	"mailcheap/internal/core/model"
)

// Upstream has no test for this: lib/contact-import.js leans on Mongo's per-row upsert to apply a
// repeated address in order, which has no single-statement equivalent here. The behaviour below is
// read off that loop — last imported history wins, strictest status wins — and is what makes an
// import of a file with a repeated address behave the way the provider's export intended.
func TestDedupe(t *testing.T) {
	received := func(n int) *int { return &n }
	rate := func(f float64) *float64 { return &f }

	rows := []Row{
		{Email: "one@example.com", Status: model.ContactActive, EmailsReceived: received(10), OpenRate: rate(0.2)},
		{Email: "two@example.com", Status: model.ContactActive},
		{Email: "one@example.com", Status: model.ContactBounced, EmailsReceived: received(40), ClickRate: rate(0.05)},
		{Email: "three@example.com", Status: model.ContactActive},
	}
	got := Dedupe(rows)

	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}
	// The repeated address keeps its first position.
	if got[0].Email != "one@example.com" || got[1].Email != "two@example.com" || got[2].Email != "three@example.com" {
		t.Fatalf("order changed: %+v", got)
	}
	one := got[0]
	if one.Status != model.ContactBounced {
		t.Errorf("status = %s, want the strictest of the two (bounced)", one.Status)
	}
	if one.EmailsReceived == nil || *one.EmailsReceived != 40 {
		t.Errorf("emailsReceived = %v, want the last row's 40", one.EmailsReceived)
	}
	// A field the last row does not carry keeps the earlier value.
	if one.OpenRate == nil || *one.OpenRate != 0.2 {
		t.Errorf("openRate = %v, want the first row's 0.2", one.OpenRate)
	}
	if one.ClickRate == nil || *one.ClickRate != 0.05 {
		t.Errorf("clickRate = %v, want the last row's 0.05", one.ClickRate)
	}
}

// 一个已退信/已投诉的地址不会因为文件后面又出现一行 active 而被拉回来。
func TestDedupeNeverLoosensAStatus(t *testing.T) {
	got := Dedupe([]Row{
		{Email: "gone@example.com", Status: model.ContactBounced},
		{Email: "gone@example.com", Status: model.ContactActive},
	})
	if len(got) != 1 || got[0].Status != model.ContactBounced {
		t.Fatalf("got %+v, want one bounced row", got)
	}
}

// pending 没有 rank，压不过任何一边，也不会被压过去。
func TestDedupeLeavesPendingAlone(t *testing.T) {
	got := Dedupe([]Row{
		{Email: "waiting@example.com", Status: model.ContactDormant},
		{Email: "waiting@example.com", Status: model.ContactPending},
	})
	if len(got) != 1 || got[0].Status != model.ContactDormant {
		t.Fatalf("got %+v, want the dormant row kept", got)
	}

	// And a subscribe date carried only by a later row is still kept.
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got = Dedupe([]Row{
		{Email: "a@example.com", Status: model.ContactActive},
		{Email: "a@example.com", SubscribedAt: &at},
	})
	if len(got) != 1 || got[0].SubscribedAt == nil || !got[0].SubscribedAt.Equal(at) {
		t.Fatalf("subscribedAt = %v, want %v", got[0].SubscribedAt, at)
	}
}
