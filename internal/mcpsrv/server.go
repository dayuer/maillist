// Package mcpsrv is Nibwire's MCP surface: the tools an agent drives the newsletter with, over
// stdio or Streamable HTTP.
//
// The third way in, after the CLI and the v1 API, and the one built for a caller that is not a
// person (design §9, §11). Two things shape it:
//
//   - The tool table is the surface and the credential is the gate. Deleting and issuing
//     credentials are not tools at all (D10), and every other tool says which scope it needs. Each
//     call re-reads the credential from the database (D8), so revoking the row stops the next call
//     rather than the next restart.
//   - No tool blocks on a send. send_campaign and its relatives enqueue a job and return its id;
//     the serve process's scheduler is what sends, exactly as it is for the CLI (the phase 4 C2
//     invariant).
package mcpsrv

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"nibwire/internal/audit"
	"nibwire/internal/config"
	"nibwire/internal/creds"
	"nibwire/internal/jobs"
	"nibwire/internal/mailer"
	"nibwire/internal/store"
)

// Deps is everything the tools need. It is not an app.App: a tool must not be able to reach the
// CLI's flags, and the MCP surface is one of the three entries that share the domain logic rather
// than a command with a flag set of its own.
type Deps struct {
	Config config.Config
	Store  *store.DB
	// Mailer is only used for the one send that answers "did it go out" — see send_test in write.go.
	Mailer     *mailer.Engine
	Jobs       *jobs.Queue
	Identities mailer.IdentityReader
	// Channels and ChannelProblems are the app's (app.App): the channels test_channel can send
	// through, and the enabled ones that could not be built, by name.
	Channels        *mailer.Registry
	ChannelProblems map[string]error
	Log             *slog.Logger
	// Version is what the server reports to the client as its implementation version.
	Version string
}

// Server builds MCP servers. One is built per credential, because the scope check and the audit
// actor are per credential.
type Server struct {
	deps Deps
}

// New returns the MCP surface over deps.
func New(deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Server{deps: deps}
}

// For builds the MCP server a session with this token talks to.
//
// The hash is what the session carries; the credential it resolved to right now is not, because the
// next call has to ask again. That is the whole of D8: a token has no claims of its own, so there is
// nothing stale to trust.
func (s *Server) For(hash string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "nibwire", Version: orDev(s.deps.Version),
		Title: "Nibwire",
	}, &mcp.ServerOptions{
		Instructions: "Send newsletters through this deployment. Reads are paginated (at most " +
			"100 rows per call); a send is queued and answers with a job id you can poll with " +
			"get_job. Nothing here can delete anything or issue credentials — that is the CLI's.",
	})
	surface := &surface{server: server, sess: &session{deps: s.deps, hash: hash}}
	registerTools(surface, s.deps)
	return server
}

func orDev(version string) string {
	if version == "" {
		return "dev"
	}
	return version
}

// session is who is calling: the token hash that authenticated the request, and nothing else that
// could go stale.
type session struct {
	deps Deps
	hash string
}

// require resolves the token and checks the scope, in that order.
//
// A call re-reads the credential every time, so a revoked row fails here on the next call — while
// the process is still running, with no cache to expire. The scope sentence names the scope the
// caller is missing: an agent that reads it can ask its operator for the right token, which is a
// better outcome than a tool that is not in the list.
func (s *session) require(ctx context.Context, scope creds.Scope) (store.Credential, error) {
	credential, err := s.deps.Store.CredentialByHash(ctx, s.hash)
	if err != nil {
		return store.Credential{}, fmt.Errorf(
			"this token is no longer valid: it was revoked, or the row is gone")
	}
	if !creds.Allows(credential.Scopes, scope) {
		return store.Credential{}, fmt.Errorf(
			`this credential may not do that (it needs the %q scope)`, scope)
	}
	if creds.ShouldTouchLastUsed(credential.LastUsedAt, time.Now().UTC()) {
		// Bookkeeping: a failure here must not fail the call.
		if err := s.deps.Store.TouchCredential(ctx, credential.ID, time.Now().UTC()); err != nil {
			s.deps.Log.Warn("could not update last_used_at", "credential", credential.ID, "error", err)
		}
	}
	return credential, nil
}

// note records one write. It is the same best-effort contract the CLI uses: the write has already
// happened, so a failed insert is logged and never turned into an error the caller would read as
// "nothing happened".
func (s *session) note(ctx context.Context, credential store.Credential, action, result string, params any) {
	_ = audit.Record(ctx, s.deps.Store, audit.MCP(credential.Name), action, result, params, s.deps.Log)
}

// ToolInfo is one line of the surface: what the agent sees and what using it takes.
type ToolInfo struct {
	Name  string
	Scope creds.Scope
}

// Surface is the tool surface in registration order.
//
// It exists so the tests can read the surface rather than restate it: "the delete tools are not
// registered" is a property of this list, and a property of the code because registerTools walks it.
func Surface() []ToolInfo {
	out := make([]ToolInfo, 0, len(tools))
	for _, tool := range tools {
		out = append(out, ToolInfo{Name: tool.name, Scope: tool.scope})
	}
	return out
}

// tool is one entry of the table: the name the agent sees, the scope the call needs, and the
// registration that binds a typed handler to them.
//
// The name and the scope are declared once, here. add is the only place a handler meets them, and
// add is where the scope check lives — so a tool cannot be registered without a scope, and the two
// cannot drift apart.
type tool struct {
	name  string
	scope creds.Scope
	about string
	add   func(sf *surface, t *tool)
}

// surface is the server being built plus the session it belongs to.
type surface struct {
	server *mcp.Server
	sess   *session
}

// addTool binds one typed handler to an entry's name, description and scope.
//
// A returned error becomes a tool error rather than a protocol error: the SDK packs it into the
// result with IsError set, which is what lets the caller read it and correct itself instead of
// seeing a transport failure.
func addTool[In, Out any](sf *surface, t *tool,
	handler func(ctx context.Context, sess *session, credential store.Credential, in In) (Out, error)) {
	mcp.AddTool(sf.server, &mcp.Tool{Name: t.name, Description: t.about},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			var zero Out
			credential, err := sf.sess.require(ctx, t.scope)
			if err != nil {
				return nil, zero, err
			}
			out, err := handler(ctx, sf.sess, credential, in)
			if err != nil {
				return nil, zero, err
			}
			return nil, out, nil
		})
}
