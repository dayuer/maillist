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

func (s *Signer) seal(info string, p payload) string {
	b, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(b)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(info, body))
}

// open checks the shape and signature of a token and decodes its payload.
func (s *Signer) open(info, token string) (payload, Ref, bool) {
	if !contactTokenPattern.MatchString(token) {
		return payload{}, Ref{}, false
	}
	body, sig, _ := strings.Cut(token, ".")
	want := base64.RawURLEncoding.EncodeToString(s.mac(info, body))
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
	return s.seal(InfoUnsubscribe, payload{C: &r.ContactID, N: &r.NewsletterID}), nil
}

// VerifyUnsubscribe returns the reference of a valid unsubscribe token.
func (s *Signer) VerifyUnsubscribe(token string) (Ref, bool) {
	_, r, ok := s.open(InfoUnsubscribe, token)
	return r, ok
}

// ContactToken is an expiring link for one contact (confirm, keep), signed with info's key.
func (s *Signer) ContactToken(info string, r Ref, now time.Time) (string, error) {
	r, err := normalizeRef(r)
	if err != nil {
		return "", err
	}
	t := float64(now.Unix())
	return s.seal(info, payload{C: &r.ContactID, N: &r.NewsletterID, T: &t}), nil
}

// VerifyContactToken returns the reference of a valid token issued less than maxAge before now.
func (s *Signer) VerifyContactToken(info, token string, maxAge time.Duration, now time.Time) (Ref, bool) {
	p, r, ok := s.open(info, token)
	if !ok || p.T == nil || *p.T != math.Trunc(*p.T) {
		return Ref{}, false
	}
	issued := time.Unix(int64(*p.T), 0)
	if now.Sub(issued) > maxAge {
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
