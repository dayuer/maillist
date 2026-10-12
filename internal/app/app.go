// Package app wires config, store, signer, jobs, the mailer and the HTTP server into one value. The
// CLI and the reader surface both take an App and nothing else.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"mailcheap/internal/alerts"
	"mailcheap/internal/config"
	"mailcheap/internal/core/ratectl"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/core/sns"
	"mailcheap/internal/events"
	"mailcheap/internal/httpsrv"
	"mailcheap/internal/jobs"
	"mailcheap/internal/mailer"
	"mailcheap/internal/store"
)

// App is everything a command needs.
type App struct {
	Config config.Config
	Store  *store.DB
	Signer *sign.Signer
	Events *events.Handler
	Jobs   *jobs.Queue
	Sender mailer.Sender
	Mailer *mailer.Engine
	HTTP   *httpsrv.Server
	// Identities answers "which sending domains does SES consider verified?" — the pre-send check
	// and `mailcheap domains` both read it.
	Identities mailer.IdentityReader
	// Quota reads the account's sending limits, and Poller keeps the send rate in step with them
	// (D17). The server runs the poller; a command reads once instead, see ReadQuota.
	Quota  mailer.QuotaReader
	Poller *mailer.QuotaPoller
	Log    *slog.Logger
}

// ReadQuota reads the account's quota now and applies it to the sending rate.
//
// A server polls; a command that is about to drain its own queue cannot wait a minute for the first
// reading, and one it never read is one email a second. The error is returned rather than fatal: a
// command that cannot read the account sends slowly and says so, which is the same trade the poller
// makes.
func (a *App) ReadQuota(ctx context.Context) (ratectl.Quota, error) {
	quota, err := a.Quota.Quota(ctx)
	if err != nil {
		return ratectl.Quota{}, err
	}
	if quota.ReadAt.IsZero() {
		// A reader that is not SES — the stand-in the end-to-end tests use — has no time of its own,
		// and a quota is a fact about a moment: the moment is now.
		quota.ReadAt = time.Now().UTC()
	}
	a.Mailer.SetQuota(quota, quota.ReadAt)
	return quota, nil
}

// New opens the database (creating and migrating it if needed) and builds the adapters on top.
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	signer, err := sign.New(cfg.SessionSecret)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	sender, err := newSender(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	identities, err := newIdentities(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// Alerts go through the sender the deployment already has: no second client, and no alert email
	// when sending is off (there would be no verified domain to send it from).
	notifier := &alerts.Notifier{
		Store: db, Sender: sender, From: cfg.AlertFrom, To: cfg.AdminEmail,
		AppURL: cfg.AppURL, SendingOn: cfg.SendingOn, Log: log,
	}
	quota, err := newQuota(ctx, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// The rate follows the account (D17): the controller decides it, the bucket enforces it, and
	// SEND_RATE_PER_SECOND can only lower what the account gave.
	control := ratectl.New(ratectl.Config{
		Headroom: cfg.Limits.SendRateHeadroom,
		Cap:      cfg.Limits.SendRatePerSecond,
		Floor:    1,
	})
	engine := mailer.NewEngine(db, sender, signer, mailer.Config{
		AppURL:          cfg.AppURL,
		ArchiveHosts:    cfg.ArchiveHosts,
		SendingOn:       cfg.SendingOn,
		SendRatePerSec:  int(control.Rate(time.Now().UTC())),
		SendConcurrency: cfg.SendConcurrency,
		Limits:          cfg.Limits,
		PollInterval:    250 * time.Millisecond,
		RateControl:     control,
		Alerts:          notifier,
	}, log)
	handler := events.New(db, log).WithAlerts(notifier)

	return &App{
		Config:     cfg,
		Store:      db,
		Signer:     signer,
		Events:     handler,
		Jobs:       jobs.New(db),
		Sender:     sender,
		Mailer:     engine,
		Identities: identities,
		Quota:      quota,
		Poller: &mailer.QuotaPoller{
			Reader: quota, Control: control, Bucket: engine.Bucket(), Alerts: notifier, Log: log,
		},
		HTTP: httpsrv.New(cfg, db, signer, handler, engine,
			sns.NewVerifier(httpsrv.CertificateFetcher(nil)), log),
		Log: log,
	}, nil
}

// newSender picks the fake when the environment asked for one, so the end-to-end test can observe
// sends without AWS credentials.
func newSender(ctx context.Context, cfg config.Config) (mailer.Sender, error) {
	if cfg.FakeSESLog != "" {
		return mailer.NewFake(mailer.WithLogFile(cfg.FakeSESLog)), nil
	}
	sender, err := mailer.NewSES(ctx, sesOptions(cfg), cfg.SESConfigSet, cfg.SendingOn)
	if err != nil {
		return nil, fmt.Errorf("SES client: %w", err)
	}
	return sender, nil
}

// sesOptions is where SES is and who is asking, from the configuration.
func sesOptions(cfg config.Config) mailer.SESOptions {
	return mailer.SESOptions{
		Region: cfg.SESRegion, Endpoint: cfg.SESEndpoint,
		AccessKeyID: cfg.SESAccessKeyID, SecretKey: cfg.SESSecretAccessKey,
	}
}

// localStandInRate is what the fake sender's stand-in account reports. There is no SES behind the
// file-backed fake, so the only thing it must be is fast enough not to slow a test down.
const localStandInRate = 500

// newQuota mirrors newSender and newIdentities. The file-backed fake means there are no AWS
// credentials, so there is no account to ask: the stand-in is a fast account with no 24-hour limit,
// because a fake with no SES behind it has no limits to model and refusing a send in the end-to-end
// tests would only ever be a false alarm.
//
// A deployment that points SES_ENDPOINT at a fake SES — the compose stack — takes the path below and
// reads the numbers that fake was configured to report, which is how D17 is exercised end to end.
func newQuota(ctx context.Context, cfg config.Config) (mailer.QuotaReader, error) {
	if cfg.FakeSESLog != "" && cfg.SESEndpoint == "" {
		rate := cfg.Limits.SendRatePerSecond
		if rate <= 0 {
			rate = localStandInRate
		}
		return mailer.StaticQuota{Value: ratectl.Quota{
			MaxSendRate: float64(rate),
			// -1 is what SES says for an account without a 24-hour limit (ratectl reads it as
			// unlimited rather than as a number).
			Max24HourSend: -1,
		}}, nil
	}
	quota, err := mailer.NewSESQuota(ctx, sesOptions(cfg))
	if err != nil {
		return nil, fmt.Errorf("SES quota client: %w", err)
	}
	return quota, nil
}

// newIdentities mirrors newSender: asking for the fake sender means there are no AWS credentials, so
// the identity list comes from MAILCHEAP_FAKE_SES_DOMAINS and the end-to-end script can drive both
// the verified and the unverified branch.
func newIdentities(ctx context.Context, cfg config.Config) (mailer.IdentityReader, error) {
	if cfg.FakeSESLog != "" {
		return mailer.StaticIdentity{Domains: cfg.FakeSESDomains}, nil
	}
	identities, err := mailer.NewSESIdentity(ctx, sesOptions(cfg))
	if err != nil {
		return nil, fmt.Errorf("SES identity client: %w", err)
	}
	return identities, nil
}

// Close releases the database and the send rate limiter.
func (a *App) Close() error {
	a.Mailer.Close()
	return a.Store.Close()
}
