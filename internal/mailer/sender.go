// Package mailer sends prepared emails through Amazon SES, and knows how to pretend to.
package mailer

import (
	"context"

	"nibwire/internal/core/rules"
)

// Sender delivers one recipient's email. Implemented by SESSender (production) and Fake (tests).
type Sender interface {
	// Send returns SES's own message id, which SNS events then reference.
	Send(ctx context.Context, email rules.RecipientEmail, tags map[string]string) (string, error)
}
