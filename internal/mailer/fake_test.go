package mailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/smithy-go"

	"mailcheap/internal/core/rules"
)

func TestFakeRecordsSends(t *testing.T) {
	fake := NewFake()
	id, err := fake.Send(context.Background(),
		rules.RecipientEmail{To: "reader@example.com", Subject: "Hi"},
		map[string]string{"message_id": "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("no message id")
	}
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].Email.To != "reader@example.com" || sent[0].Tags["message_id"] != "m1" {
		t.Fatalf("sent = %+v", sent)
	}
	if fake.SentCount() != 1 {
		t.Fatalf("count = %d", fake.SentCount())
	}
}

// The engine classifies errors by the SES error code, so the fake has to produce something the
// smithy.APIError interface matches — not a plain error with a code in its message.
func TestFakeInjectsSESErrorCodes(t *testing.T) {
	fake := NewFake()
	fake.FailWith("reader@example.com", "ThrottlingException", "Maximum sending rate exceeded")
	if _, err := fake.Send(context.Background(), rules.RecipientEmail{To: "reader@example.com"}, nil); err == nil {
		t.Fatal("no error")
	} else {
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ThrottlingException" {
			t.Fatalf("err = %v", err)
		}
	}

	// A cleared failure sends again.
	fake.ClearFailures()
	if _, err := fake.Send(context.Background(), rules.RecipientEmail{To: "reader@example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	// Another address was never affected.
	if _, err := fake.Send(context.Background(), rules.RecipientEmail{To: "other@example.com"}, nil); err != nil {
		t.Fatal(err)
	}
}

// Writing the log is what the CLI end-to-end test asserts on.
func TestFakeAppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.jsonl")
	fake := NewFake(WithLogFile(path))
	for _, to := range []string{"a@example.com", "b@example.com"} {
		if _, err := fake.Send(context.Background(),
			rules.RecipientEmail{To: to, Subject: "Subject " + to},
			map[string]string{"message_id": "m-" + to, "kind": "campaign"}); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(path) //nolint:gosec // G304: a file this test just created in t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d: %q", len(lines), body)
	}
	if !strings.Contains(lines[1], `"to":"b@example.com"`) || !strings.Contains(lines[1], `"kind":"campaign"`) {
		t.Fatalf("second line = %q", lines[1])
	}
}

// The fake is used from several goroutines by the engine, so its bookkeeping has to hold up.
func TestFakeIsConcurrencySafe(t *testing.T) {
	fake := NewFake()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for n := 0; n < 25; n++ {
				if _, err := fake.Send(context.Background(), rules.RecipientEmail{To: "reader@example.com"}, nil); err != nil {
					t.Errorf("send: %v", err)
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if got := fake.SentCount(); got != 200 {
		t.Fatalf("sent = %d, want 200", got)
	}
}
