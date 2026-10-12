// Package app wires config, store, signer, jobs, the mailer and the HTTP server into one value. The
// CLI and the reader surface both take an App and nothing else.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"mailcheap/internal/config"
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
	Log        *slog.Logger
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
	engine := mailer.NewEngine(db, sender, signer, mailer.Config{
		AppURL:         cfg.AppURL,
		ArchiveHosts:   cfg.ArchiveHosts,
		SendingOn:      cfg.SendingOn,
		SendRatePerSec: cfg.Limits.SendRatePerSecond,
		Limits:         cfg.Limits,
		PollInterval:   250 * time.Millisecond,
	}, log)
	handler := events.New(db, log)

	return &App{
		Config:     cfg,
		Store:      db,
		Signer:     signer,
		Events:     handler,
		Jobs:       jobs.New(db),
		Sender:     sender,
		Mailer:     engine,
		Identities: identities,
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
