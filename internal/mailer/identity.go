package mailer

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// DNSRecord is one record SES asks the operator to publish (lib/ses.js dnsRecords).
type DNSRecord struct {
	Type  string // CNAME | MX | TXT
	Name  string
	Value string
}

// Identity is one sending identity, as `nibwire domains` prints it and as the pre-send check reads
// it: everything the CLI needs to answer "may this From address send?".
type Identity struct {
	Domain          string
	VerifiedForSend bool
	DKIMStatus      string // NOT_STARTED | PENDING | SUCCESS | FAILED | TEMPORARY_FAILURE
	MailFromDomain  string
	MailFromStatus  string
	Records         []DNSRecord
}

// IdentityReader is where the sending identities come from. Tests inject a StaticIdentity;
// production gets SESIdentity.
type IdentityReader interface {
	Identities(ctx context.Context) ([]Identity, error)
}

// identityCacheTTL, identityReadTimeout and identityPageSize are lib/ses.js CACHE_TTL_MS,
// READ_TIMEOUT_MS and the ListEmailIdentities PageSize, verbatim.
const (
	identityCacheTTL    = 5 * time.Minute
	identityReadTimeout = 5 * time.Second
	identityPageSize    = 100
)

// SESIdentity reads the identities from Amazon SES v2 and caches the list for five minutes, the way
// the dashboard did: a page load or a save must not fan out into one API call per identity.
type SESIdentity struct {
	client *sesv2.Client
	region string

	mu        sync.Mutex
	cached    []Identity
	expiresAt time.Time
}

// NewSESIdentity builds the production reader. It goes through the same credential handling and the
// same endpoint override as NewSES.
func NewSESIdentity(ctx context.Context, options SESOptions) (*SESIdentity, error) {
	cfg, err := awsConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	return &SESIdentity{client: sesv2.NewFromConfig(cfg), region: options.Region}, nil
}

// Identities returns every domain identity, from cache while it is fresh.
func (s *SESIdentity) Identities(ctx context.Context) ([]Identity, error) {
	s.mu.Lock()
	if s.cached != nil && time.Now().Before(s.expiresAt) {
		out := s.cached
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	// Reads only: a read that timed out could still have been applied, and the SDK would retry it.
	readCtx, cancel := context.WithTimeout(ctx, identityReadTimeout)
	defer cancel()

	list, err := s.client.ListEmailIdentities(readCtx, &sesv2.ListEmailIdentitiesInput{
		PageSize: pointer(int32(identityPageSize)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Identity, 0, len(list.EmailIdentities))
	for _, info := range list.EmailIdentities {
		if info.IdentityType != types.IdentityTypeDomain {
			continue // an email-address identity is never what a newsletter sends from
		}
		domain := deref(info.IdentityName)
		got, err := s.client.GetEmailIdentity(readCtx, &sesv2.GetEmailIdentityInput{EmailIdentity: &domain})
		if err != nil {
			return nil, err
		}
		out = append(out, IdentityFromSES(domain, got, s.region))
	}

	s.mu.Lock()
	s.cached, s.expiresAt = out, time.Now().Add(identityCacheTTL)
	s.mu.Unlock()
	return out, nil
}

// Invalidate drops the cache, so `domains --fresh` asks SES again after the operator publishes DNS.
func (s *SESIdentity) Invalidate() {
	s.mu.Lock()
	s.cached, s.expiresAt = nil, time.Time{}
	s.mu.Unlock()
}

// IdentityFromSES turns one GetEmailIdentity answer into an Identity: the DKIM CNAMEs and the
// MAIL FROM MX/TXT rows the operator has to publish (lib/ses.js dnsRecords).
func IdentityFromSES(domain string, got *sesv2.GetEmailIdentityOutput, region string) Identity {
	identity := Identity{Domain: domain, VerifiedForSend: got.VerifiedForSendingStatus}
	if got.DkimAttributes != nil {
		identity.DKIMStatus = string(got.DkimAttributes.Status)
		for _, token := range got.DkimAttributes.Tokens {
			identity.Records = append(identity.Records, DNSRecord{
				Type:  "CNAME",
				Name:  token + "._domainkey." + domain,
				Value: token + ".dkim.amazonses.com",
			})
		}
	}
	if got.MailFromAttributes != nil {
		identity.MailFromDomain = deref(got.MailFromAttributes.MailFromDomain)
		identity.MailFromStatus = string(got.MailFromAttributes.MailFromDomainStatus)
	}
	if identity.MailFromDomain != "" {
		identity.Records = append(identity.Records,
			DNSRecord{
				Type: "MX", Name: identity.MailFromDomain,
				Value: "10 feedback-smtp." + region + ".amazonses.com",
			},
			DNSRecord{
				Type: "TXT", Name: identity.MailFromDomain,
				Value: "v=spf1 include:amazonses.com ~all",
			})
	}
	return identity
}

// StaticIdentity is a fixed list of verified domains, for tests and for the fake sender's
// NIBWIRE_FAKE_SES_DOMAINS.
type StaticIdentity struct{ Domains []string }

// Identities reports every listed domain as verified with DKIM in order: the fake SES does not model
// a half-verified domain, and the code under test only reads VerifiedForSend.
func (s StaticIdentity) Identities(context.Context) ([]Identity, error) {
	out := make([]Identity, 0, len(s.Domains))
	for _, domain := range s.Domains {
		if domain = strings.TrimSpace(domain); domain == "" {
			continue
		}
		out = append(out, Identity{Domain: domain, VerifiedForSend: true, DKIMStatus: "SUCCESS"})
	}
	return out, nil
}

// VerifiedDomains is the shorthand every caller of the pre-send check wants: just the domains.
func VerifiedDomains(identities []Identity) []string {
	out := make([]string, 0, len(identities))
	for _, identity := range identities {
		if identity.VerifiedForSend {
			out = append(out, identity.Domain)
		}
	}
	return out
}
