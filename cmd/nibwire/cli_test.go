package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"nibwire/internal/store"
)

// TestMain registers this binary as "nibwire" so the scripts in testdata/script can run it, and
// lets TestServeShutsDownCleanly run the same binary as a `nibwire serve` child.
//
// The MCP stdio test needs the same trick for the same reason: the stdio transport talks to the
// process's own stdin and stdout, so it has to be a process.
func TestMain(m *testing.M) {
	if addr := os.Getenv("NIBWIRE_TEST_SERVE_ADDR"); addr != "" {
		os.Exit(runServeChild(addr))
	}
	if token := os.Getenv("NIBWIRE_TEST_MCP_STDIO"); token != "" {
		os.Exit(runMCPStdioChild(token))
	}
	testscript.Main(m, map[string]func(){"nibwire": func() { main() }})
}

// runMCPStdioChild runs `nibwire mcp --stdio` until its client closes stdin.
func runMCPStdioChild(token string) int {
	root := newRootCmd()
	root.SetArgs([]string{"mcp", "--stdio", "--token", token})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		return 1
	}
	return 0
}

// runServeChild runs the serve command in-process until it is signalled.
//
// The MCP endpoint is another listener, and the test picks its port the way it picks the HTTP one:
// leaving it on the configured default would have every test run contend for one fixed address.
func runServeChild(addr string) int {
	root := newRootCmd()
	root.SetArgs([]string{"serve", "--addr", addr, "--mcp-addr", freeAddrForChild()})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	return 0
}

// freeAddrForChild is a loopback address nothing is listening on: it is bound to find out, then
// released for serve to take. A port that is taken between the two is a lost race, and the server
// failing to bind is what the caller would see.
func freeAddrForChild() string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ""
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().String()
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		// A txtar file cannot build a 100 KB body out of one repeated fragment, and the point of these
		// two is exactly that they are large: one is a shape the parser cannot finish, the other is
		// prose of the same size.
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			// routeid <variable> <match> reads the routes out of the database the script has been
			// writing and exports the id of one of them, so that `route remove` and `route move`
			// can be named. A script asserts on output; it cannot bind it to a name, and a route's
			// id is generated, so this is what puts it in the environment.
			"routeid": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 2 {
					ts.Fatalf("routeid takes a variable and a match")
				}
				ctx := context.Background()
				db, err := store.Open(ctx, ts.Getenv("NIBWIRE_DB"))
				if err != nil {
					ts.Fatalf("open the database: %v", err)
				}
				defer func() { _ = db.Close() }()
				rules, err := db.Routes(ctx)
				if err != nil {
					ts.Fatalf("read the routes: %v", err)
				}
				for _, rule := range rules {
					if rule.Match.String() == args[1] {
						ts.Setenv(args[0], rule.ID)
						return
					}
				}
				ts.Fatalf("no rule matching %q", args[1])
			},
			"writebodies": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg {
					ts.Fatalf("writebodies takes no negation")
				}
				dir := ts.Getenv("WORK")
				for name, content := range map[string]string{
					"pathological.md": strings.Repeat("[a](", 25_000),
					"long.md":         strings.Repeat("Some ordinary words. ", 4000),
				} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
						ts.Fatalf("write %s: %v", name, err)
					}
				}
			},
		},
		Setup: func(env *testscript.Env) error {
			// A fixed secret, and the fake SES log the scripts assert on.
			env.Setenv("SESSION_SECRET", "test-session-secret-with-at-least-32-characters")
			env.Setenv("APP_URL", "https://mail.example.com")
			env.Setenv("SENDING_ENABLED", "true")
			env.Setenv("NIBWIRE_DB", env.WorkDir+"/nibwire.db")
			env.Setenv("NIBWIRE_FAKE_SES_LOG", env.WorkDir+"/sent.jsonl")
			// The stand-in SES identity list. example.net is verified, the way a real deployment's
			// sending domain would be; a script that wants the unverified branch overrides this.
			env.Setenv("NIBWIRE_FAKE_SES_DOMAINS", "example.net")
			return nil
		},
	})
}
