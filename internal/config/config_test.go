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
		c.DBPath != "/data/nibwire.db" || c.MCPHTTPAddr != "127.0.0.1:8081" || c.Limits.MaxSendsPerDay != 50_000 {
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

// 发送开着却没有去处，启动就拒绝：默认路径不该悄悄构造真实 SES 客户端、把失败推到第一封信。
//
// A half-configured deployment is the one that reads as a bug later. SENDING_ENABLED=false (the
// default) starts whatever else is missing, so a fresh deployment is unaffected.
func TestSendingOnNeedsSomewhereToSend(t *testing.T) {
	env := with("SENDING_ENABLED", "true")
	if _, err := Load(env); err == nil {
		t.Fatal("SENDING_ENABLED=true with no SES_REGION, endpoint or fake was accepted")
	}
	for _, key := range []string{"SES_REGION", "SES_ENDPOINT", "NIBWIRE_FAKE_SES_LOG"} {
		env := with("SENDING_ENABLED", "true")
		env[key] = "something"
		if _, err := Load(env); err != nil {
			t.Errorf("%s set is not enough: %v", key, err)
		}
	}
	// Sending off: the same env is fine.
	if _, err := Load(strong); err != nil {
		t.Fatalf("sending off was refused: %v", err)
	}
}

// NIBWIRE_FAKE_SES_DOMAINS is the stand-in identity list the end-to-end script drives the
// unverified branch with; commas and blanks are the operator's, not ours.
func TestFakeSESDomains(t *testing.T) {
	c, err := Load(with("NIBWIRE_FAKE_SES_DOMAINS", " example.net , other.example,"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.FakeSESDomains) != 2 || c.FakeSESDomains[0] != "example.net" || c.FakeSESDomains[1] != "other.example" {
		t.Fatalf("FakeSESDomains = %#v", c.FakeSESDomains)
	}
	empty, _ := Load(strong)
	if empty.FakeSESDomains != nil {
		t.Fatalf("FakeSESDomains = %#v, want nil", empty.FakeSESDomains)
	}
}
