// Package creds issues and checks the bearer credentials of the v1 API and MCP.
package creds

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

var (
	tokenPattern  = regexp.MustCompile(`^mc_[A-Za-z0-9_-]{43}$`)
	bearerPattern = regexp.MustCompile(`(?i)^Bearer\s+(\S+)$`)
)

// LastUsedResolution limits last_used_at writes to one per credential per minute.
const LastUsedResolution = time.Minute

// Generated is a new credential. Token is shown once; only Hash is stored.
type Generated struct {
	Token  string
	Prefix string
	Hash   string
}

// Hash is the SHA-256 hex digest stored in place of the token: a database leak doesn't leak usable tokens.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Generate returns a token carrying 32 random bytes.
func Generate() Generated {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := "mc_" + base64.RawURLEncoding.EncodeToString(b)
	return Generated{Token: token, Prefix: token[:10] + "…", Hash: Hash(token)}
}

// ReadBearer returns the token of a well-formed "Authorization: Bearer mc_…" header.
func ReadBearer(authorization string) (string, bool) {
	m := bearerPattern.FindStringSubmatch(strings.TrimSpace(authorization))
	if m == nil || !tokenPattern.MatchString(m[1]) {
		return "", false
	}
	return m[1], true
}

// ShouldTouchLastUsed reports whether last_used_at is stale enough to rewrite.
func ShouldTouchLastUsed(lastUsed *time.Time, now time.Time) bool {
	return lastUsed == nil || now.Sub(*lastUsed) >= LastUsedResolution
}
