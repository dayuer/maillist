package creds

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGenerateCarries32BytesPrefixAndHash(t *testing.T) {
	g := Generate()
	if !regexp.MustCompile(`^mc_[A-Za-z0-9_-]{43}$`).MatchString(g.Token) {
		t.Fatalf("token %q", g.Token)
	}
	if b, _ := base64.RawURLEncoding.DecodeString(g.Token[3:]); len(b) != 32 {
		t.Fatalf("decoded %d bytes", len(b))
	}
	if g.Prefix != g.Token[:10]+"…" {
		t.Fatalf("prefix %q", g.Prefix)
	}
	sum := sha256.Sum256([]byte(g.Token))
	if g.Hash != hex.EncodeToString(sum[:]) {
		t.Fatal("hash mismatch")
	}
}

func TestGeneratedTokensAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		g := Generate()
		if seen[g.Token] || seen[g.Hash] {
			t.Fatal("duplicate")
		}
		seen[g.Token], seen[g.Hash] = true, true
	}
}

func TestHashIsDeterministicAndSensitive(t *testing.T) {
	g := Generate()
	if Hash(g.Token) != g.Hash || Hash(g.Token[:len(g.Token)-1]+"x") == g.Hash {
		t.Fatal("hash not deterministic or not sensitive")
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(g.Hash) {
		t.Fatalf("hash %q is not 64 lowercase hex characters", g.Hash)
	}
}

func TestOnlyWellFormedBearerTokensAreRead(t *testing.T) {
	g := Generate()
	for _, h := range []string{"Bearer " + g.Token, "bearer   " + g.Token + "  "} {
		if tok, ok := ReadBearer(h); !ok || tok != g.Token {
			t.Errorf("ReadBearer(%q) = %q %v", h, tok, ok)
		}
	}
	for _, h := range []string{
		"", "Bearer ", g.Token, "Basic " + g.Token, "Bearer " + g.Token + " extra",
		"Bearer " + g.Token[:len(g.Token)-1], "Bearer " + g.Token + "=", "Bearer sk_" + g.Token[3:],
		"Bearer " + strings.Replace(strings.ToUpper(g.Token), "MC_", "mc_", 1) + "!",
	} {
		if _, ok := ReadBearer(h); ok {
			t.Errorf("accepted %q", h)
		}
	}
}

func TestLastUsedIsRefreshedAtMostOncePerMinute(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	if !ShouldTouchLastUsed(nil, now) || ShouldTouchLastUsed(at(59*time.Second), now) ||
		!ShouldTouchLastUsed(at(time.Minute), now) || !ShouldTouchLastUsed(at(time.Hour), now) {
		t.Fatal("wrong refresh decision")
	}
}
