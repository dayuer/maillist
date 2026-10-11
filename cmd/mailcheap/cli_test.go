package main

import (
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain registers this binary as "mailcheap" so the scripts in testdata/script can run it. The
// other TestMain the package would need does not exist: main() is the whole program.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){"mailcheap": func() { main() }})
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
			return nil
		},
	})
}
