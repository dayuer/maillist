package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mailcheap/internal/store"
)

var testNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func newQueue(t *testing.T) *Queue {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func TestLeaseOnlyTakesDueJobs(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, KindSendCampaign, map[string]string{"campaignId": "x"}, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := queue.Lease(ctx, testNow); err != nil || ok {
		t.Fatalf("leased a future job: %v %v", ok, err)
	}
	job, ok, err := queue.Lease(ctx, testNow.Add(2*time.Hour))
	if err != nil || !ok {
		t.Fatalf("lease = %v %v", ok, err)
	}
	if job.Kind != KindSendCampaign || job.State != Running {
		t.Fatalf("job = %+v", job)
	}
	if job.LockedAt == nil || !job.LockedAt.Equal(testNow.Add(2*time.Hour)) {
		t.Fatalf("lockedAt = %v", job.LockedAt)
	}
	var payload map[string]string
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload["campaignId"] != "x" {
		t.Fatalf("payload = %s (%v)", job.Payload, err)
	}

	// A second lease finds nothing while the first is running.
	if _, ok, err := queue.Lease(ctx, testNow.Add(2*time.Hour)); err != nil || ok {
		t.Fatalf("second lease = %v %v", ok, err)
	}
}

// A job whose worker died is leaseable again after the lease expires.
func TestLeaseExpires(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, KindImport, nil, testNow); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := queue.Lease(ctx, testNow); err != nil || !ok {
		t.Fatalf("first lease = %v %v", ok, err)
	}
	if _, ok, _ := queue.Lease(ctx, testNow.Add(LeaseDuration-time.Second)); ok {
		t.Fatal("leased a running job inside its lease")
	}
	if _, ok, err := queue.Lease(ctx, testNow.Add(LeaseDuration+time.Second)); err != nil || !ok {
		t.Fatalf("expired lease was not reclaimable: %v %v", ok, err)
	}
}

// The fifth failure is final: 1s, 5s, 30s, 2min, then dead.
func TestRetryBackoffEndsDead(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	id, err := queue.Enqueue(ctx, KindSendCampaign, nil, testNow)
	if err != nil {
		t.Fatal(err)
	}
	for attempt, delay := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute} {
		at := testNow.Add(time.Duration(attempt) * time.Hour)
		job, ok, err := queue.Lease(ctx, at)
		if err != nil || !ok {
			t.Fatalf("lease %d = %v %v", attempt, ok, err)
		}
		if err := queue.Fail(ctx, job.ID, errors.New("boom"), at); err != nil {
			t.Fatal(err)
		}
		got, err := queue.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != Pending {
			t.Fatalf("attempt %d: state = %s", attempt, got.State)
		}
		if want := at.Add(delay); !got.RunAt.Equal(want) {
			t.Fatalf("attempt %d: runAt = %s, want %s", attempt, got.RunAt, want)
		}
		if got.LastError != "boom" {
			t.Fatalf("attempt %d: lastError = %q", attempt, got.LastError)
		}
	}
	at := testNow.Add(4 * time.Hour)
	job, ok, err := queue.Lease(ctx, at)
	if err != nil || !ok {
		t.Fatalf("fifth lease = %v %v", ok, err)
	}
	if err := queue.Fail(ctx, job.ID, errors.New("boom"), at); err != nil {
		t.Fatal(err)
	}
	got, err := queue.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Dead || got.Attempts != 5 {
		t.Fatalf("state = %s, attempts = %d, want dead after 5", got.State, got.Attempts)
	}
	if _, ok, err := queue.Lease(ctx, at.Add(24*time.Hour)); err != nil || ok {
		t.Fatalf("a dead job was leased: %v %v", ok, err)
	}
}

// Completing a job settles it for good.
func TestCompleteEndsAJob(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, KindSendBatch, nil, testNow); err != nil {
		t.Fatal(err)
	}
	job, ok, err := queue.Lease(ctx, testNow)
	if err != nil || !ok {
		t.Fatalf("lease = %v %v", ok, err)
	}
	if err := queue.Complete(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	got, err := queue.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Done || got.LockedAt != nil {
		t.Fatalf("job = %+v", got)
	}
	if _, ok, _ := queue.Lease(ctx, testNow.Add(time.Hour)); ok {
		t.Fatal("a done job was leased")
	}
}

// An idle worker is woken the moment something is enqueued, without waiting for the poll interval.
func TestEnqueueWakesAnIdleWorker(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	if _, err := queue.Enqueue(ctx, KindSendCampaign, nil, testNow); err != nil {
		t.Fatal(err)
	}
	select {
	case <-queue.Wake():
	case <-time.After(time.Second):
		t.Fatal("no wake signal")
	}
}

func TestGetMissingJob(t *testing.T) {
	queue := newQueue(t)
	if _, err := queue.Get(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPendingListsWaitingJobs(t *testing.T) {
	queue := newQueue(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := queue.Enqueue(ctx, KindImport, nil, testNow.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := queue.Pending(ctx, 10)
	if err != nil || len(pending) != 3 {
		t.Fatalf("pending = %d %v", len(pending), err)
	}
	if !pending[0].RunAt.Before(pending[2].RunAt) {
		t.Fatalf("pending is not oldest first: %+v", pending)
	}
}
