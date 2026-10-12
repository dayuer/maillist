package mailer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/aws/smithy-go"

	"nibwire/internal/core/ids"
	"nibwire/internal/core/rules"
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
	// cancelOnContext makes Send report the context's error after recording the send; see
	// WithContextCancellation.
	cancelOnContext bool
	// OnSend, when set, is called with the count of sends so far as each send arrives. It is what
	// lets a test cancel a context mid-batch.
	OnSend func(sent int)
}

// FakeOption configures a Fake.
type FakeOption func(*Fake)

// WithLogFile appends one JSON object per send to path. It is how the CLI end-to-end test observes
// that a message really went out.
func WithLogFile(path string) FakeOption {
	return func(f *Fake) { f.logPath = path }
}

// WithContextCancellation makes Send behave like a real HTTP client: the request is recorded as
// accepted and *then* the context's error is reported. That is the failure this exists to test — the
// mail reached SES, and the caller stopped waiting for the answer — and it is the one a fake that
// ignores its context can never produce.
func WithContextCancellation() FakeOption {
	return func(f *Fake) { f.cancelOnContext = true }
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
func (f *Fake) Send(ctx context.Context, email rules.RecipientEmail, tags map[string]string) (string, error) {
	f.mu.Lock()
	onSend, seen := f.OnSend, len(f.sent)
	f.mu.Unlock()
	if onSend != nil {
		onSend(seen)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if failure, ok := f.failures[email.To]; ok {
		return "", &failure
	}
	f.sent = append(f.sent, SentEmail{Email: email, Tags: tags})
	cancelled := f.cancelOnContext && ctx.Err() != nil
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
	if cancelled {
		// Recorded, then reported as unknown: the caller cannot tell this apart from a send SES
		// accepted and answered too late.
		return "", ctx.Err()
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
