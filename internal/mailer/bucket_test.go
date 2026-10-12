package mailer

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 速率可以在运行中改：配额轮询与 AIMD 都要在发送过程中调它。
func TestTheRateCanChangeWhileSending(t *testing.T) {
	bucket := NewBucket(1000)
	t.Cleanup(bucket.Close)

	// 慢下来：之后一个周期内就该按新速率放行。
	bucket.SetRate(50)
	start := time.Now()
	tokens := 0
	for time.Since(start) < 300*time.Millisecond {
		if err := bucket.Take(context.Background()); err != nil {
			t.Fatal(err)
		}
		tokens++
	}
	// 300 ms、50/s：大约 15 个，给调度留一点余量。
	if tokens > 25 || tokens < 8 {
		t.Fatalf("%d tokens in 300 ms at 50/s", tokens)
	}
}

// 一个周期内不超速率：容量 1，不攒突发。
func TestTheBucketDoesNotBankBursts(t *testing.T) {
	bucket := NewBucket(20)
	t.Cleanup(bucket.Close)

	// 空闲一秒之后也不该有余量：立刻连取 40 次，用掉的时间应接近 40 / 20 = 2 秒。
	start := time.Now()
	for i := 0; i < 40; i++ {
		if err := bucket.Take(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
		t.Fatalf("40 takes at 20/s took %s: the bucket banked a burst", elapsed)
	}
}

// 小数速率：比 1/s 还慢的账号也要能发，只是很慢。
func TestAFractionalRateIsSlowButReal(t *testing.T) {
	bucket := NewBucket(1)
	bucket.SetRate(0.5)
	t.Cleanup(bucket.Close)

	start := time.Now()
	if err := bucket.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bucket.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bucket.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第一个立刻，第二个 2 秒，第三个 4 秒——这里只断言「明显慢于 1/s」。
	if elapsed := time.Since(start); elapsed < 3500*time.Millisecond {
		t.Fatalf("three takes at 0.5/s took %s", elapsed)
	}
}

// 并发取令牌与反复改速率在 -race 下无竞争，且总共取到的令牌数不超速率 × 时间。
func TestConcurrentTakesWithARateThatKeepsChanging(t *testing.T) {
	bucket := NewBucket(200)
	t.Cleanup(bucket.Close)

	const workers = 32
	var taken atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for ctx.Err() == nil {
				if err := bucket.Take(ctx); err != nil {
					return
				}
				taken.Add(1)
			}
		}(i)
	}
	// 一边取一边改：速率在 100 与 200 之间来回。
	changer := make(chan struct{})
	go func() {
		defer close(changer)
		for ctx.Err() == nil {
			bucket.SetRate(100)
			time.Sleep(20 * time.Millisecond)
			bucket.SetRate(200)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	wg.Wait()
	<-changer

	// 上限取最宽松的 200/s × 0.5 s = 100，再给一点调度余量。
	if got := taken.Load(); got > 120 {
		t.Fatalf("%d tokens in half a second while the rate moved between 100 and 200", got)
	}
	if got := taken.Load(); got < 10 {
		t.Fatalf("only %d tokens: the bucket is not letting anything through", got)
	}
}

// 关掉之后等在里面的调用返回，而不是永远等着。
func TestClosingWakesUpAWaiter(t *testing.T) {
	bucket := NewBucket(1)
	// 先把下一个槽位推到未来。
	if err := bucket.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- bucket.Take(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	bucket.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a take after close returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter was not woken by Close")
	}
}
