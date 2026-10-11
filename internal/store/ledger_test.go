package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Two reservations of 60% each: exactly one fits.
func TestReserveIsAtomic(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const day = "2026-10-11"

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	for slot := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[slot], errs[slot] = db.Reserve(ctx, day, "", 6000, 10000, 0)
		}()
	}
	wg.Wait()
	for slot, err := range errs {
		if err != nil {
			t.Fatalf("reservation %d: %v", slot, err)
		}
	}
	if !results[0] && !results[1] {
		t.Fatal("neither reservation succeeded")
	}
	if results[0] && results[1] {
		t.Fatal("both reservations succeeded")
	}

	reserved, err := db.Reserved(ctx, day, "")
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 6000 {
		t.Fatalf("reserved = %d, want 6000", reserved)
	}
}

func TestReserveHonoursTheCredentialBudget(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if ok, err := db.Reserve(ctx, "2026-10-11", "cred-1", 50, 10000, 100); err != nil || !ok {
		t.Fatalf("first reserve = %v %v", ok, err)
	}
	if ok, err := db.Reserve(ctx, "2026-10-11", "cred-1", 60, 10000, 100); err != nil || ok {
		t.Fatalf("second reserve = %v %v, want false", ok, err)
	}
	// The global pool is separate: another credential still fits.
	if ok, err := db.Reserve(ctx, "2026-10-11", "cred-2", 60, 10000, 100); err != nil || !ok {
		t.Fatalf("other credential = %v %v", ok, err)
	}
	// A refused reservation writes nothing.
	if reserved, err := db.Reserved(ctx, "2026-10-11", "cred-1"); err != nil || reserved != 50 {
		t.Fatalf("cred-1 reserved = %d %v", reserved, err)
	}
}

// A new UTC day starts empty.
func TestReserveResetsAcrossDays(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if ok, _ := db.Reserve(ctx, "2026-10-11", "", 9000, 10000, 0); !ok {
		t.Fatal("first day")
	}
	if ok, _ := db.Reserve(ctx, "2026-10-12", "", 9000, 10000, 0); !ok {
		t.Fatal("next day must start empty")
	}
}

// A whole campaign is refused when it does not fit, never partially sent (D15).
func TestReserveRefusesTheWholeBatch(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if ok, _ := db.Reserve(ctx, "2026-10-11", "", 9999, 10000, 0); !ok {
		t.Fatal("seeding reservation")
	}
	if ok, err := db.Reserve(ctx, "2026-10-11", "", 2, 10000, 0); err != nil || ok {
		t.Fatalf("a 2-message batch over the line = %v %v, want false", ok, err)
	}
	if reserved, err := db.Reserved(ctx, "2026-10-11", ""); err != nil || reserved != 9999 {
		t.Fatalf("reserved = %d %v, want 9999", reserved, err)
	}
}

// A zero-message reservation is free and never blocks.
func TestReserveZeroNeedsNothing(t *testing.T) {
	db := openTemp(t)
	if ok, err := db.Reserve(context.Background(), "2026-10-11", "", 0, 0, 0); err != nil || !ok {
		t.Fatalf("zero = %v %v", ok, err)
	}
}

// The ledger is keyed by UTC day, whatever local time the process runs in.
func TestDayIsUTC(t *testing.T) {
	if got := Day(time.Date(2026, 10, 11, 23, 30, 0, 0, time.UTC)); got != "2026-10-11" {
		t.Fatalf("Day = %q", got)
	}
}
