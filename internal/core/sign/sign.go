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

// MinSecretLength is SESSION_SECRET's floor, checked here as well as in config: a short or empty key
// signs links anybody can forge, and a caller that builds a Signer directly must not be able to
// (the phase 1-3 audit's S-M3).
const MinSecretLength = 32

// Kind is which links a key is derived for. It is a type rather than a string so that a typo, or a
// purpose nothing derives a key for, is a compile error instead of a silent HMAC with an empty key.
type Kind string

// The four kinds. Their values are the HKDF info labels, which are upstream's and must not change:
// they are what makes a tracking signature useless as an unsubscribe signature.
const (
	KindTracking    Kind = InfoTracking
	KindUnsubscribe Kind = InfoUnsubscribe
	KindConfirm     Kind = InfoConfirm
	KindKeep        Kind = InfoKeep
)

// Kinds is every kind a Signer holds a key for.
func Kinds() []Kind {
	return []Kind{KindTracking, KindUnsubscribe, KindConfirm, KindKeep}
}

// ErrNoSecret is returned when the signer is built without a secret: never sign with an empty key.
var ErrNoSecret = errors.New("SESSION_SECRET is required to sign links")

// ErrInvalidReference is returned when a token would name something that isn't an ID.
var ErrInvalidReference = errors.New("invalid link reference")

// Signer holds one derived key per link kind.
type Signer struct {
	keys map[Kind][]byte
}

// New derives the per-kind keys from secret.
func New(secret string) (*Signer, error) {
	if len(secret) < MinSecretLength {
		return nil, ErrNoSecret
	}
	s := &Signer{keys: map[Kind][]byte{}}
	for _, kind := range Kinds() {
		k, err := hkdf.Key(sha256.New, []byte(secret), nil, string(kind), 32)
		if err != nil {
			return nil, err
		}
		s.keys[kind] = k
	}
	return s, nil
}

func (s *Signer) mac(kind Kind, value string) []byte {
	h := hmac.New(sha256.New, s.keys[kind])
	h.Write([]byte(value))
	return h.Sum(nil)
}

// Ref names the contact and newsletter a link acts for.
type Ref struct {
	ContactID    string `json:"contactId"`
	NewsletterID string `json:"newsletterId"`
}
