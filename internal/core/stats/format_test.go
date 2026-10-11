package stats

import "testing"

// Translated from tests/overview.test.mjs:
//
//	test("changes need something to compare with; rates change by their difference, shown in %", …)
//
// The formatting half. Every expected string is upstream's, character for character — the minus sign
// is U+2212 and the thousands separator is a comma, and a caller that "cleans up" either one breaks
// the contract with the tests and with the numbers an operator reads.
func TestFormattingChanges(t *testing.T) {
	change := func(value float64) *float64 { return &value }

	if got := FormatChange(change(8.4), 0); got != "+8%" {
		t.Errorf("FormatChange(8.4) = %q, want +8%%", got)
	}
	if got := FormatChange(CompareRate(391, 1000, 418, 1000), 1); got != "−2.7%" {
		t.Errorf("FormatChange(compareRate(391,1000,418,1000)) = %q, want −2.7%%", got)
	}
	if got := FormatChange(change(-0.24), 1); got != "−0.2%" {
		t.Errorf("FormatChange(-0.24, 1) = %q, want −0.2%%", got)
	}
	if got := FormatChange(change(0.004), 1); got != "0.0%" {
		t.Errorf("FormatChange(0.004, 1) = %q, want 0.0%%", got)
	}
	if got := FormatChange(nil, 0); got != "" {
		t.Errorf("FormatChange(nil) = %q, want the empty string", got)
	}
}

func TestFormattingSignedCounts(t *testing.T) {
	if got := FormatSigned(1204); got != "+1,204" {
		t.Errorf("FormatSigned(1204) = %q, want +1,204", got)
	}
	if got := FormatSigned(-32); got != "−32" {
		t.Errorf("FormatSigned(-32) = %q, want −32", got)
	}
	if got := FormatSigned(0); got != "0" {
		t.Errorf("FormatSigned(0) = %q, want 0", got)
	}
	if got := FormatNumber(1000000); got != "1,000,000" {
		t.Errorf("FormatNumber(1000000) = %q", got)
	}
}

func TestFormattingHoursAndRates(t *testing.T) {
	if got := FormatHour(0); got != "12 AM" {
		t.Errorf("FormatHour(0) = %q, want 12 AM", got)
	}
	if got := FormatHour(14); got != "2 PM" {
		t.Errorf("FormatHour(14) = %q, want 2 PM", got)
	}
	if got := FormatHour(12); got != "12 PM" {
		t.Errorf("FormatHour(12) = %q, want 12 PM", got)
	}
	if got := FormatRate(391, 1000, 1); got != "39.1%" {
		t.Errorf("FormatRate(391, 1000) = %q, want 39.1%%", got)
	}
	if got := FormatRate(1, 0, 1); got != "" {
		t.Errorf("FormatRate(1, 0) = %q, want the empty string", got)
	}
}
