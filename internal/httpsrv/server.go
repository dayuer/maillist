// Package httpsrv is the reader-facing HTTP surface: tracking, unsubscribe, confirm, keep, the
// subscribe form, the SES webhook and the health check.
package httpsrv

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"mailcheap/internal/config"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/core/sns"
	"mailcheap/internal/events"
	"mailcheap/internal/store"
)

// Server holds everything the handlers need. The admin surface (/api/v1, /mcp) is later phases;
// this one is the reader side plus the SES webhook.
type Server struct {
	Config  config.Config
	Store   *store.DB
	Signer  *sign.Signer
	Events  *events.Handler
	Limiter *MemoryLimiter
	// Verifier checks SNS signatures. Production passes sns.NewVerifier(CertificateFetcher(nil));
	// the tests pass one that trusts a key they generated.
	Verifier *sns.Verifier
	// Subscribe confirms an SNS subscription; it is a field so tests can stand in for SNS.
	Subscribe Subscriber
	Log       *slog.Logger
	now       func() time.Time
	handler   http.Handler
}

// New builds the router over the reader surface.
func New(cfg config.Config, db *store.DB, signer *sign.Signer, handler *events.Handler,
	verifier *sns.Verifier, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	server := &Server{
		Config: cfg, Store: db, Signer: signer, Events: handler,
		Limiter: NewMemoryLimiter(), Verifier: verifier, Subscribe: HTTPSubscriber(nil),
		Log: log, now: time.Now,
	}
	server.handler = server.routes()
	return server
}

// clock is the current time, injectable so a test can pin the scanner window.
func (s *Server) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// ServeHTTP is the http.Handler the tests and cmd/serve both use.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// routes builds the reader surface. Everything unregistered is a 404 rather than a redirect, so a
// typo never lands somewhere surprising.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /o/{token}", s.handleOpen)
	mux.HandleFunc("GET /c/{token}", s.handleClick)
	mux.HandleFunc("HEAD /c/{token}", s.handleClickHead)
	mux.HandleFunc("POST /api/webhooks/ses", s.handleSESWebhook)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return s.withSecurityHeaders(s.withArchiveHost(s.withRequestLog(mux)))
}

// Run serves until ctx is cancelled, then shuts down gracefully within shutdownTimeout: in-flight
// requests finish, new connections are refused.
func (s *Server) Run(ctx context.Context, addr string, shutdownTimeout time.Duration) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
