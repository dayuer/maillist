package sign

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"

	"mailcheap/internal/core/ids"
)

// base64url JSON payload + "." + full base64url HMAC-SHA256 (43 characters).
var contactTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}\.[A-Za-z0-9_-]{43}$`)

// Max ages of the expiring contact links.
const (
	ConfirmMaxAge = 30 * 24 * time.Hour
	KeepMaxAge    = 90 * 24 * time.Hour
)

type payload struct {
	C *string  `json:"c"`
	N *string  `json:"n"`
	T *float64 `json:"t,omitempty"`
}

func normalizeRef(r Ref) (Ref, error) {
	r = Ref{ContactID: strings.ToLower(r.ContactID), NewsletterID: strings.ToLower(r.NewsletterID)}
	if !ids.Valid(r.ContactID) || !ids.Valid(r.NewsletterID) {
		return Ref{}, ErrInvalidReference
	}
	return r, nil
}

func (s *Signer) seal(kind Kind, p payload) string {
	b, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(b)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(kind, body))
}

// open checks the shape and signature of a token and decodes its payload.
func (s *Signer) open(kind Kind, token string) (payload, Ref, bool) {
	if !contactTokenPattern.MatchString(token) {
		return payload{}, Ref{}, false
	}
	body, sig, _ := strings.Cut(token, ".")
	want := base64.RawURLEncoding.EncodeToString(s.mac(kind, body))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return payload{}, Ref{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return payload{}, Ref{}, false
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil || p.C == nil || p.N == nil {
		return payload{}, Ref{}, false
	}
	if !ids.Valid(*p.C) || !ids.Valid(*p.N) {
		return payload{}, Ref{}, false
	}
	return p, Ref{ContactID: *p.C, NewsletterID: *p.N}, true
}

// UnsubscribeToken never expires: links in old emails must keep working (CAN-SPAM: at least 30 days).
func (s *Signer) UnsubscribeToken(r Ref) (string, error) {
	r, err := normalizeRef(r)
	if err != nil {
		return "", err
	}
	return s.seal(KindUnsubscribe, payload{C: &r.ContactID, N: &r.NewsletterID}), nil
}

// VerifyUnsubscribe returns the reference of a valid unsubscribe token.
func (s *Signer) VerifyUnsubscribe(token string) (Ref, bool) {
	_, r, ok := s.open(KindUnsubscribe, token)
	return r, ok
}

// ContactToken is an expiring link for one contact (confirm, keep), signed with kind's key.
func (s *Signer) ContactToken(kind Kind, r Ref, now time.Time) (string, error) {
	r, err := normalizeRef(r)
	if err != nil {
		return "", err
	}
	t := float64(now.Unix())
	return s.seal(kind, payload{C: &r.ContactID, N: &r.NewsletterID, T: &t}), nil
}

// VerifyContactToken returns the reference of a valid token issued less than maxAge before now.
func (s *Signer) VerifyContactToken(kind Kind, token string, maxAge time.Duration, now time.Time) (Ref, bool) {
	p, r, ok := s.open(kind, token)
	if !ok || p.T == nil || *p.T != math.Trunc(*p.T) {
		return Ref{}, false
	}
	issued := time.Unix(int64(*p.T), 0)
	// A timestamp in the future would make the age negative, and an age that is never greater than
	// maxAge is a link that never expires. Only this package issues tokens and it stamps them with
	// now, so the only way here is a clock that moved backwards or a tampered payload (the phase 1-3
	// audit's Minor pool).
	if issued.After(now) || now.Sub(issued) > maxAge {
		return Ref{}, false
	}
	return r, true
}

// UnsubscribeLinks are the footer page link and the RFC 8058 one-click headers for one recipient.
// They never include the email address.
type UnsubscribeLinks struct {
	PageURL string
	Headers map[string]string
}

// UnsubscribeLinksFor builds both links on appURL (no trailing slash) with the same token.
func (s *Signer) UnsubscribeLinksFor(appURL string, r Ref) (UnsubscribeLinks, error) {
	tok, err := s.UnsubscribeToken(r)
	if err != nil {
		return UnsubscribeLinks{}, err
	}
	q := "token=" + tok
	return UnsubscribeLinks{
		PageURL: appURL + "/unsubscribe?" + q,
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + appURL + "/api/unsubscribe?" + q + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}, nil
}
