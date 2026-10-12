package ratectl

import (
	"testing"
	"time"
)

// 夹具：一次 500/s、24 小时 20 万、已发 1000 的配额，默认配置（headroom 0.9、下限 1、每 30 s 升 10%）。
var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// closeTo compares two rates: 225 × 1.1 is 247.50000000000003, and a rate is not a currency.
func closeTo(got, want float64) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 0.001
}

func quota() Quota {
	return Quota{MaxSendRate: 500, Max24HourSend: 200_000, SentLast24Hours: 1_000, ReadAt: t0}
}

func defaultConfig() Config {
	return Config{Headroom: 0.9, Floor: 1, IncreaseEvery: 30 * time.Second, IncreaseFactor: 1.1}
}

// 从没读到配额时是最保守的速率：1/s。按 1/s 发只是慢，按 450/s 发进一个只允许 14/s 的账号会被连续限流。
func TestWithoutAQuotaTheRateIsOnePerSecond(t *testing.T) {
	c := New(defaultConfig())
	if got := c.Rate(t0); got != 1 {
		t.Fatalf("rate = %v, want 1", got)
	}
	if got := c.Remaining24h(); got != 0 {
		t.Errorf("remaining = %d, want 0 before any quota is known", got)
	}
}

// target = MaxSendRate × headroom。
func TestTheTargetFollowsTheQuotaWithHeadroom(t *testing.T) {
	c := New(defaultConfig())
	c.SetQuota(quota(), t0)
	if got := c.Rate(t0); got != 450 {
		t.Fatalf("rate = %v, want 450 (500 × 0.9)", got)
	}
}

// 设了上限时取上限。
func TestACapWinsWhenItIsLower(t *testing.T) {
	cfg := defaultConfig()
	cfg.Cap = 100
	c := New(cfg)
	c.SetQuota(quota(), t0)
	if got := c.Rate(t0); got != 100 {
		t.Fatalf("rate = %v, want the cap", got)
	}

	// 上限比目标高时不参与：还是 450。
	cfg.Cap = 1000
	c = New(cfg)
	c.SetQuota(quota(), t0)
	if got := c.Rate(t0); got != 450 {
		t.Fatalf("rate = %v, want 450", got)
	}
}

// 限流减半，连续限流不低过下限。
func TestThrottlingHalvesAndNeverGoesBelowTheFloor(t *testing.T) {
	c := New(defaultConfig())
	c.SetQuota(quota(), t0)

	c.OnThrottle(t0)
	if got := c.Rate(t0); got != 225 {
		t.Fatalf("rate = %v, want 225", got)
	}
	at := t0
	for i := 0; i < 20; i++ {
		at = at.Add(time.Second)
		c.OnThrottle(at)
	}
	if got := c.Rate(at); got != 1 {
		t.Fatalf("rate after twenty throttles = %v, want the floor", got)
	}
}

// 三十秒没有限流回升 10%，回到 target 就停在那里。
func TestTheRateCreepsBackUpEveryThirtySeconds(t *testing.T) {
	c := New(defaultConfig())
	c.SetQuota(quota(), t0)
	c.OnThrottle(t0) // 450 → 225

	// 不到三十秒：不动。
	c.OnSuccess(t0.Add(29 * time.Second))
	if got := c.Rate(t0.Add(29 * time.Second)); got != 225 {
		t.Fatalf("rate = %v before the interval was up", got)
	}
	// 到一个间隔：+10%。
	c.OnSuccess(t0.Add(30 * time.Second))
	if got := c.Rate(t0.Add(30 * time.Second)); !closeTo(got, 247.5) {
		t.Fatalf("rate = %v, want 247.5", got)
	}
	// 过去很久没发送、也没限流：按间隔补上，但不超 target。
	c.OnSuccess(t0.Add(10 * time.Minute))
	if got := c.Rate(t0.Add(10 * time.Minute)); got != 450 {
		t.Fatalf("rate = %v, want the target at most", got)
	}
}

// 配额降低时立即生效：SES 说账号只有 14/s 就该立刻按 14/s 发。
func TestALowerQuotaTakesEffectImmediately(t *testing.T) {
	c := New(defaultConfig())
	c.SetQuota(quota(), t0)
	if got := c.Rate(t0); got != 450 {
		t.Fatalf("rate = %v", got)
	}
	lowered := quota()
	lowered.MaxSendRate = 14
	c.SetQuota(lowered, t0.Add(time.Minute))
	if got := c.Rate(t0.Add(time.Minute)); !closeTo(got, 12.6) {
		t.Fatalf("rate = %v, want 14 × 0.9", got)
	}
}

// 24 小时剩余配额，且不为负。
func TestRemainingIsWhatIsLeftOfTheDay(t *testing.T) {
	c := New(defaultConfig())
	c.SetQuota(quota(), t0)
	if got := c.Remaining24h(); got != 199_000 {
		t.Fatalf("remaining = %d, want 199000", got)
	}
	spent := quota()
	spent.SentLast24Hours = 200_001
	c.SetQuota(spent, t0)
	if got := c.Remaining24h(); got != 0 {
		t.Fatalf("remaining = %d, want 0 rather than a negative number", got)
	}
}
