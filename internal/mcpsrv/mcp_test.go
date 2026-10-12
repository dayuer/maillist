package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mailcheap/internal/config"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/creds"
	"mailcheap/internal/jobs"
	"mailcheap/internal/mailer"
	"mailcheap/internal/store"
)

// fixture is a deployment an agent can be pointed at: a database, a fake sender, a queue and one
// credential per scope set the tests need. Nothing here is a stand-in for the code under test — the
// session, the scope checks and the tools are the real ones.
type fixture struct {
	deps   Deps
	store  *store.DB
	fake   *mailer.Fake
	engine *mailer.Engine
	token  string
}

// newFixture builds a deployment with a credential holding every scope.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithScopes(t, []creds.Scope{creds.ScopeRead, creds.ScopeWrite, creds.ScopeSend})
}

func newFixtureWithScopes(t *testing.T, scopes []creds.Scope) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The deployment's own heartbeat: serve is running, which is what the send tools require.
	if err := db.TouchHeartbeat(ctx, store.ServeHeartbeat, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	signer, err := sign.New(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	fake := mailer.NewFake()
	limits := model.Limits{
		BounceRate: 0.02, ComplaintRate: 0.0008, MaxEmailBytes: 100_000,
		SendRatePerSecond: 1000, WelcomeEmailsPer10Minute: 100, MaxSendsPerDay: 50_000,
	}
	engine := mailer.NewEngine(db, fake, signer, mailer.Config{
		AppURL: "https://mail.example.com", SendingOn: true, SendRatePerSec: 1000,
		Limits: limits, PollInterval: 10 * time.Millisecond,
	}, slog.Default())
	t.Cleanup(engine.Close)

	token := "mc_" + ids.New()
	credential := store.Credential{
		ID: ids.New(), Kind: "mcp", Name: "newsletter-bot", Prefix: "mc_test…",
		TokenHash: creds.Hash(token), Scopes: scopes, DailyBudget: 0,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}

	return &fixture{
		deps: Deps{
			Config: config.Config{
				AppURL: "https://mail.example.com", DataDir: dir, SendingOn: true,
				SendApproval: "auto", Limits: limits,
			},
			Store: db, Mailer: engine, Jobs: jobs.New(db),
			Identities: mailer.StaticIdentity{Domains: []string{"example.net"}},
			Log:        slog.Default(), Version: "test",
		},
		store: db, fake: fake, engine: engine, token: token,
	}
}

// connectWith opens a session for this fixture's credential.
func (f *fixture) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return connect(t, f.deps, creds.Hash(f.token))
}

// call runs one tool and fails the test on a transport error or a tool error.
func call[Out any](t *testing.T, session *mcp.ClientSession, name string, args map[string]any) Out {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %s", name, textOf(result))
	}
	var out Out
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("%s: %s", name, err)
	}
	return out
}

// callError runs one tool and returns what it said. A tool that did not fail is a test failure.
func callError(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	if !result.IsError {
		t.Fatalf("%s did not fail", name)
	}
	return textOf(result)
}

func textOf(result *mcp.CallToolResult) string {
	var parts []string
	for _, item := range result.Content {
		if text, ok := item.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// seedNewsletter creates a newsletter with contacts and a draft the way an operator would, so the
// MCP tests start from a deployment that already has something in it.
func (f *fixture) seedNewsletter(t *testing.T, contacts int) model.Newsletter {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "field-notes", Name: "Field notes", FromName: "Jane Doe",
		FromEmail: "jane@example.net", PostalAddress: "1 Main St, Paris",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.CreateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < contacts; i++ {
		contact := model.Contact{
			ID: ids.New(), NewsletterID: newsletter.ID, Status: model.ContactActive,
			Email: fmt.Sprintf("reader%d@example.com", i), CreatedAt: now, SubscribedAt: &now,
		}
		if err := f.store.CreateContact(ctx, contact); err != nil {
			t.Fatal(err)
		}
	}
	return newsletter
}
