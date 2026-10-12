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

// SEND_RATE_PER_SECOND 是运营方自己的上限，不设就是没有上限：速率跟随账号（D17）。
//
// It used to default to 10 and cap at 14, which was a guess about an account the deployment can read
// for itself. The parse is still stricter than upstream's Number.parseInt on purpose — "5.5" and " 7 "
// are refused rather than half-read — and a refused value falls back to no ceiling rather than to a
// number nobody asked for.
func TestTheSendRateCapIsOptional(t *testing.T) {
	for v, want := range map[string]int{
		"": 0, "0": 0, "-1": 0, "abc": 0, "5.5": 0, " 7 ": 0, "1": 1, "5000": 5000, "5001": 0,
	} {
		c, err := Load(with("SEND_RATE_PER_SECOND", v))
		if err != nil {
			t.Fatal(err)
		}
		if c.Limits.SendRatePerSecond != want {
			t.Errorf("SEND_RATE_PER_SECOND=%q → %d, want %d", v, c.Limits.SendRatePerSecond, want)
		}
	}
}

// 并发与 headroom：越界的值回退到默认并记日志，而不是让进程起不来。
func TestTheConcurrencyAndHeadroomSettings(t *testing.T) {
	c, err := Load(strong)
	if err != nil {
		t.Fatal(err)
	}
	if c.SendConcurrency != 16 || c.Limits.SendRateHeadroom != 0.9 {
		t.Fatalf("defaults = %d workers, headroom %v", c.SendConcurrency, c.Limits.SendRateHeadroom)
	}
	for v, want := range map[string]int{"0": 16, "257": 16, "abc": 16, "": 16, "1": 1, "256": 256} {
		c, err := Load(with("SEND_CONCURRENCY", v))
		if err != nil {
			t.Fatal(err)
		}
		if c.SendConcurrency != want {
			t.Errorf("SEND_CONCURRENCY=%q → %d, want %d", v, c.SendConcurrency, want)
		}
	}
	for v, want := range map[string]float64{
		"": 0.9, "0.05": 0.9, "1.5": 0.9, "abc": 0.9, "0.5": 0.5, "1": 1,
	} {
		c, err := Load(with("SEND_RATE_HEADROOM", v))
		if err != nil {
			t.Fatal(err)
		}
		if c.Limits.SendRateHeadroom != want {
			t.Errorf("SEND_RATE_HEADROOM=%q → %v, want %v", v, c.Limits.SendRateHeadroom, want)
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

// 发送开着却没有去处，启动就拒绝：默认路径不该悄悄构造真实 SES 客户端、把失败推到第一封信。
//
// A half-configured deployment is the one that reads as a bug later. SENDING_ENABLED=false (the
// default) starts whatever else is missing, so a fresh deployment is unaffected.
func TestSendingOnNeedsSomewhereToSend(t *testing.T) {
	env := with("SENDING_ENABLED", "true")
	if _, err := Load(env); err == nil {
		t.Fatal("SENDING_ENABLED=true with no SES_REGION, endpoint or fake was accepted")
	}
	for _, key := range []string{"SES_REGION", "SES_ENDPOINT", "MAILCHEAP_FAKE_SES_LOG"} {
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

// MAILCHEAP_FAKE_SES_DOMAINS is the stand-in identity list the end-to-end script drives the
// unverified branch with; commas and blanks are the operator's, not ours.
func TestFakeSESDomains(t *testing.T) {
	c, err := Load(with("MAILCHEAP_FAKE_SES_DOMAINS", " example.net , other.example,"))
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
