package mcpsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mailcheap/internal/creds"
)

// tokenHashKey is how the authenticated token travels from the middleware to the server builder.
type tokenHashKey struct{}

// sessionTimeout is how long an idle MCP session is kept. A client that goes away without saying so
// leaves a session behind for this long before the transport reclaims it.
const sessionTimeout = 10 * time.Minute

// HTTPHandler is the `/mcp` endpoint: the Streamable HTTP transport with a Bearer credential in
// front of it.
//
// The credential is checked on **every** request, not once per session. A streamable session spans
// many requests, and the token is looked up in the database for each of them (D8) — which is what
// makes revoking a row stop the calls that follow, in a session that is already open.
// maxRequestBytes is the endpoint's body limit. The transport's own default is 4 MiB, which is less
// than upload_image's own allowance: a 4 MiB picture is ~5.6 MiB of base64 before the JSON envelope,
// so an agent working over HTTP could not upload what the tool promises. 10 MiB fits that with room
// for the envelope, and a body past it is refused with 413 — by the transport, during the read, so
// Content-Length, chunked and HTTP/2 are all capped the same way.
const maxRequestBytes = 10 << 20

func (s *Server) HTTPHandler() http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		hash, _ := r.Context().Value(tokenHashKey{}).(string)
		if hash == "" {
			// Unreachable through this middleware; a nil server is how the transport answers 400
			// rather than serving an anonymous session.
			return nil
		}
		return s.For(hash)
	}, &mcp.StreamableHTTPOptions{
		Logger: s.deps.Log, SessionTimeout: sessionTimeout, MaxRequestBodyBytes: maxRequestBytes,
	})
	return s.authenticate(inner)
}

// authenticate is the Bearer gate. It answers 401 for a token that is missing or unknown and 403 for
// one of the other kind, and only then lets the MCP transport see the request.
//
// It is the same three steps the v1 API takes (read the header, hash it, look the hash up), because
// the two entries share one credential model: the kind is what says which of them a token is for.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := creds.ReadBearer(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w, "Send an Authorization: Bearer <mcp token> header")
			return
		}
		hash := creds.Hash(token)
		credential, err := s.deps.Store.CredentialByHash(r.Context(), hash)
		if err != nil {
			unauthorized(w, "Invalid token")
			return
		}
		if credential.Kind != creds.KindMCP {
			writeJSONError(w, http.StatusForbidden,
				"This token is an API key: it works on /api/v1, not on the MCP endpoint")
			return
		}
		now := time.Now().UTC()
		if creds.ShouldTouchLastUsed(credential.LastUsedAt, now) {
			// Bookkeeping: a failure here must not fail the request.
			if err := s.deps.Store.TouchCredential(r.Context(), credential.ID, now); err != nil {
				s.deps.Log.Warn("could not update last_used_at", "credential", credential.ID, "error", err)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenHashKey{}, hash)))
	})
}

func unauthorized(w http.ResponseWriter, message string) {
	// A 401 without this header is a 401 a client cannot act on: it says "authenticate" and not
	// "with what".
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSONError(w, http.StatusUnauthorized, message)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
