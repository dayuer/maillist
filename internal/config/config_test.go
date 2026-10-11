package config

import (
	"strings"
	"testing"
)

var strong = map[string]string{"SESSION_SECRET": strings.Repeat("s", 32)}

func with(key, value string) map[string]string {
	env := map[string]string{key: value}
	for k, v := range strong {
		if k != key {
			env[k] = v
		}
	}
	return env
}

func TestSessionSecretNeeds32Characters(t *testing.T) {
	if _, err := Load(strong); err != nil {
		t.Fatal(err)
	}
	_, err := Load(with("SESSION_SECRET", strings.Repeat("s", 31)))
	if err == nil || !strings.Contains(err.Error(), "SESSION_SECRET must be at least 32") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(map[string]string{}); err == nil {
		t.Fatal("missing secret counts as too short")
	}
}

// Intentional tightening, not a straight port of tests/config.test.mjs: upstream readSendRate uses
// Number.parseInt, so "5.5" → 5 and " 7 " → 7; Go's strconv.Atoi rejects both and falls back to 10.
// Being stricter here (never a silent half-read value) is deliberate.
func TestSendRateOutsideOneToFourteenFallsBackToTen(t *testing.T) {
	for v, want := range map[string]int{"": 10, "1": 1, "14": 14, "0": 10, "15": 10, "abc": 10, "5.5": 10, " 7 ": 10} {
		c, err := Load(with("SEND_RATE_PER_SECOND", v))
		if err != nil {
			t.Fatal(err)
		}
		if c.Limits.SendRatePerSecond != want {
			t.Errorf("SEND_RATE_PER_SECOND=%q → %d, want %d", v, c.Limits.SendRatePerSecond, want)
		}
	}
}

func TestDefaultsAreSafe(t *testing.T) {
	c, _ := Load(with("APP_URL", "https://mail.example.com/"))
	if c.SendingOn || c.SendApproval != "auto" || c.AppURL != "https://mail.example.com" ||
		c.DBPath != "/data/mailcheap.db" || c.MCPHTTPAddr != "127.0.0.1:8081" || c.Limits.MaxSendsPerDay != 50_000 {
		t.Fatalf("unexpected defaults %+v", c)
	}
}

func TestSendApprovalMustBeAutoOrManual(t *testing.T) {
	if _, err := Load(with("SEND_APPROVAL", "sometimes")); err == nil {
		t.Fatal("bad SEND_APPROVAL accepted")
	}
}

// tests/sending.test.mjs "campaigns pause above the bounce or complaint limit once 100 are sent"
// opens by asserting the upstream LIMITS values; they live here now.
func TestSendingLimitsMatchUpstream(t *testing.T) {
	c, err := Load(strong)
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits.BounceRate != 0.02 || c.Limits.ComplaintRate != 0.0008 {
		t.Fatalf("limits = %+v", c.Limits)
	}
}
