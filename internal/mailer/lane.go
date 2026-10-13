package mailer

import (
	"time"

	"nibwire/internal/core/model"
	"nibwire/internal/core/ratectl"
)

// lane is one channel's way out of the engine: the channel, the bucket that paces it and the
// controller that decides the bucket's rate (multi-channel design §6). Each lane claims its own
// messages and has its own dispatcher and workers (SendBatch), so a lane that is throttled, or
// waiting on a slow rate, holds only its own workers and never another channel's mail.
type lane struct {
	channel Channel
	bucket  *Bucket
	// control is nil when the engine was built without one (Config.RateControl): the bucket then keeps
	// the rate it was built with.
	control *ratectl.Controller
}

// name is the channel's name, which is what messages.channel holds.
func (l *lane) name() string { return l.channel.Name() }

// onThrottle is the channel saying "too fast": the rate halves, and the bucket — the only thing that
// enforces a rate — is told at once. Only this lane slows down: another channel's account said
// nothing.
func (l *lane) onThrottle() {
	if l.control == nil {
		return
	}
	now := time.Now().UTC()
	l.control.OnThrottle(now)
	FollowControl(l.control, l.bucket, now)
}

// onSuccess is a send the channel accepted. Enough of those and the rate creeps back toward the
// account's limit — one step per IncreaseEvery, and never past it — which is what keeps a backoff
// from being permanent.
func (l *lane) onSuccess() {
	if l.control == nil {
		return
	}
	now := time.Now().UTC()
	l.control.OnSuccess(now)
	FollowControl(l.control, l.bucket, now)
}

// ChannelLimit is a configured channel's own ceilings: channels.max_per_second and
// channels.max_per_day, 0 for none. The default channel's rate is SEND_RATE_PER_SECOND, which is
// already in Config.RateControl, and it has no max_per_day: the account's own 24-hour allowance is
// the ceiling there, and that is checked as a quota rather than as a budget.
type ChannelLimit struct {
	MaxPerSecond int
	MaxPerDay    int
}

// Lane is one lane as the app sees it: what serve needs to keep the lane's rate in step with its
// account (one QuotaPoller per lane) and what a command reads the quota through.
type Lane struct {
	Channel Channel
	// Control is nil when the engine follows no quota; there is nothing to poll for then.
	Control *ratectl.Controller
	Bucket  *Bucket
}

// newLanes builds one lane per channel in the registry, the default channel's first.
//
// The default lane is what the engine was before channels: Config.RateControl and SendRatePerSec.
// Every other lane follows its own account with the same headroom and its own max_per_second as the
// cap (design §6) — but only in an engine that follows quotas at all (RateControl set). Without one,
// each lane keeps a fixed rate: its max_per_second, or SendRatePerSec.
func newLanes(channels *Registry, cfg Config) (map[string]*lane, []*lane) {
	byName := map[string]*lane{}
	order := make([]*lane, 0, len(channels.All()))
	now := time.Now().UTC()
	for _, channel := range channels.All() {
		l := &lane{channel: channel}
		if channel.Name() == model.DefaultChannel {
			l.control = cfg.RateControl
			l.bucket = NewBucket(float64(cfg.SendRatePerSec))
		} else {
			limit := cfg.ChannelLimits[channel.Name()]
			rate := float64(cfg.SendRatePerSec)
			if limit.MaxPerSecond > 0 {
				rate = float64(limit.MaxPerSecond)
			}
			if cfg.RateControl != nil {
				l.control = ratectl.New(ratectl.Config{
					Headroom: cfg.Limits.SendRateHeadroom, Cap: limit.MaxPerSecond, Floor: 1,
				})
				rate = l.control.Rate(now)
			}
			l.bucket = NewBucket(rate)
		}
		byName[channel.Name()] = l
		order = append(order, l)
	}
	return byName, order
}

// laneFor is the lane of a channel name. An empty name is the default channel, as it is when a
// message is written (model.Message.Channel).
func (e *Engine) laneFor(channel string) (*lane, bool) {
	if channel == "" {
		channel = model.DefaultChannel
	}
	l, ok := e.lanes[channel]
	return l, ok
}

// defaultLane is the lane of the channel the SES_* settings describe.
func (e *Engine) defaultLane() *lane { return e.order[0] }

// Lanes lists every lane, the default channel's first.
func (e *Engine) Lanes() []Lane {
	out := make([]Lane, 0, len(e.order))
	for _, l := range e.order {
		out = append(out, Lane{Channel: l.channel, Control: l.control, Bucket: l.bucket})
	}
	return out
}

// SetQuota applies a reading of one channel's account to that channel's rate.
//
// It is what a poller does on each tick, and what a command does once before it drains its own
// queue: a CLI process has no poller, and a rate it never read is one email a second (D8-2). An
// unknown channel, or a lane with no controller, is left alone.
func (e *Engine) SetQuota(channel string, quota ratectl.Quota, now time.Time) {
	l, ok := e.laneFor(channel)
	if !ok || l.control == nil {
		return
	}
	l.control.SetQuota(quota, now)
	FollowControl(l.control, l.bucket, now)
}

// channelNames are the channels this engine has a lane for.
func (e *Engine) channelNames() []string {
	names := make([]string, 0, len(e.order))
	for _, l := range e.order {
		names = append(names, l.name())
	}
	return names
}

// laneConcurrency is how many of a lane's sends may be in flight at once: SEND_CONCURRENCY for the
// default channel, Config.ChannelConcurrency for the others. It is read when a batch starts, not
// when the engine is built, so a test that changes the configuration is heard.
func (e *Engine) laneConcurrency(l *lane) int {
	concurrency := e.cfg.SendConcurrency
	if l.name() != model.DefaultChannel && e.cfg.ChannelConcurrency > 0 {
		concurrency = e.cfg.ChannelConcurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return concurrency
}

// sendError is an error the channel returned from Send. It is the only kind the channel classifies:
// an error the engine raised itself — rendering, signing, the rate limiter — says nothing about the
// channel's account, and reading it with the provider's rules could pause a campaign or halve a rate
// for a reason that is not the provider's.
type sendError struct{ err error }

func (e *sendError) Error() string { return e.err.Error() }
func (e *sendError) Unwrap() error { return e.err }
