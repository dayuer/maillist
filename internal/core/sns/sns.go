// Package sns verifies Amazon SNS notifications. It does no I/O itself: the HTTP downloader is
// injected as a CertFetcher, because core is pure logic (no net/http).
package sns

import (
	"context"
	"crypto"
	"crypto/rsa"
	_ "crypto/sha1"   //nolint:gosec // registers crypto.SHA1: SNS still sends SignatureVersion 1 with RSA-SHA1.
	_ "crypto/sha256" // registers crypto.SHA256 for SignatureVersion 2.
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Message is the SNS JSON body. Every field is a string; SignatureVersion is "1" or "2".
type Message map[string]string

// Exactly sns.<region>.amazonaws.com: a looser pattern also matched S3 bucket hosts like
// sns.s3.amazonaws.com.
var awsSNSHost = regexp.MustCompile(`^sns\.[a-z]{2}(?:-[a-z]+)+-\d+\.amazonaws\.com$`)

var signingCertPath = regexp.MustCompile(`(?i)^/SimpleNotificationService-[a-z0-9]+\.pem$`)

var pemPattern = regexp.MustCompile(`-----BEGIN (CERTIFICATE|PUBLIC KEY)-----`)

// signatureAlgorithms maps SignatureVersion to its hash.
var signatureAlgorithms = map[string]crypto.Hash{"1": crypto.SHA1, "2": crypto.SHA256}

// subscriptionKeys are shared by the two confirmation message types.
var subscriptionKeys = []string{"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"}

// AWS spec: keys in byte order, Subject only when the notification has one.
var signedKeys = map[string][]string{
	"Notification":             {"Message", "MessageId", "Subject", "Timestamp", "TopicArn", "Type"},
	"SubscriptionConfirmation": subscriptionKeys,
	"UnsubscribeConfirmation":  subscriptionKeys,
}

var optionalKeys = map[string]bool{"Subject": true}

const maxCachedCertificates = 20

// ErrInvalidMessage is returned for a message of an unknown type, with a missing required field, or
// with a signing certificate URL that isn't SNS's.
var ErrInvalidMessage = errors.New("invalid SNS message")

// ErrInvalidSignature is returned when the signature doesn't match.
var ErrInvalidSignature = errors.New("SNS signature does not verify")

func parseSNSURL(value string) *url.URL {
	u, err := url.Parse(value)
	if err != nil {
		return nil
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return nil
	}
	if !awsSNSHost.MatchString(strings.ToLower(u.Hostname())) {
		return nil
	}
	return u
}

// ValidSigningCertURL reports whether a URL may be fetched as an SNS signing certificate.
func ValidSigningCertURL(value string) bool {
	u := parseSNSURL(value)
	return u != nil && signingCertPath.MatchString(u.Path) && u.RawQuery == "" && u.Fragment == ""
}

// ValidSubscribeURL reports whether a URL is an SNS SubscribeURL worth confirming.
func ValidSubscribeURL(value string) bool {
	return parseSNSURL(value) != nil
}

// SigningString builds the string SNS signs, or ok=false when a required field is missing.
func SigningString(m Message) (string, bool) {
	keys, ok := signedKeys[m["Type"]]
	if !ok {
		return "", false
	}
	var b strings.Builder
	for _, key := range keys {
		value, present := m[key]
		if !present {
			if optionalKeys[key] {
				continue
			}
			return "", false
		}
		b.WriteString(key)
		b.WriteByte('\n')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	return b.String(), true
}

// CertFetcher downloads a PEM: a certificate, or a bare public key. The real downloader lives in
// httpsrv (phase 3) and is injected here.
type CertFetcher func(ctx context.Context, url string) ([]byte, error)

// Verifier caches certificates per URL and retries failed downloads.
//
// A failed download is not remembered: the retry is upstream's behaviour and its test asserts it
// ("certificates are cached per URL and failed downloads are retried"). The cost of that choice —
// the verifier runs before the signature is checked, so an unauthenticated caller decides how many
// outbound requests the process makes — is bounded at the webhook instead, by a per-source rate
// limit (the phase 1-3 audit's S-I1).
type Verifier struct {
	fetch CertFetcher
	mu    sync.Mutex
	cache map[string]crypto.PublicKey
	locks map[string]*sync.Mutex
}

// NewVerifier returns a Verifier that downloads certificates with fetch.
func NewVerifier(fetch CertFetcher) *Verifier {
	return &Verifier{
		fetch: fetch,
		cache: map[string]crypto.PublicKey{},
		locks: map[string]*sync.Mutex{},
	}
}

// Verify checks the signature of an SNS message. SignatureVersion 1 is SHA-1, 2 is SHA-256.
func (v *Verifier) Verify(ctx context.Context, m Message) error {
	hash, ok := signatureAlgorithms[m["SignatureVersion"]]
	if !ok {
		return fmt.Errorf("%w: unknown SignatureVersion %q", ErrInvalidMessage, m["SignatureVersion"])
	}
	signingString, ok := SigningString(m)
	if !ok || m["Signature"] == "" {
		return fmt.Errorf("%w: unusable signing string or signature", ErrInvalidMessage)
	}
	if !ValidSigningCertURL(m["SigningCertURL"]) {
		return fmt.Errorf("%w: bad SigningCertURL", ErrInvalidMessage)
	}
	publicKey, err := v.certificate(ctx, m["SigningCertURL"])
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(m["Signature"])
	if err != nil {
		return fmt.Errorf("%w: signature is not base64: %w", ErrInvalidMessage, err)
	}
	rsaKey, ok := publicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("%w: signing key is not RSA", ErrInvalidSignature)
	}
	digest := hash.New()
	digest.Write([]byte(signingString))
	if err := rsa.VerifyPKCS1v15(rsaKey, hash, digest.Sum(nil), signature); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}
	return nil
}

func (v *Verifier) certificate(ctx context.Context, certURL string) (crypto.PublicKey, error) {
	if key, ok := v.cached(certURL); ok {
		return key, nil
	}
	lock := v.urlLock(certURL)
	lock.Lock()
	defer lock.Unlock()
	if key, ok := v.cached(certURL); ok {
		return key, nil
	}

	pemBytes, err := v.fetch(ctx, certURL)
	if err != nil {
		return nil, err
	}
	if !pemPattern.Match(pemBytes) {
		return nil, fmt.Errorf("%w: certificate is not PEM", ErrInvalidMessage)
	}
	key, err := parsePublicKey(pemBytes)
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.cache) >= maxCachedCertificates {
		// Both maps are keyed by certificate URL, so they are bounded together: the locks map grew
		// with every URL ever seen, even as the cache was emptied.
		v.cache = map[string]crypto.PublicKey{}
		v.locks = map[string]*sync.Mutex{}
	}
	v.cache[certURL] = key
	return key, nil
}

func (v *Verifier) cached(certURL string) (crypto.PublicKey, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key, ok := v.cache[certURL]
	return key, ok
}

func (v *Verifier) urlLock(certURL string) *sync.Mutex {
	v.mu.Lock()
	defer v.mu.Unlock()
	lock, ok := v.locks[certURL]
	if !ok {
		if len(v.locks) >= maxCachedCertificates {
			// The cache is trimmed only when it fills, and a URL that fails to download never reaches
			// it — so without a bound of its own, every URL an unauthenticated caller names leaves a
			// mutex behind for the life of the process (phase 9 #39). Dropping the map costs at most a
			// duplicated download while a URL is in flight: a lock that is not there serialises
			// nothing, and a failed download is not remembered either way.
			v.locks = map[string]*sync.Mutex{}
		}
		lock = &sync.Mutex{}
		v.locks[certURL] = lock
	}
	return lock
}

func parsePublicKey(pemBytes []byte) (crypto.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: certificate is not PEM", ErrInvalidMessage)
	}
	switch block.Type {
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
		}
		return cert.PublicKey, nil
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("%w: unexpected PEM type %q", ErrInvalidMessage, block.Type)
	}
}
