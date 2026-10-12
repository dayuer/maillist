package main

import (
	"strings"
	"testing"

	"nibwire/internal/core/ratectl"
	"nibwire/internal/mailer"
)

// check 的三行里，速率必须说清它是账号给的还是本进程的默认值：1/s 在这两种情况下的意思完全不同，
// 而读到配额之前的那 1/s 只是「还没读到」，不是账号的答案。
func TestTheCheckOutputSaysWhereTheRateCameFrom(t *testing.T) {
	known := describeRate(mailer.SendingRate{PerSecond: 450, Known: true})
	if !strings.Contains(known, "450/s") || !strings.Contains(known, "SES account") {
		t.Errorf("describeRate(known) = %q", known)
	}
	unread := describeRate(mailer.SendingRate{PerSecond: 14})
	if !strings.Contains(unread, "14/s") || !strings.Contains(unread, "no SES quota read") {
		t.Errorf("describeRate(unread) = %q", unread)
	}
	// 450 rather than 450.000000, and a fractional rate as itself.
	if got := describeRate(mailer.SendingRate{PerSecond: 0.5, Known: true}); !strings.Contains(got, "0.5/s") {
		t.Errorf("describeRate(fractional) = %q", got)
	}
}

// 剩余额度：读不到时是「未知」，SES 说没有上限时是「unlimited」，其余按千分位。
func TestTheCheckOutputReportsTheAccountsRemainingAllowance(t *testing.T) {
	cases := map[string]string{
		"a reading":          "199,000",
		"an unlimited quota": "unlimited",
		"nothing read yet":   "unknown (no SES quota read)",
		"a spent allowance":  "0",
	}
	values := map[string]mailer.SendingRate{
		"a reading":          {Known: true, Remaining24h: 199_000},
		"an unlimited quota": {Known: true, Remaining24h: ratectl.Unlimited},
		"nothing read yet":   {},
		"a spent allowance":  {Known: true},
	}
	for name, want := range cases {
		if got := describeRemaining(values[name]); got != want {
			t.Errorf("describeRemaining(%s) = %q, want %q", name, got, want)
		}
	}
}
