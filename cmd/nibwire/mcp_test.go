package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nibwire/internal/core/ids"
	"nibwire/internal/creds"
	"nibwire/internal/store"
)

// mcpEnv points the CLI at a database with one mcp credential and one newsletter in it.
func mcpEnv(t *testing.T) (token string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("APP_URL", "https://mail.example.com")
	t.Setenv("NIBWIRE_DATA_DIR", dir)
	t.Setenv("NIBWIRE_DB", filepath.Join(dir, "nibwire.db"))
	t.Setenv("NIBWIRE_FAKE_SES_LOG", filepath.Join(dir, "sent.jsonl"))
	t.Setenv("NIBWIRE_FAKE_SES_DOMAINS", "example.net")
	t.Setenv("NIBWIRE_MCP_TOKEN", "")

	runNibwire(t, "init")
	runNibwire(t, "newsletter", "create", "--name", "Notes", "--slug", "notes",
		"--from-email", "jane@example.net", "--postal-address", "1 Main St, Paris")

	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("NIBWIRE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	generated := creds.Generate()
	if err := db.CreateCredential(ctx, store.Credential{
		ID: ids.New(), Kind: creds.KindMCP, Name: "agent", Prefix: generated.Prefix,
		TokenHash: generated.Hash, Scopes: []creds.Scope{creds.ScopeRead, creds.ScopeWrite, creds.ScopeSend},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	api := creds.Generate()
	if err := db.CreateCredential(ctx, store.Credential{
		ID: ids.New(), Kind: creds.KindAPI, Name: "site", Prefix: api.Prefix,
		TokenHash: api.Hash, Scopes: []creds.Scope{creds.ScopeRead}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return generated.Token
}

// 没有 token 就说清楚要什么，而不是启动一个谁都能用的 MCP 服务。
func TestMCPNeedsAToken(t *testing.T) {
	mcpEnv(t)
	out := runNibwireError(t, "mcp", "--stdio")
	if !strings.Contains(out, "credentials create --kind mcp") {
		t.Fatalf("err = %q", out)
	}
}

// token 的 kind 必须对得上：api key 用不了 mcp 面。
func TestMCPRefusesTheWrongKindOfToken(t *testing.T) {
	mcpEnv(t)
	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("NIBWIRE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	generated := creds.Generate()
	if err := db.CreateCredential(ctx, store.Credential{
		ID: ids.New(), Kind: creds.KindAPI, Name: "for-mcp", Prefix: generated.Prefix,
		TokenHash: generated.Hash, Scopes: []creds.Scope{creds.ScopeRead}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	out := runNibwireError(t, "mcp", "--stdio", "--token", generated.Token)
	if !strings.Contains(out, "needs an mcp one") {
		t.Fatalf("err = %q", out)
	}
}

// 未知 token 也是启动就失败：不等到 agent 里的第一次调用才报错。
func TestMCPRefusesAnUnknownToken(t *testing.T) {
	mcpEnv(t)
	out := runNibwireError(t, "mcp", "--stdio", "--token", creds.Generate().Token)
	if !strings.Contains(out, "not a live credential") {
		t.Fatalf("err = %q", out)
	}
}

// 两种传输方式是二选一。
func TestMCPRefusesBothTransportsAtOnce(t *testing.T) {
	mcpEnv(t)
	out := runNibwireError(t, "mcp", "--stdio", "--http", "--token", creds.Generate().Token)
	if !strings.Contains(out, "pick one") {
		t.Fatalf("err = %q", out)
	}
}

// stdio 的端到端：子进程里跑真的 `nibwire mcp --stdio`，用 JSON-RPC 在它的 stdin/stdout 上
// 走一遍 initialize → tools/list → tools/call。这是 `docker exec -i` 和编辑器插件用的那条路。
func TestMCPStdioServesTools(t *testing.T) {
	token := mcpEnv(t)
	child := exec.Command(os.Args[0], "-test.run=TestMCPStdioServesTools") //nolint:gosec // G204: re-running this test binary as the mcp child
	child.Env = append(os.Environ(), "NIBWIRE_TEST_MCP_STDIO="+token)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()

	// One reader for the whole session: two goroutines on the same pipe would take turns eating
	// each other's lines.
	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	writer := bufio.NewWriter(stdin)
	send := func(message string) {
		if _, err := fmt.Fprintln(writer, message); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	reply(t, lines, "1", "capabilities")

	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	listing := reply(t, lines, "2", "tools")
	for _, wanted := range []string{"count_audience", "send_campaign", "get_stats"} {
		if !strings.Contains(listing, wanted) {
			t.Errorf("tools/list did not offer %s", wanted)
		}
	}
	if strings.Contains(listing, "delete_") || strings.Contains(listing, "create_credentials") {
		t.Errorf("tools/list offered something design §9 keeps off the surface")
	}

	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_newsletters","arguments":{}}}`)
	called := reply(t, lines, "3", `"slug":"notes"`)
	if called == "" {
		t.Fatal("list_newsletters answered nothing")
	}
}

// reply reads lines until the one with this id arrives, and checks it mentions what the test expects.
func reply(t *testing.T, lines <-chan string, id, contains string) string {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the server closed stdout before answering %s", id)
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(line), &parsed); err != nil {
				continue // a log line, or part of one
			}
			if fmt.Sprint(parsed["id"]) != id {
				continue
			}
			if !strings.Contains(line, contains) {
				t.Fatalf("the answer to %s was %s", id, line)
			}
			return line
		case <-deadline:
			t.Fatalf("no answer to %s within 20s", id)
		}
	}
}
