package mailer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nibwire/internal/alerts"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/store"
)

// flakyQuota answers the first N times with an error, then the quota.
type flakyQuota struct {
	mu     sync.Mutex
	fails  int
	reads  int
	quota  ratectl.Quota
	broken bool
}

func (f *flakyQuota) Quota(context.Context) (ratectl.Quota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.broken || f.fails > 0 {
		if f.fails > 0 {
			f.fails--
		}
		return ratectl.Quota{}, errors.New("ses is unavailable")
	}
	return f.quota, nil
}

// 读不到配额时沿用上一次的值：上一次的数字仍然是手上最好的答案。
func TestAFailedReadKeepsTheLastQuota(t *testing.T) {
	reader := &flakyQuota{quota: ratectl.Quota{MaxSendRate: 500, Max24HourSend: 1000, SentLast24Hours: 10}}
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	bucket := NewBucket(1)
	t.Cleanup(bucket.Close)
	poller := &QuotaPoller{Reader: reader, Control: control, Bucket: bucket, Log: testLoggerForQuota()}

	poller.pollOnce(context.Background(), testLoggerForQuota())
	if got := control.Rate(time.Now()); got != 450 {
		t.Fatalf("rate after a good read = %v", got)
	}
	reader.broken = true
	poller.pollOnce(context.Background(), testLoggerForQuota())
	if got := control.Rate(time.Now()); got != 450 {
		t.Fatalf("rate after a failed read = %v, want the last known one", got)
	}
	if got := bucket.Rate(); got != 450 {
		t.Fatalf("bucket rate = %v, want it kept too", got)
	}
}

// 从没读到配额时速率是 1/s，而且只告警一次（冷却之内不再发）。
func TestNeverHavingReadAQuotaWarnsOnce(t *testing.T) {
	reader := &flakyQuota{broken: true}
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	bucket := NewBucket(1)
	t.Cleanup(bucket.Close)

	// 告警要走到「抢冷却」那一步才看得见，所以这里配一个能发信的 notifier：发信人是假发送器。
	ctx := context.Background()
	db := newQuotaTestStore(t)
	sender := NewFake()
	notifier := &alerts.Notifier{
		Store: db, Sender: sender, From: "alerts@example.net", To: "admin@example.net",
		AppURL: "https://mail.example.com", SendingOn: true, Log: testLoggerForQuota(),
	}
	poller := &QuotaPoller{
		Reader: reader, Control: control, Bucket: bucket, Alerts: notifier, Log: testLoggerForQuota(),
	}
	for i := 0; i < 3; i++ {
		poller.pollOnce(ctx, testLoggerForQuota())
	}
	if got := control.Rate(time.Now()); got != 1 {
		t.Fatalf("rate without a quota = %v, want 1", got)
	}
	if got := bucket.Rate(); got != 1 {
		t.Fatalf("bucket rate = %v, want 1", got)
	}
	// 三次失败、一封邮件、一个 key：冷却之内不会重复。
	rows, err := db.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "ses-quota-unreadable" {
		t.Fatalf("alert rows = %+v", rows)
	}
	if got := sender.SentCount(); got != 1 {
		t.Fatalf("%d alerts were emailed, want 1", got)
	}
}

// 其他渠道的配额读不到时，告警带上渠道名，不与 default 的告警共用冷却。
func TestAChannelsUnreadableQuotaAlertsUnderItsOwnName(t *testing.T) {
	ctx := context.Background()
	db := newQuotaTestStore(t)
	notifier := &alerts.Notifier{
		Store: db, Sender: NewFake(), From: "alerts@example.net", To: "admin@example.net",
		AppURL: "https://mail.example.com", SendingOn: true, Log: testLoggerForQuota(),
	}
	for _, channel := range []string{"", "aws2"} {
		bucket := NewBucket(1)
		t.Cleanup(bucket.Close)
		poller := &QuotaPoller{
			Channel: channel, Reader: &flakyQuota{broken: true},
			Control: ratectl.New(ratectl.Config{Headroom: 0.9}), Bucket: bucket, Alerts: notifier,
		}
		poller.pollOnce(ctx, testLoggerForQuota())
	}
	rows, err := db.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, row := range rows {
		keys[row.Key] = true
	}
	if len(rows) != 2 || !keys["ses-quota-unreadable"] || !keys["ses-quota-unreadable-aws2"] {
		t.Fatalf("alert rows = %+v", rows)
	}
}

// newQuotaTestStore is a migrated database in the test's own directory.
func newQuotaTestStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "nibwire.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 轮询会一直跑：到点就读，ctx 结束就停。
func TestThePollerKeepsReading(t *testing.T) {
	reader := &flakyQuota{quota: ratectl.Quota{MaxSendRate: 100, Max24HourSend: 1000, SentLast24Hours: 0}}
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	bucket := NewBucket(1)
	t.Cleanup(bucket.Close)
	poller := &QuotaPoller{
		Reader: reader, Control: control, Bucket: bucket,
		Log: testLoggerForQuota(), Interval: 20 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	time.Sleep(120 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	reads := reader.reads
	reader.mu.Unlock()
	if reads < 3 {
		t.Fatalf("%d reads in 120 ms at a 20 ms interval", reads)
	}
}

func testLoggerForQuota() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
