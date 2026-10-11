package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mailcheap/internal/jobs"
	"mailcheap/internal/store"
)

func newQueue(t *testing.T) *jobs.Queue {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return jobs.New(db)
}

// 到期的 job 被跑一次，然后标记完成。
func TestDueJobsRunOnce(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, jobs.KindSendCampaign, map[string]string{"campaignId": "x"}, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}

	ran := make(chan string, 4)
	runner := New(queue, func(_ context.Context, job jobs.Job) error {
		ran <- job.ID
		return nil
	}, 5*time.Millisecond, nil)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Run(runCtx) }()

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never ran")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A completed job is not leased again.
	if _, ok, err := queue.Lease(ctx, time.Now()); err != nil || ok {
		t.Fatalf("the job was leased again: %v %v", ok, err)
	}
}

// 还没到点的 job 不跑。
func TestFutureJobsWait(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, jobs.KindSendWelcome, nil, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{}, 4)
	runner := New(queue, func(context.Context, jobs.Job) error {
		ran <- struct{}{}
		return nil
	}, 5*time.Millisecond, nil)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(runCtx) }()

	select {
	case <-ran:
		t.Fatal("a future job ran")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done
}

// 失败的 job 走退避重试，而不是把循环打断。
func TestFailedJobIsRetriedAndTheLoopContinues(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, jobs.KindSendCampaign, nil, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	// A second, healthy job behind it proves the loop is not stuck on the first.
	good, err := queue.Enqueue(ctx, jobs.KindSendWelcome, nil, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu        sync.Mutex
		attempts  int
		completed bool
	)
	runner := New(queue, func(_ context.Context, job jobs.Job) error {
		mu.Lock()
		defer mu.Unlock()
		if job.ID == good {
			completed = true
			return nil
		}
		attempts++
		return errors.New("boom")
	}, 5*time.Millisecond, nil)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(runCtx) }()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		failed, done2 := attempts > 0, completed
		mu.Unlock()
		if failed && done2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("attempts = %d, completed = %v", attempts, done2)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// The failed job went back to pending with an attempt recorded.
	pending, err := queue.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, job := range pending {
		if job.Kind == jobs.KindSendCampaign && job.Attempts > 0 && job.LastError == "boom" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the failed job is not waiting to be retried: %+v", pending)
	}
}
