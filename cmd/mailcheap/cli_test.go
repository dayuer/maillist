package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain registers this binary as "mailcheap" so the scripts in testdata/script can run it, and
// lets TestServeShutsDownCleanly run the same binary as a `mailcheap serve` child.
func TestMain(m *testing.M) {
	if addr := os.Getenv("MAILCHEAP_TEST_SERVE_ADDR"); addr != "" {
		os.Exit(runServeChild(addr))
	}
	testscript.Main(m, map[string]func(){"mailcheap": func() { main() }})
}

// runServeChild runs the serve command in-process until it is signalled.
func runServeChild(addr string) int {
	root := newRootCmd()
	root.SetArgs([]string{"serve", "--addr", addr})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		return 1
	}
	return 0
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			// A fixed secret, and the fake SES log the scripts assert on.
			env.Setenv("SESSION_SECRET", "test-session-secret-with-at-least-32-characters")
			env.Setenv("APP_URL", "https://mail.example.com")
			env.Setenv("SENDING_ENABLED", "true")
			env.Setenv("MAILCHEAP_DB", env.WorkDir+"/mailcheap.db")
			env.Setenv("MAILCHEAP_FAKE_SES_LOG", env.WorkDir+"/sent.jsonl")
			// The stand-in SES identity list. example.net is verified, the way a real deployment's
			// sending domain would be; a script that wants the unverified branch overrides this.
			env.Setenv("MAILCHEAP_FAKE_SES_DOMAINS", "example.net")
			return nil
		},
	})
}
