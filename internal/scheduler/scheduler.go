// Package scheduler turns due jobs into work. The jobs table is the queue (D2); this is the loop
// that leases from it and calls the right thing.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"nibwire/internal/jobs"
)

// Handler runs one job. Returning an error hands the job back to the queue with its backoff.
type Handler func(ctx context.Context, job jobs.Job) error

// Scheduler leases due jobs and hands them to a handler.
type Scheduler struct {
	queue  *jobs.Queue
	handle Handler
	period time.Duration
	log    *slog.Logger
}

// New returns a Scheduler that polls every period.
func New(queue *jobs.Queue, handle Handler, period time.Duration, log *slog.Logger) *Scheduler {
	if period <= 0 {
		period = 250 * time.Millisecond
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{queue: queue, handle: handle, period: period, log: log}
}

// Run leases and runs due jobs until ctx is cancelled.
//
// It waits on the queue's wake channel and on a ticker: an idle worker starts the moment something
// is enqueued rather than on the next tick, and the ticker is the fallback that also catches a job
// whose run_at has just passed (design §6.1).
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.period)
	defer ticker.Stop()
	for {
		if err := s.drain(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("scheduler", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.queue.Wake():
		case <-ticker.C:
		}
	}
}

// drain runs every job that is due right now. One bad payload never stops the loop: it is failed,
// which schedules the queue's backoff.
func (s *Scheduler) drain(ctx context.Context) error {
	for {
		job, ok, err := s.queue.Lease(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := s.handle(ctx, job); err != nil {
			s.log.Error("job failed", "job", job.ID, "kind", job.Kind, "error", err)
			if err := s.queue.Fail(ctx, job.ID, err, time.Now().UTC()); err != nil {
				return err
			}
			continue
		}
		if err := s.queue.Complete(ctx, job.ID); err != nil {
			return err
		}
	}
}
