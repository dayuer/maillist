// Package sign signs the links Mailcheap puts in emails. Each kind of link derives its own key
// from SESSION_SECRET with HKDF-SHA256 (empty salt) and a distinct info label, so one kind can
// never stand in for another. Rotating the secret invalidates every link already sent.
package sign

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

const (
	InfoTracking    = "mailcheap-tracking-v1"
	InfoUnsubscribe = "mailcheap-unsubscribe-v1"
	InfoConfirm     = "mailcheap-confirm-v1"
	InfoKeep        = "mailcheap-keep-v1"
)

// ErrNoSecret is returned when the signer is built without a secret: never sign with an empty key.
var ErrNoSecret = errors.New("SESSION_SECRET is required to sign links")

// ErrInvalidReference is returned when a token would name something that isn't an ID.
var ErrInvalidReference = errors.New("invalid link reference")

// Signer holds one derived key per link kind.
type Signer struct {
	keys map[string][]byte
}

// New derives the per-kind keys from secret.
func New(secret string) (*Signer, error) {
	if secret == "" {
		return nil, ErrNoSecret
	}
	s := &Signer{keys: map[string][]byte{}}
	for _, info := range []string{InfoTracking, InfoUnsubscribe, InfoConfirm, InfoKeep} {
		k, err := hkdf.Key(sha256.New, []byte(secret), nil, info, 32)
		if err != nil {
			return nil, err
		}
		s.keys[info] = k
	}
	return s, nil
}

func (s *Signer) mac(info, value string) []byte {
	h := hmac.New(sha256.New, s.keys[info])
	h.Write([]byte(value))
	return h.Sum(nil)
}

// Ref names the contact and newsletter a link acts for.
type Ref struct {
	ContactID    string `json:"contactId"`
	NewsletterID string `json:"newsletterId"`
}
