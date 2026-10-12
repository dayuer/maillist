package httpsrv

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nibwire/internal/creds"
)

// The v1 API tests send JSON rather than form posts, and read status codes and error strings back.

func newJSONRequest(t *testing.T, method, path, payload string) *http.Request {
	t.Helper()
	// An io.Reader, not a *strings.Reader: a typed nil in an interface is not nil to NewRequest.
	var body io.Reader
	if payload != "" {
		body = strings.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/141.0")
	if payload != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

// do runs one request through the server, as a client would.
func (s *testServer) do(request *http.Request) *http.Response {
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	return recorder.Result()
}

// status runs a request and returns only its code, closing the body.
func status(t *testing.T, response *http.Response) int {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}

// errorOf reads the {"error": …} a failed API call answers with.
func errorOf(t *testing.T, response *http.Response) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Error
}

// credentialID is the row a token belongs to, so a test can look at last_used_at.
func (s *testServer) credentialID(t *testing.T, token string) string {
	t.Helper()
	credential, err := s.store.CredentialByHash(context.Background(), creds.Hash(token))
	if err != nil {
		t.Fatal(err)
	}
	return credential.ID
}

// lastUsedAt is when a credential was last used, or nil.
func (s *testServer) lastUsedAt(t *testing.T, id string) *time.Time {
	t.Helper()
	credential, err := s.store.Credential(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return credential.LastUsedAt
}

// getFrom sends a GET with a token, to a host of the test's choosing: the archive-host checks need
// the Host header to be the one a reader would type.
func (s *testServer) getFrom(t *testing.T, path, host, token string) *http.Response {
	t.Helper()
	request := newJSONRequest(t, http.MethodGet, path, "")
	request.Host = host
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return s.do(request)
}
