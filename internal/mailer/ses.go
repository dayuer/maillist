package mailer

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
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

// SESOptions is where SES is and who is asking.
type SESOptions struct {
	Region string
	// Endpoint replaces the service address (SES_ENDPOINT). The compose smoke stack points it at a
	// fake SES, so the request this code builds is the request under test: real signing, real
	// serialisation, no AWS.
	Endpoint string
	// AccessKeyID and SecretKey are SES_ACCESS_KEY_ID and SES_SECRET_ACCESS_KEY, the variables this
	// project documents. Empty means the SDK's own chain — an instance role, a task role, or the
	// shared config file — which is what a deployment on AWS should use.
	//
	// They are passed explicitly because the SDK's environment provider reads AWS_ACCESS_KEY_ID, not
	// SES_ACCESS_KEY_ID: leaving it to the chain meant these variables were ignored and the chain
	// went looking for an instance role that is not there — which shows up as a five-second
	// credential timeout on the first request, not as a missing setting.
	AccessKeyID string
	SecretKey   string
}

// NewSES builds the production Sender.
func NewSES(ctx context.Context, options SESOptions, configSet string, sendingOn bool) (*SESSender, error) {
	cfg, err := awsConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	return &SESSender{client: sesv2.NewFromConfig(cfg), configSet: configSet, sendingOn: sendingOn}, nil
}

// awsConfig is the one place a client is built, so neither the endpoint override nor the credentials
// can be forgotten in one of the two constructors.
func awsConfig(ctx context.Context, options SESOptions) (aws.Config, error) {
	loaders := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(options.Region)}
	if options.Endpoint != "" {
		loaders = append(loaders, awsconfig.WithBaseEndpoint(options.Endpoint))
	}
	if options.AccessKeyID != "" && options.SecretKey != "" {
		loaders = append(loaders, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(options.AccessKeyID, options.SecretKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config: %w", err)
	}
	return cfg, nil
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
