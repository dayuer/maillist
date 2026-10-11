package store

import (
	"context"
	"testing"
	"time"
)

// serve 的心跳：刚打过算在跑，过了窗口算不在，清掉立即不在。
func TestServeHeartbeat(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	// The store keeps milliseconds, so the expectations have to as well.
	now := time.Now().UTC().Truncate(time.Millisecond)

	if running, err := db.ServeIsRunning(ctx, now); err != nil || running {
		t.Fatalf("before any stamp: running = %v %v", running, err)
	}
	if err := db.TouchHeartbeat(ctx, ServeHeartbeat, now); err != nil {
		t.Fatal(err)
	}
	if running, err := db.ServeIsRunning(ctx, now); err != nil || !running {
		t.Fatalf("right after a stamp: running = %v %v", running, err)
	}
	if running, err := db.ServeIsRunning(ctx, now.Add(HeartbeatWindow-time.Second)); err != nil || !running {
		t.Fatalf("one second inside the window: running = %v %v", running, err)
	}
	if running, err := db.ServeIsRunning(ctx, now.Add(HeartbeatWindow)); err != nil || running {
		t.Fatalf("at the window's edge: running = %v %v", running, err)
	}

	// A second stamp moves it forward rather than adding a row.
	if err := db.TouchHeartbeat(ctx, ServeHeartbeat, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	seen, ok, err := db.Heartbeat(ctx, ServeHeartbeat)
	if err != nil || !ok || !seen.Equal(now.Add(time.Minute)) {
		t.Fatalf("heartbeat = %v %v %v", seen, ok, err)
	}

	if err := db.ClearHeartbeat(ctx, ServeHeartbeat); err != nil {
		t.Fatal(err)
	}
	if running, err := db.ServeIsRunning(ctx, now.Add(time.Minute)); err != nil || running {
		t.Fatalf("after clearing: running = %v %v", running, err)
	}
}
