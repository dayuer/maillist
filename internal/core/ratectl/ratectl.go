// Package ratectl decides how fast this deployment may send.
//
// The ceiling is not ours: it is the SES account's, and it moves — a new account
// allows a handful per second, an account that asked for an increase allows hundreds, and the same
// account has a 24-hour quota that a big send can exhaust. So the rate follows the account's own
// numbers (design §19, D17) instead of a configured guess.
//
// It is pure logic: time is passed in, nothing here reads a clock or a socket. What enforces the
// rate is mailer.Bucket; what reads the quota is mailer.QuotaPoller.
//
// Pure is not the same as single-threaded: the quota poller writes the reading from its own
// goroutine while the send workers report throttles and successes and read the rate, so the state
// below is guarded. The values are a handful of floats; the lock is not where the time goes.
package ratectl

import (
	"sync"
	"time"
)

// Quota is what SES says this account may do, as of ReadAt.
type Quota struct {
	// MaxSendRate is sends per second the account is allowed.
	MaxSendRate float64
	// Max24HourSend is the account's 24-hour allowance.
	Max24HourSend float64
	// SentLast24Hours is how much of it SES has already counted.
	SentLast24Hours float64
	// ReadAt is when this was read: a quota is a fact about a moment.
	ReadAt time.Time
}

// Config tunes how the rate follows the quota.
type Config struct {
	// Headroom is the fraction of MaxSendRate to aim at. Sending at exactly the account's limit means
	// every rounding, every other client of the same account and every timing wobble lands on a
	// throttle; a tenth of it is cheap insurance.
	Headroom float64
	// Cap is a ceiling of our own, 0 for none. It is what `SEND_RATE_PER_SECOND` sets.
	Cap int
	// Floor is the slowest the rate may go, and the rate used before any quota is known.
	Floor int
	// IncreaseEvery is how long the rate must go unthrottled before it creeps back up.
	IncreaseEvery time.Duration
	// IncreaseFactor is how much it creeps by. Additive decrease, multiplicative increase: that is
	// AIMD, and it is what makes the rate stable rather than oscillating against the account's limit.
	IncreaseFactor float64
}

// Controller is the state: the current rate, the target it is aiming at, and when it last moved.
type Controller struct {
	mu  sync.Mutex
	cfg Config

	target float64
	rate   float64
	quota  Quota
	known  bool

	// backedOff is set by a throttle and cleared when the rate reaches the target again. While it is
	// set the rate creeps up; while it is clear the rate simply is the target.
	backedOff bool
	// lastIncrease is when the creep last moved, so it moves once per interval rather than once per
	// call.
	lastIncrease time.Time
}

// New returns a controller that has not read a quota yet: it will send at the floor until one
// arrives.
func New(cfg Config) *Controller {
	if cfg.Headroom <= 0 || cfg.Headroom > 1 {
		cfg.Headroom = 0.9
	}
	if cfg.Floor < 1 {
		cfg.Floor = 1
	}
	if cfg.IncreaseEvery <= 0 {
		cfg.IncreaseEvery = 30 * time.Second
	}
	if cfg.IncreaseFactor <= 1 {
		cfg.IncreaseFactor = 1.1
	}
	return &Controller{cfg: cfg, rate: float64(cfg.Floor)}
}

// SetQuota takes a fresh reading from SES.
//
// A lower target takes effect at once: if the account's rate was reduced, continuing at the old one
// is exactly the throttling this is here to avoid. A higher target does not: when the rate has been
// backed off, it creeps back up on its own, which is what keeps a burst from slamming into the limit
// again.
func (c *Controller) SetQuota(q Quota, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quota, c.known = q, true
	c.target = q.MaxSendRate * c.cfg.Headroom
	if c.cfg.Cap > 0 && float64(c.cfg.Cap) < c.target {
		c.target = float64(c.cfg.Cap)
	}
	if c.target < float64(c.cfg.Floor) {
		c.target = float64(c.cfg.Floor)
	}
	if !c.backedOff {
		c.rate = c.target
		return
	}
	if c.rate > c.target {
		c.rate = c.target
	}
}

// OnThrottle is SES saying "too fast": the rate halves, down to the floor.
func (c *Controller) OnThrottle(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backedOff = true
	c.rate = c.rate / 2
	if c.rate < float64(c.cfg.Floor) {
		c.rate = float64(c.cfg.Floor)
	}
	if c.lastIncrease.IsZero() {
		c.lastIncrease = now
	}
}

// OnSuccess is a send that went through. Enough of those in a row and the rate creeps back up toward
// the target: one step per IncreaseEvery, and never past it.
//
// The clock is what drives the creep, not the count of successes — a deployment that has stopped
// sending has learned nothing about the account's limit.
func (c *Controller) OnSuccess(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.backedOff {
		return
	}
	if c.lastIncrease.IsZero() {
		c.lastIncrease = now
		return
	}
	elapsed := now.Sub(c.lastIncrease)
	if elapsed < c.cfg.IncreaseEvery {
		return
	}
	steps := float64(elapsed / c.cfg.IncreaseEvery)
	c.lastIncrease = c.lastIncrease.Add(time.Duration(steps) * c.cfg.IncreaseEvery)
	for i := 0; i < int(steps); i++ {
		c.rate *= c.cfg.IncreaseFactor
		if c.rate >= c.target {
			c.rate = c.target
			c.backedOff = false
			return
		}
	}
}

// Rate is the current ceiling, in sends per second.
func (c *Controller) Rate(time.Time) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rate
}

// Unlimited is what Remaining24h answers for an account SES says has no 24-hour limit: the SDK's
// documentation gives -1 that meaning, and an account like that has no ceiling to respect. Big enough
// that min() against anything real picks the real one.
const Unlimited = 1 << 40

// Remaining24h is how many sends the account has left in its 24-hour window. It is 0 before any quota
// is known: an unknown allowance is not an invitation to spend it.
func (c *Controller) Remaining24h() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.known {
		return 0
	}
	if c.quota.Max24HourSend < 0 {
		// -1 means unlimited (types.SendQuota.Max24HourSend). Read as a number it would leave this
		// negative, and the largest accounts would be refused every send.
		return Unlimited
	}
	remaining := c.quota.Max24HourSend - c.quota.SentLast24Hours
	if remaining < 0 {
		return 0
	}
	return int(remaining)
}

// Quota is the last reading, and whether there has been one.
func (c *Controller) Quota() (Quota, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quota, c.known
}
