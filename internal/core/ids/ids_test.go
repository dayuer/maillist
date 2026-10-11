package ids

import "testing"

func TestNewIsValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := New()
		if !Valid(id) || id != Normalize(id) {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		"64f1a2b3c4d5e6f7a8b9c0d1":  true,
		"64F1A2B3C4D5E6F7A8B9C0D1":  true,
		"64f1a2b3c4d5e6f7a8b9c0d":   false,
		"64f1a2b3c4d5e6f7a8b9c0d10": false,
		"../../admin":               false,
		"":                          false,
	} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v, want %v", s, !want, want)
		}
	}
}
