package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/creds"
	"mailcheap/internal/httpsrv"
	"mailcheap/internal/mcpsrv"
)

// newMCPCmd is `mailcheap mcp`: the MCP surface as a process of its own.
//
// `serve` already listens for MCP on MCP_HTTP_ADDR, and this is for the two cases it cannot cover —
// a client that speaks stdio (Claude Desktop, an editor, `docker exec -i`), and a deployment that
// wants the endpoint in its own container.
func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the MCP tools to an agent",
		Long: "Speaks MCP over stdio by default, or over Streamable HTTP with --http.\n\n" +
			"A token is required either way, and it has to be an `mcp` credential — issue one with " +
			"`mailcheap credentials create --kind mcp --name <name> --scopes read,write,send`. The " +
			"tools it may use and the daily budget it spends are that credential's, and revoking the " +
			"row stops the next call.",
		Args: cobra.NoArgs,
		RunE: runMCP,
	}
	flags := cmd.Flags()
	flags.Bool("stdio", false, "speak MCP on stdin/stdout (the default)")
	flags.Bool("http", false, "listen for Streamable HTTP instead")
	flags.String("addr", "", "address for --http (default MCP_HTTP_ADDR)")
	flags.String("token", "", "the mcp token (default MAILCHEAP_MCP_TOKEN)")
	return cmd
}

func runMCP(cmd *cobra.Command, _ []string) error {
	stdio, _ := cmd.Flags().GetBool("stdio")
	httpMode, _ := cmd.Flags().GetBool("http")
	if stdio && httpMode {
		return errors.New("--stdio and --http are two ways to serve the same thing: pick one")
	}

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		token, err := mcpToken(cmd)
		if err != nil {
			return err
		}
		// The token is checked once at startup so a wrong one fails here, with a message, rather
		// than at the first tool call inside an agent's transcript. Every call checks it again
		// (mcpsrv does that), which is what makes revocation instant.
		hash := creds.Hash(token)
		credential, err := a.Store.CredentialByHash(ctx, hash)
		if err != nil {
			return fmt.Errorf("that token is not a live credential: %w", err)
		}
		if credential.Kind != creds.KindMCP {
			return fmt.Errorf("that token is a %s credential; `mailcheap mcp` needs an %s one",
				credential.Kind, creds.KindMCP)
		}

		server := mcpsrv.New(mcpsrv.Deps{
			Config: a.Config, Store: a.Store, Mailer: a.Mailer, Jobs: a.Jobs,
			Identities: a.Identities, Log: a.Log, Version: version,
		})

		if httpMode {
			addr, _ := cmd.Flags().GetString("addr")
			if strings.TrimSpace(addr) == "" {
				addr = a.Config.MCPHTTPAddr
			}
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "listening on "+addr)
			return httpsrv.Serve(ctx, addr, server.HTTPHandler(), shutdownTimeout)
		}

		// stdio: the session outlives any single call, so the tool list is the one this token had
		// when the process started. A call still re-reads the credential, so a revocation stops the
		// calls that follow even here.
		a.Log.Info("serving MCP on stdio", "credential", credential.Name)
		return server.For(hash).Run(ctx, &mcp.StdioTransport{})
	})
}

// mcpToken reads the token from the flag or the environment, and says what to do when there is none.
func mcpToken(cmd *cobra.Command) (string, error) {
	token, _ := cmd.Flags().GetString("token")
	if strings.TrimSpace(token) == "" {
		token = os.Getenv("MAILCHEAP_MCP_TOKEN")
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("no token: pass --token, or set MAILCHEAP_MCP_TOKEN. Issue one with " +
			"`mailcheap credentials create --kind mcp --name agent --scopes read,write,send`")
	}
	return strings.TrimSpace(token), nil
}
