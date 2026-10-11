package mailer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/aws/smithy-go"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/rules"
)

// SentEmail is one recorded send.
type SentEmail struct {
	Email rules.RecipientEmail
	Tags  map[string]string
}

// Fake records what would have been sent. It can be told to fail a given address with a given SES
// error code, so the engine's classification is exercised without AWS.
type Fake struct {
	mu       sync.Mutex
	sent     []SentEmail
	failures map[string]APIError
	logPath  string
}

// FakeOption configures a Fake.
type FakeOption func(*Fake)

// WithLogFile appends one JSON object per send to path. It is how the CLI end-to-end test observes
// that a message really went out.
func WithLogFile(path string) FakeOption {
	return func(f *Fake) { f.logPath = path }
}

// NewFake returns an empty Fake.
func NewFake(options ...FakeOption) *Fake {
	fake := &Fake{failures: map[string]APIError{}}
	for _, option := range options {
		option(fake)
	}
	return fake
}

// FailWith makes the next Send to email fail with an SES error of this code.
func (f *Fake) FailWith(email, code, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[email] = APIError{Code: code, Message: message}
}

// ClearFailures stops injecting errors.
func (f *Fake) ClearFailures() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = map[string]APIError{}
}

// Sent returns a copy of everything sent so far.
func (f *Fake) Sent() []SentEmail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentEmail(nil), f.sent...)
}

// SentCount is how many messages went out.
func (f *Fake) SentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// Send records the email, unless that address was told to fail.
func (f *Fake) Send(_ context.Context, email rules.RecipientEmail, tags map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if failure, ok := f.failures[email.To]; ok {
		return "", &failure
	}
	f.sent = append(f.sent, SentEmail{Email: email, Tags: tags})
	if f.logPath != "" {
		line, err := json.Marshal(map[string]string{
			"to": email.To, "from": email.From, "subject": email.Subject,
			"message_id": tags["message_id"], "kind": tags["kind"],
		})
		if err != nil {
			return "", err
		}
		handle, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("append send log: %w", err)
		}
		defer func() { _ = handle.Close() }()
		if _, err := handle.Write(append(line, '\n')); err != nil {
			return "", fmt.Errorf("append send log: %w", err)
		}
	}
	return "fake-" + ids.New(), nil
}

// APIError is shaped like the AWS SDK's, so the engine classifies a fake failure exactly as it
// classifies a real one: rules.SendErrorAction reads the code, and the message is passed through.
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// ErrorCode is what smithy.APIError exposes, and what the engine reads.
func (e *APIError) ErrorCode() string { return e.Code }

// ErrorMessage is the human-readable half.
func (e *APIError) ErrorMessage() string { return e.Message }

// ErrorFault completes smithy.APIError. Every code the engine cares about is the caller's fault.
func (e *APIError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }
