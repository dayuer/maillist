package httpsrv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"nibwire/internal/core/sns"
)

// certFetchTimeout and maxCertificateBytes bound the one outbound request the verifier makes.
// Upstream used a 5 s abort signal; the size cap is this port's addition — a hostile or broken
// endpoint must not be able to hand the process an arbitrarily large body.
const (
	certFetchTimeout    = 5 * time.Second
	maxCertificateBytes = 64 * 1024
)

// CertificateFetcher implements sns.CertFetcher over HTTP. It only ever fetches a URL that passes
// ValidSigningCertURL, so someone who can post to the webhook cannot make the server fetch an
// address of their choosing.
func CertificateFetcher(client *http.Client) sns.CertFetcher {
	if client == nil {
		client = &http.Client{
			Timeout: certFetchTimeout,
			// Upstream fetches with redirect: "error"; following one would leave the allow list.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return func(ctx context.Context, url string) ([]byte, error) {
		if !sns.ValidSigningCertURL(url) {
			return nil, fmt.Errorf("refusing to fetch %q", url)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("certificate request failed with %d", response.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxCertificateBytes+1))
		if err != nil {
			return nil, err
		}
		if len(body) > maxCertificateBytes {
			return nil, errors.New("certificate is larger than 64 KiB")
		}
		return body, nil
	}
}
