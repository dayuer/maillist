package mailer

import (
	"context"
	"time"
)

// Bucket spaces sends at a fixed rate. Upstream kept a nextSendAt variable and slept until it
// (lib/sending.js waitForSendSlot); a ticker is the same shape and is safe for concurrent callers,
// so the rate is a process-wide cap rather than a per-batch one.
type Bucket struct {
	tokens chan struct{}
	stop   func()
}

// NewBucket allows rate sends per second. A rate below 1 is treated as 1.
func NewBucket(rate int) *Bucket {
	if rate < 1 {
		rate = 1
	}
	period := time.Second / time.Duration(rate)
	bucket := &Bucket{tokens: make(chan struct{}, 1)}
	ticker := time.NewTicker(period)
	done := make(chan struct{})
	bucket.stop = func() {
		ticker.Stop()
		close(done)
	}
	go func() {
		for {
			select {
			case <-ticker.C:
				select {
				case bucket.tokens <- struct{}{}:
				default: // a slot is already waiting; nobody sends faster than the ticker
				}
			case <-done:
				return
			}
		}
	}()
	return bucket
}

// Take blocks until the next slot is free, or ctx is done.
func (b *Bucket) Take(ctx context.Context) error {
	select {
	case <-b.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops the ticker. It is safe to call once.
func (b *Bucket) Close() { b.stop() }
