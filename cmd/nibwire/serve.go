package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/httpsrv"
	"nibwire/internal/mcpsrv"
	"nibwire/internal/scheduler"
	"nibwire/internal/store"
)

// shutdownTimeout is how long the HTTP server may take to finish the requests in flight.
const shutdownTimeout = 10 * time.Second

func newServeCmd() *cobra.Command {
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the reader side: HTTP, the sender, the scheduler and the MCP endpoint",
		Args:  cobra.NoArgs,
		RunE:  runServe,
	}
	serve.Flags().String("addr", ":8080", "address to listen on")
	// The MCP endpoint is a listener of its own, on the loopback by default (design §4): it is the
	// admin surface, and caddy is what fronts the reader side. The flag exists so a test — or a
	// deployment that puts it on another interface — can move it.
	serve.Flags().String("mcp-addr", "", "address for the MCP endpoint (default MCP_HTTP_ADDR)")
	return serve
}

// runServe starts everything one process runs. SIGTERM stops claiming new work, lets the batch in
// flight finish its writes, then exits — so no message is left claimed (design §3.1).
func runServe(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	mcpAddr, _ := cmd.Flags().GetString("mcp-addr")

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		if strings.TrimSpace(mcpAddr) == "" {
			mcpAddr = a.Config.MCPHTTPAddr
		}
		mcpServer := mcpsrv.New(mcpsrv.Deps{
			Config: a.Config, Store: a.Store, Mailer: a.Mailer, Jobs: a.Jobs,
			Identities: a.Identities, Log: a.Log, Version: version,
		})

		// A crash between the SES call and the write-back leaves claimed rows. They become unknown
		// and are never sent twice (D14).
		recovered, err := a.Store.RecoverClaimed(ctx)
		if err != nil {
			return err
		}
		if recovered > 0 {
			a.Log.Warn("messages were in flight during an earlier crash; they will not be resent",
				"count", recovered)
		}

		runner := scheduler.New(a.Jobs, a.Mailer.RunJob, 250*time.Millisecond, a.Log)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "listening on "+addr)

		// The three components share one context, and a failure in any of them cancels it: a server
		// that has lost its sender, or its listener, is not a healthy server. Waiting for all three
		// to stop on their own would leave exactly that running — and healthz answering 200.
		runCtx, stopEverything := context.WithCancel(ctx)
		defer stopEverything()

		// Sending converges on this process while it runs: the CLI asks the heartbeat before it
		// drains anything itself (design §5.2). It is cleared on the way out so the next CLI command
		// does not wait for the window to lapse.
		beatDone := make(chan struct{})
		go func() {
			defer close(beatDone)
			stampServeHeartbeat(runCtx, a)
		}()
		defer func() {
			<-beatDone
			clearCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := a.Store.ClearHeartbeat(clearCtx, store.ServeHeartbeat); err != nil {
				a.Log.Warn("could not clear the serve heartbeat", "error", err)
			}
		}()

		errs := make(chan error, 5)
		var wg sync.WaitGroup
		for name, run := range map[string]func() error{
			// The rate follows the account's own numbers (D17), and this is what keeps them current:
			// it reads once now and again every minute. A server is the process that needs this — it
			// is the one that sends for hours — while a command asks once before it drains (app.ReadQuota).
			"quota":     func() error { return a.Poller.Run(runCtx) },
			"sender":    func() error { return a.Mailer.Run(runCtx) },
			"scheduler": func() error { return runner.Run(runCtx) },
			"http":      func() error { return a.HTTP.Run(runCtx, addr, shutdownTimeout) },
			"mcp": func() error {
				return httpsrv.Serve(runCtx, mcpAddr, mcpServer.HTTPHandler(), shutdownTimeout)
			},
		} {
			wg.Add(1)
			go func(name string, run func() error) {
				defer wg.Done()
				err := run()
				if err == nil || errors.Is(err, context.Canceled) {
					return
				}
				errs <- fmt.Errorf("%s: %w", name, err)
				stopEverything()
			}(name, run)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			return err
		}
		return nil
	})
}

// stampServeHeartbeat keeps the liveness stamp fresh until ctx ends. A failed write is not fatal —
// the stamp going stale only means another process may start sending too, and the store's own
// busy-retry already covers a contended database.
func stampServeHeartbeat(ctx context.Context, a *app.App) {
	touch := func() {
		if err := a.Store.TouchHeartbeat(ctx, store.ServeHeartbeat, time.Now().UTC()); err != nil &&
			ctx.Err() == nil {
			a.Log.Warn("could not stamp the serve heartbeat", "error", err)
		}
	}
	touch()
	ticker := time.NewTicker(store.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			touch()
		}
	}
}
