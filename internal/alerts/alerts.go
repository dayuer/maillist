// Package alerts is the deployment's way of telling its operator that something is wrong: a campaign
// that paused itself, a spam complaint, a scheduled send that never started, the welcome-email cap.
//
// Everything about it is upstream lib/alerts.js, and the rules are worth restating because they are
// what makes alerts useful rather than noisy:
//
//   - it never returns an error, and it never panics. An alert is a message about a failure, and a
//     message that can cause one is worse than no message.
//   - every alert is logged, always.
//   - an email goes to ADMIN_EMAIL from ALERT_FROM, and only while sending is on: a deployment with
//     no verified domain cannot send its own alert, and pretending otherwise just fails twice.
//   - the same key is emailed at most once per cooldown. A one-minute cooldown for a campaign pause
//     and an hour for the rest is upstream's.
package alerts

import (
	"context"
	"log/slog"
	"time"

	"mailcheap/internal/core/render"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/store"
)

// Sender is the mailer, seen from here: one message to one address. It is declared rather than
// imported so that internal/mailer can use this package without the two importing each other.
type Sender interface {
	Send(ctx context.Context, email rules.RecipientEmail, tags map[string]string) (string, error)
}

// DefaultCooldown is upstream DEFAULT_COOLDOWN_MINUTES: an hour between emails for the same key.
const DefaultCooldown = 60 * time.Minute

// Alert is one thing worth telling the operator about.
type Alert struct {
	// Key is what the cooldown is counted against: two alerts with the same key are the same news.
	Key    string
	Title  string
	Detail string
	// Path is where in the deployment the operator should look, appended to APP_URL.
	Path string
	// Cooldown is how long before this key may be emailed again. Zero means DefaultCooldown.
	Cooldown time.Duration
}

// Notifier logs and emails alerts.
type Notifier struct {
	Store  *store.DB
	Sender Sender
	// From and To are ALERT_FROM and ADMIN_EMAIL. Either missing means no email is sent: alerts are
	// optional, and a deployment that has not configured them still gets the log lines.
	From string
	To   string
	// AppURL is what Path is appended to.
	AppURL string
	// SendingOn is whether this deployment may send at all (SENDING_ENABLED).
	SendingOn bool
	Log       *slog.Logger
}

// Send is upstream sendAlert: log, then email at most once per cooldown.
//
// A nil notifier is a deployment that was built without alerts (the CLI, whose commands have no
// server to alert); Send is then a no-op rather than a nil dereference, because the callers are in
// code paths that must not fail over this.
func (n *Notifier) Send(ctx context.Context, alert Alert) {
	if n == nil {
		return
	}
	log := n.Log
	if log == nil {
		log = slog.Default()
	}
	// Always logged: the log is where an operator sees it even with no mail configured.
	log.Warn("alert", "key", alert.Key, "title", alert.Title, "detail", alert.Detail)
	if n.Store == nil || n.Sender == nil || n.From == "" || n.To == "" || !n.SendingOn {
		return
	}
	cooldown := alert.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	claimed, err := n.Store.ClaimAlert(ctx, alert.Key, alert.Title, cooldown, time.Now().UTC())
	if err != nil {
		log.Error("alert: could not claim the cooldown", "key", alert.Key, "error", err)
		return
	}
	if !claimed {
		return
	}

	text := alert.Title + "\n" + alert.Detail
	if alert.Path != "" {
		text += "\n" + n.AppURL + alert.Path
	}
	_, err = n.Sender.Send(ctx, rules.RecipientEmail{
		From: n.From, To: n.To, Subject: "[Mailcheap] " + alert.Title,
		Text: text,
		HTML: `<pre style="font:14px/1.5 ui-monospace,Menlo,monospace;white-space:pre-wrap">` +
			render.EscapeHTML(text) + `</pre>`,
	}, nil)
	if err != nil {
		// The cooldown was already taken, so this alert waits for the next cooldown rather than
		// retrying; the log line above is what an operator has to work from.
		log.Error("alert: could not send the email", "key", alert.Key, "error", err)
	}
}
