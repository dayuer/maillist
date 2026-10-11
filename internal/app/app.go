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
	Log    *slog.Logger
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
	engine := mailer.NewEngine(db, sender, signer, mailer.Config{
		AppURL:         cfg.AppURL,
		SendingOn:      cfg.SendingOn,
		SendRatePerSec: cfg.Limits.SendRatePerSecond,
		Limits:         cfg.Limits,
		PollInterval:   250 * time.Millisecond,
	}, log)
	handler := events.New(db, log)

	return &App{
		Config: cfg,
		Store:  db,
		Signer: signer,
		Events: handler,
		Jobs:   jobs.New(db),
		Sender: sender,
		Mailer: engine,
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
	sender, err := mailer.NewSES(ctx, cfg.SESRegion, cfg.SESConfigSet, cfg.SendingOn)
	if err != nil {
		return nil, fmt.Errorf("SES client: %w", err)
	}
	return sender, nil
}

// Close releases the database and the send rate limiter.
func (a *App) Close() error {
	a.Mailer.Close()
	return a.Store.Close()
}
