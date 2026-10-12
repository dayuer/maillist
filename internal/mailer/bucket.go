package mailer

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrBucketClosed is what a Take returns once the bucket has been closed: the process is stopping, so
// the caller should stop asking.
var ErrBucketClosed = errors.New("the send rate limiter is closed")

// Bucket spaces sends at the process's rate, and the rate can change while it runs: it follows the
// account's quota (ratectl) and backs off when SES says it is too fast.
//
// It is a reservation rather than a ticker. Taking a token reserves the next free moment and sleeps
// until it: that works for any rate — including rates below one per second, where a ticker would have
// to fire less often than the clock's own resolution makes comfortable — and it lets SetRate take
// effect on the next reservation rather than on the next ticker rebuild.
//
// Capacity is one moment: no bursts are banked. A bucket that banked them would send a burst at the
// account's limit after an idle period, which is exactly when SES is least forgiving.
type Bucket struct {
	mu     sync.Mutex
	rate   float64
	next   time.Time
	closed chan struct{}
	once   sync.Once
}

// NewBucket returns a bucket that allows rate sends per second. A rate below one is treated as one:
// a deployment that may send less often than that is a deployment whose quota read failed, and one
// per second is already the conservative answer (ratectl decides the number).
func NewBucket(rate float64) *Bucket {
	if rate < 1 {
		rate = 1
	}
	return &Bucket{rate: rate, closed: make(chan struct{})}
}

// SetRate changes the rate. It takes effect on the next reservation, so a slower rate is honoured
// immediately and a faster one within one period.
func (b *Bucket) SetRate(perSecond float64) {
	if perSecond < 0 {
		perSecond = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rate = perSecond
}

// Rate is the rate in force right now.
func (b *Bucket) Rate() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rate
}

// Take blocks until this caller's slot arrives, the context ends, or the bucket closes.
func (b *Bucket) Take(ctx context.Context) error {
	b.mu.Lock()
	select {
	case <-b.closed:
		b.mu.Unlock()
		return ErrBucketClosed
	default:
	}
	now := time.Now()
	if b.next.Before(now) {
		b.next = now
	}
	wait := b.next.Sub(now)
	if b.rate > 0 {
		b.next = b.next.Add(time.Duration(float64(time.Second) / b.rate))
	} else {
		// A rate of zero means "do not send": the caller waits for a new rate or for the context.
		b.next = now.Add(time.Hour)
	}
	b.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-b.closed:
		return ErrBucketClosed
	}
}

// Close wakes everyone waiting and makes future takes fail. It is safe to call more than once.
func (b *Bucket) Close() {
	b.once.Do(func() { close(b.closed) })
}
