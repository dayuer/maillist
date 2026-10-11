package mailer

import (
	"context"
	"errors"
	"fmt"
	"sort"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"mailcheap/internal/core/rules"
)

// SESSender sends through Amazon SES v2. It is the production Sender.
type SESSender struct {
	client    *sesv2.Client
	configSet string
	sendingOn bool
}

// NewSES builds the production Sender from the standard AWS credential chain (SES_ACCESS_KEY_ID and
// SES_SECRET_ACCESS_KEY are picked up by the SDK's own environment provider).
func NewSES(ctx context.Context, region, configSet string, sendingOn bool) (*SESSender, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &SESSender{client: sesv2.NewFromConfig(cfg), configSet: configSet, sendingOn: sendingOn}, nil
}

// Send is upstream sendSesEmail: the whole email goes as Simple content, the list headers travel as
// named Headers, and the message tags are what SNS echoes back in events.
func (s *SESSender) Send(ctx context.Context, email rules.RecipientEmail, tags map[string]string) (string, error) {
	if !s.sendingOn {
		// Upstream's exact wording: rules.SendErrorAction tells this apart from a real failure by
		// the "Sending is off" prefix, so the capital letter is load-bearing.
		return "", errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording drives rules.SendErrorAction
	}
	input := &sesv2.SendEmailInput{
		FromEmailAddress: &email.From,
		Destination:      &types.Destination{ToAddresses: []string{email.To}},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: &email.Subject, Charset: pointer("UTF-8")},
				Body: &types.Body{
					Html: &types.Content{Data: &email.HTML, Charset: pointer("UTF-8")},
					Text: &types.Content{Data: &email.Text, Charset: pointer("UTF-8")},
				},
				Headers: headersOf(email.Headers),
			},
		},
	}
	if email.ReplyTo != "" {
		input.ReplyToAddresses = []string{email.ReplyTo}
	}
	if s.configSet != "" {
		input.ConfigurationSetName = &s.configSet
	}
	for _, name := range sortedKeys(tags) {
		input.EmailTags = append(input.EmailTags, types.MessageTag{Name: pointer(name), Value: pointer(tags[name])})
	}
	result, err := s.client.SendEmail(ctx, input)
	if err != nil {
		return "", err
	}
	return deref(result.MessageId), nil
}

// headersOf turns the List-Unsubscribe pair and Feedback-ID into SES named headers. Sorting makes
// the request body deterministic, which keeps the tests readable.
func headersOf(headers map[string]string) []types.MessageHeader {
	names := sortedKeys(headers)
	out := make([]types.MessageHeader, 0, len(names))
	for _, name := range names {
		out = append(out, types.MessageHeader{Name: pointer(name), Value: pointer(headers[name])})
	}
	return out
}

func sortedKeys(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func pointer[T any](value T) *T { return &value }

func deref[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
