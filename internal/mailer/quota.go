package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"mailcheap/internal/alerts"
	"mailcheap/internal/core/ratectl"
)

// The sending quota follows the account (design §19, D17). It is read rather than configured, because
// the number an operator types is a guess about an account they cannot see, and the guess is wrong in
// both directions: a new account allows a handful per second, an account that asked for an increase
// allows hundreds, and the same account has a 24-hour allowance a big send can exhaust.

// quotaReadTimeout bounds one GetAccount call. It is the same 5 s the identity reader uses: a quota
// that cannot be read in five seconds is a quota this process is going to keep using the last value
// of anyway.
const quotaReadTimeout = 5 * time.Second

// QuotaPollInterval is how often the quota is read again. The numbers move slowly — an AWS support
// case raises them, a day's sending spends them — and the poller's job is to notice, not to be
// current to the second.
const QuotaPollInterval = 60 * time.Second

// QuotaReader is where the account's quota comes from. Tests inject a StaticQuota; production gets
// SESQuota; and a deployment with several senders could one day put a shared service behind it
// (D19's extension point).
type QuotaReader interface {
	Quota(ctx context.Context) (ratectl.Quota, error)
}

// SESQuota reads the sending quota from SES v2 GetAccount.
type SESQuota struct {
	client *sesv2.Client
	// endpoint is recorded only so `mailcheap quota` can say where it asked.
	region string
}

// NewSESQuota builds the production reader, through the same credentials and endpoint override as
// NewSES and NewSESIdentity.
func NewSESQuota(ctx context.Context, options SESOptions) (*SESQuota, error) {
	cfg, err := awsConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	return &SESQuota{client: sesv2.NewFromConfig(cfg), region: options.Region}, nil
}

// Quota is one GetAccount call.
//
// SendQuota.Max24HourSend is -1 for an account without a 24-hour limit, which ratectl reads as
// unlimited rather than as a number (its documentation says so).
func (s *SESQuota) Quota(ctx context.Context) (ratectl.Quota, error) {
	readCtx, cancel := context.WithTimeout(ctx, quotaReadTimeout)
	defer cancel()
	out, err := s.client.GetAccount(readCtx, &sesv2.GetAccountInput{})
	if err != nil {
		return ratectl.Quota{}, fmt.Errorf("GetAccount: %w", err)
	}
	readAt := time.Now().UTC()
	if out.SendQuota == nil {
		return ratectl.Quota{}, errors.New("GetAccount answered no sending quota")
	}
	return ratectl.Quota{
		MaxSendRate:     out.SendQuota.MaxSendRate,
		Max24HourSend:   out.SendQuota.Max24HourSend,
		SentLast24Hours: out.SendQuota.SentLast24Hours,
		ReadAt:          readAt,
	}, nil
}

// StaticQuota is a fixed answer, for tests and for a deployment that would rather set the numbers
// itself than read them.
type StaticQuota struct {
	Value ratectl.Quota
	Err   error
}

// Quota returns what the struct was given.
func (s StaticQuota) Quota(context.Context) (ratectl.Quota, error) { return s.Value, s.Err }

// QuotaPoller keeps the controller and the bucket in step with the account: it reads once at
// startup, then every QuotaPollInterval.
//
// A failed read is not an error the process can act on — the last known quota is still the best guess
// available — so it is logged and the previous numbers stay. Never having read one is different: the
// rate is one per second until it succeeds, and the operator is told once (the alert's cooldown keeps
// it to once an hour while it stays broken).
type QuotaPoller struct {
	Reader   QuotaReader
	Control  *ratectl.Controller
	Bucket   *Bucket
	Alerts   *alerts.Notifier
	Log      *slog.Logger
	Interval time.Duration
}

// Run reads the quota until ctx ends. The first read happens immediately: a process that starts
// sending before it has read one would send at the floor for a minute for no reason.
func (p *QuotaPoller) Run(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = QuotaPollInterval
	}
	log := p.Log
	if log == nil {
		log = slog.Default()
	}

	p.pollOnce(ctx, log)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.pollOnce(ctx, log)
		}
	}
}

// pollOnce is one read. It never returns an error: the sender has a rate either way.
func (p *QuotaPoller) pollOnce(ctx context.Context, log *slog.Logger) {
	quota, err := p.Reader.Quota(ctx)
	if err != nil {
		log.Warn("could not read the SES sending quota", "error", err)
		if _, known := p.Control.Quota(); !known {
			p.Alerts.Send(ctx, alerts.Alert{
				Key: "ses-quota-unreadable", Title: "SES quota could not be read",
				Detail: "Sending at one email per second until it can be: " + err.Error() +
					". Check the credentials and that the IAM policy allows ses:GetAccount.",
			})
		}
		return
	}
	now := time.Now().UTC()
	p.Control.SetQuota(quota, now)
	FollowControl(p.Control, p.Bucket, now)
	log.Debug("read the SES sending quota",
		"maxSendRate", quota.MaxSendRate, "remaining24h", p.Control.Remaining24h(),
		"rate", p.Control.Rate(now))
}

// FollowControl pushes what the controller now says into the bucket.
//
// The controller decides the rate and the bucket enforces it (D8-1); this is the one place the two
// are joined, so that a fresh reading from the poller, a halving after a throttle and a step back up
// after a success all end at the same number. A nil controller is "no rate to follow": the bucket
// keeps the rate it was built with.
func FollowControl(control *ratectl.Controller, bucket *Bucket, now time.Time) {
	if control == nil || bucket == nil {
		return
	}
	bucket.SetRate(control.Rate(now))
}
