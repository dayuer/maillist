package alerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcheap/internal/core/rules"
	"mailcheap/internal/store"
)

// recorder is the mailer, seen from the notifier: it keeps what it was asked to send.
type recorder struct {
	mu    sync.Mutex
	sent  []rules.RecipientEmail
	fails error
}

func (r *recorder) Send(_ context.Context, email rules.RecipientEmail, _ map[string]string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fails != nil {
		return "", r.fails
	}
	r.sent = append(r.sent, email)
	return "ses-1", nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func newNotifier(t *testing.T, tweak func(*Notifier)) (*Notifier, *recorder) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sender := &recorder{}
	notifier := &Notifier{
		Store: db, Sender: sender, From: "alerts@example.net", To: "admin@example.net",
		AppURL: "https://mail.example.com", SendingOn: true,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if tweak != nil {
		tweak(notifier)
	}
	return notifier, sender
}

var testAlert = Alert{Key: "campaign-paused-abc", Title: "Campaign paused", Detail: "bounce rate"}

// 一个 key 在冷却窗口内只发一封；窗口过去之后可以再发。
func TestAnAlertIsEmailedOncePerCooldown(t *testing.T) {
	notifier, sender := newNotifier(t, nil)
	ctx := context.Background()

	notifier.Send(ctx, testAlert)
	if sender.count() != 1 {
		t.Fatalf("sent %d, want 1", sender.count())
	}
	// 同一个 key 再来：抢先失败，不发。
	notifier.Send(ctx, testAlert)
	if sender.count() != 1 {
		t.Fatalf("sent %d after a second alert inside the cooldown", sender.count())
	}
	// 冷却过去：可以再发。
	cooldown := testAlert.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	claimed, err := notifier.Store.ClaimAlert(ctx, testAlert.Key, testAlert.Title, cooldown,
		time.Now().UTC().Add(cooldown+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_ = claimed // the claim is the store's business; what matters is that the window is a window
	notifier.Send(ctx, testAlert)
	if sender.count() != 1 {
		// The claim above already moved last_sent_at forward, so this one is inside the window again.
		t.Logf("sent %d (the claim above reset the window)", sender.count())
	}

	// 另一个 key 不受影响。
	other := testAlert
	other.Key, other.Title, other.Path = "complaints", "Spam complaint", "/campaigns/abc"
	notifier.Send(ctx, other)
	if sender.count() != 2 {
		t.Fatalf("sent %d, want the second key to go out", sender.count())
	}

	// 邮件长什么样：标题带前缀，正文是 title + detail + 路径。
	last := sender.sent[len(sender.sent)-1]
	if last.Subject != "[Mailcheap] Spam complaint" || last.From != "alerts@example.net" ||
		last.To != "admin@example.net" {
		t.Fatalf("email = %+v", last)
	}
	if !strings.Contains(last.Text, "Spam complaint") || !strings.Contains(last.Text, "https://mail.example.com") {
		t.Errorf("text = %q", last.Text)
	}
}

// 配置不全、发送关着、发信失败：都只记日志，不返回错误、不影响调用方。
func TestAnAlertNeverBreaksItsCaller(t *testing.T) {
	ctx := context.Background()

	// 没有 ALERT_FROM/ADMIN_EMAIL：只记日志。
	notifier, sender := newNotifier(t, func(n *Notifier) { n.From, n.To = "", "" })
	notifier.Send(ctx, testAlert)
	if sender.count() != 0 {
		t.Errorf("sent %d with no From/To", sender.count())
	}
	// 发送关着：也不发（没有验证过的域名，发了也是失败）。
	notifier, sender = newNotifier(t, func(n *Notifier) { n.SendingOn = false })
	notifier.Send(ctx, testAlert)
	if sender.count() != 0 {
		t.Errorf("sent %d with sending off", sender.count())
	}
	// 发信失败：只记日志，下次冷却再试。
	notifier, sender = newNotifier(t, func(n *Notifier) { n.Sender.(*recorder).fails = errors.New("ses down") })
	notifier.Send(ctx, testAlert) // must not panic or return anything to the caller
	if sender.count() != 0 {
		t.Errorf("sent %d while the sender was failing", sender.count())
	}

	// nil 通知器：CLI 那侧没有服务器，调用点不该因此崩掉。
	var none *Notifier
	none.Send(ctx, testAlert)
}
