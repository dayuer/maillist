package sign

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	secret    = "test-session-secret-with-at-least-32-characters"
	base      = "https://mail.example.com"
	messageID = "66f1a2b3c4d5e6f7a8b9c0d1"
	contactID = "64f1a2b3c4d5e6f7a8b9c0d1"
	otherID   = "64f1a2b3c4d5e6f7a8b9c0d9"
	nlID      = "74f1a2b3c4d5e6f7a8b9c0d2"
)

var ref = Ref{ContactID: contactID, NewsletterID: nlID}

func mustSigner(t *testing.T, s string) *Signer {
	t.Helper()
	sg, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

func tokenOf(u string) string { return u[strings.LastIndex(u, "/")+1:] }

// Golden values produced by the original Node implementation (marclou/mailcheap@67d13b0).
func TestGoldenVectorsMatchOriginal(t *testing.T) {
	s := mustSigner(t, secret)
	if got := s.OpenURL(base, messageID); got != "https://mail.example.com/o/66f1a2b3c4d5e6f7a8b9c0d1.Kve15SMFSQgfp3EcKblC_w" {
		t.Errorf("open = %s", got)
	}
	if got := s.ClickURL(base, messageID, "https://example.org/?utm_source=newsletter&ref=1"); got != "https://mail.example.com/c/66f1a2b3c4d5e6f7a8b9c0d1.eYRYRA0c6sDtnw0fNtufPs.aHR0cHM6Ly9leGFtcGxlLm9yZy8_dXRtX3NvdXJjZT1uZXdzbGV0dGVyJnJlZj0x" {
		t.Errorf("click = %s", got)
	}
	if got, _ := s.UnsubscribeToken(ref); got != "eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiJ9.apuRlFjYJ2ns-renATb2e45fLpGIBDe9xm_yHAJ7_rE" {
		t.Errorf("unsubscribe = %s", got)
	}
	at := time.UnixMilli(1791000000000)
	if got, _ := s.ContactToken(InfoConfirm, ref, at); got != "eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiIsInQiOjE3OTEwMDAwMDB9.cNcx9tj972399hE6WeVQw9FNKAxI_iUvKZvXRtT4n78" {
		t.Errorf("confirm = %s", got)
	}
	if got, _ := s.ContactToken(InfoKeep, ref, at); got != "eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiIsInQiOjE3OTEwMDAwMDB9.a2AcpJdkmOh8VfUsPyjKvRH4JZ6eC04BQNDDqS24ceI" {
		t.Errorf("keep = %s", got)
	}
}

func TestNewRejectsEmptySecret(t *testing.T) {
	if _, err := New(""); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAndClickCannotBeForged(t *testing.T) {
	s := mustSigner(t, secret)
	open := tokenOf(s.OpenURL(base, messageID))
	if id, ok := s.ReadOpenToken(open); !ok || id != messageID {
		t.Fatalf("open round trip = %q %v", id, ok)
	}
	if _, ok := s.ReadOpenToken(strings.Replace(open, messageID, "66f1a2b3c4d5e6f7a8b9c0d2", 1)); ok {
		t.Fatal("another message id must not verify")
	}

	target := "https://example.org/?utm_source=newsletter&ref=1"
	click := tokenOf(s.ClickURL(base, messageID, target))
	if c, ok := s.ReadClickToken(click); !ok || c != (Click{MessageID: messageID, URL: target, Signed: true}) {
		t.Fatalf("click round trip = %+v %v", c, ok)
	}
	parts := strings.Split(click, ".")
	evil := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("https://evil.example"))
	if c, ok := s.ReadClickToken(evil); !ok || c.Signed {
		t.Fatal("another destination must decode but not be signed")
	}
	js := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("javascript:alert(1)"))
	if _, ok := s.ReadClickToken(js); ok {
		t.Fatal("non-http destinations must be rejected")
	}
	long := parts[0] + "." + parts[1] + "." + strings.Repeat("a", 4001)
	if _, ok := s.ReadClickToken(long); ok {
		t.Fatal("targets longer than 4000 characters must be rejected")
	}
	if _, ok := s.ReadClickToken("not-a-token"); ok {
		t.Fatal("garbage must be rejected")
	}
}

func TestUnsubscribeRoundTripAndCaseFolding(t *testing.T) {
	s := mustSigner(t, secret)
	tok, err := s.UnsubscribeToken(ref)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := s.VerifyUnsubscribe(tok); !ok || r != ref {
		t.Fatalf("round trip = %+v %v", r, ok)
	}
	upper, _ := s.UnsubscribeToken(Ref{ContactID: strings.ToUpper(contactID), NewsletterID: nlID})
	if upper != tok {
		t.Fatal("uppercase hex must produce the same token")
	}
}

func TestUnsubscribeRejectsInvalidReferences(t *testing.T) {
	s := mustSigner(t, secret)
	for _, r := range []Ref{
		{ContactID: "not-an-object-id", NewsletterID: nlID},
		{ContactID: contactID, NewsletterID: ""},
		{ContactID: contactID, NewsletterID: nlID + "0"},
		{ContactID: "../../admin", NewsletterID: nlID},
	} {
		if _, err := s.UnsubscribeToken(r); !errors.Is(err, ErrInvalidReference) {
			t.Errorf("%+v: err = %v", r, err)
		}
	}
}

// signLikeServer signs an arbitrary JSON payload with the unsubscribe key, as the server would.
func signLikeServer(json, withSecret string) string {
	key, _ := hkdf.Key(sha256.New, []byte(withSecret), nil, InfoUnsubscribe, 32)
	body := base64.RawURLEncoding.EncodeToString([]byte(json))
	h := hmac.New(sha256.New, key)
	h.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func TestUnsubscribeRejectsTamperedForgedMalformed(t *testing.T) {
	s := mustSigner(t, secret)
	tok, _ := s.UnsubscribeToken(ref)
	body, sig, _ := strings.Cut(tok, ".")
	swapped := base64.RawURLEncoding.EncodeToString([]byte(`{"c":"` + otherID + `","n":"` + nlID + `"}`))
	flipped := sig[:len(sig)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(sig, "A")]
	for _, bad := range []string{
		"", body, body + ".", "." + sig, tok + ".extra", body + ".." + sig, tok + "=", " " + tok,
		swapped + "." + sig, body + "." + flipped, strings.Repeat("a", 201) + "." + sig,
		signLikeServer(`{"c":"`+contactID+`"}`, secret),
		signLikeServer(`{"c":["`+contactID+`"],"n":"`+nlID+`"}`, secret),
		signLikeServer(`{"c":"`+contactID+`","n":"everything"}`, secret),
		signLikeServer(`["`+contactID+`","`+nlID+`"]`, secret),
		signLikeServer("null", secret),
		signLikeServer("not json", secret),
		signLikeServer(`{"__proto__":{"c":"`+contactID+`","n":"`+nlID+`"}}`, secret),
		signLikeServer(`{"c":"`+contactID+`","n":"`+nlID+`"}`, "another-secret-with-at-least-32-characters"),
	} {
		if _, ok := s.VerifyUnsubscribe(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if r, ok := s.VerifyUnsubscribe(signLikeServer(`{"c":"`+contactID+`","n":"`+nlID+`"}`, secret)); !ok || r != ref {
		t.Fatal("a correctly signed payload must verify")
	}
}

func TestRotatingSecretInvalidatesLinks(t *testing.T) {
	tok, _ := mustSigner(t, secret).UnsubscribeToken(ref)
	if _, ok := mustSigner(t, "rotated-session-secret-with-at-least-32-chars").VerifyUnsubscribe(tok); ok {
		t.Fatal("rotated secret must reject old token")
	}
}

func TestConfirmExpiresAfter30Days(t *testing.T) {
	s := mustSigner(t, secret)
	now := time.Now()
	tok, _ := s.ContactToken(InfoConfirm, ref, now)
	if r, ok := s.VerifyContactToken(InfoConfirm, tok, ConfirmMaxAge, now.Add(24*time.Hour)); !ok || r != ref {
		t.Fatal("fresh confirm token must verify")
	}
	if _, ok := s.VerifyContactToken(InfoConfirm, tok, ConfirmMaxAge, now.Add(31*24*time.Hour)); ok {
		t.Fatal("31-day-old confirm token must be rejected")
	}
}

// tests/confirmation.test.mjs "a confirmation link names one contact and stops working after 30
// days", second half: a signature issued for one contact can't be reused for another.
func TestConfirmTokensCannotBeReusedForAnotherContact(t *testing.T) {
	s := mustSigner(t, secret)
	now := time.UnixMilli(1791000000000)
	tok, _ := s.ContactToken(InfoConfirm, ref, now)
	payload, signature, _ := strings.Cut(tok, ".")

	other := base64.RawURLEncoding.EncodeToString([]byte(
		`{"c":"` + otherID + `","n":"` + nlID + `","t":1791000000}`))
	if _, ok := s.VerifyContactToken(InfoConfirm, other+"."+signature, ConfirmMaxAge, now); ok {
		t.Error("another contact must not reuse the signature")
	}
	if _, ok := s.VerifyContactToken(InfoConfirm, payload+"."+signature[1:]+"x", ConfirmMaxAge, now); ok {
		t.Error("a shifted signature must not verify")
	}
	if _, ok := s.VerifyContactToken(InfoConfirm, "not-a-token", ConfirmMaxAge, now); ok {
		t.Error("garbage must not verify")
	}
}

func TestLinkKindsCannotStandInForEachOther(t *testing.T) {
	s := mustSigner(t, secret)
	now := time.Now()
	unsub, _ := s.UnsubscribeToken(ref)
	confirm, _ := s.ContactToken(InfoConfirm, ref, now)
	keep, _ := s.ContactToken(InfoKeep, ref, now)
	if _, ok := s.VerifyContactToken(InfoConfirm, unsub, ConfirmMaxAge, now); ok {
		t.Error("unsubscribe token confirmed a subscription")
	}
	if _, ok := s.VerifyUnsubscribe(confirm); ok {
		t.Error("confirm token unsubscribed")
	}
	if _, ok := s.VerifyContactToken(InfoConfirm, keep, ConfirmMaxAge, now); ok {
		t.Error("keep token confirmed a subscription")
	}
}

func TestUnsubscribeLinksFollowRFC8058(t *testing.T) {
	s := mustSigner(t, secret)
	l, err := s.UnsubscribeLinksFor(base, ref)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := s.UnsubscribeToken(ref)
	if l.PageURL != base+"/unsubscribe?token="+tok {
		t.Errorf("page = %s", l.PageURL)
	}
	if l.Headers["List-Unsubscribe"] != "<"+base+"/api/unsubscribe?token="+tok+">" {
		t.Errorf("header = %s", l.Headers["List-Unsubscribe"])
	}
	if l.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Errorf("post = %s", l.Headers["List-Unsubscribe-Post"])
	}
	if strings.Contains(l.PageURL, "@") {
		t.Error("links must never include the email address")
	}
}
