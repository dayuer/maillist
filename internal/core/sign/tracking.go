package sign

import (
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

const signatureLength = 22

var (
	openTokenPattern = regexp.MustCompile(`^([a-f0-9]{24})\.([A-Za-z0-9_-]{22})$`)
	// RE2 caps repeat counts at 1000, so the original {4,4000} is split into a pattern and a length check.
	clickTokenPattern = regexp.MustCompile(`^([a-f0-9]{24})\.([A-Za-z0-9_-]{22})\.([A-Za-z0-9_-]{4,})$`)
)

const maxClickTargetChars = 4000

func (s *Signer) trackingSig(value string) string {
	return base64.RawURLEncoding.EncodeToString(s.mac(KindTracking, value))[:signatureLength]
}

func (s *Signer) trackingSigned(value, signature string) bool {
	return subtle.ConstantTimeCompare([]byte(s.trackingSig(value)), []byte(signature)) == 1
}

// OpenURL is the 1×1 pixel URL for one message.
func (s *Signer) OpenURL(baseURL, messageID string) string {
	id := strings.ToLower(messageID)
	return baseURL + "/o/" + id + "." + s.trackingSig("open:"+id)
}

// ClickURL is the redirect through Mailcheap to target for one message.
func (s *Signer) ClickURL(baseURL, messageID, target string) string {
	id := strings.ToLower(messageID)
	return baseURL + "/c/" + id + "." + s.trackingSig("click:"+id+":"+target) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(target))
}

// ReadOpenToken returns the message ID of a correctly signed open token.
func (s *Signer) ReadOpenToken(token string) (string, bool) {
	m := openTokenPattern.FindStringSubmatch(token)
	if m == nil || !s.trackingSigned("open:"+m[1], m[2]) {
		return "", false
	}
	return m[1], true
}

// Click is a decoded click token. A token whose signature doesn't match still decodes
// (Signed false), so its interstitial page can offer a plain link instead of redirecting.
type Click struct {
	MessageID string
	URL       string
	Signed    bool
}

// ReadClickToken decodes a click token; ok is false when it is malformed or not http(s).
func (s *Signer) ReadClickToken(token string) (Click, bool) {
	m := clickTokenPattern.FindStringSubmatch(token)
	if m == nil || len(m[3]) > maxClickTargetChars {
		return Click{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(m[3])
	if err != nil {
		return Click{}, false
	}
	target := string(raw)
	if !isHTTPURL(target) {
		return Click{}, false
	}
	return Click{MessageID: m[1], URL: target, Signed: s.trackingSigned("click:"+m[1]+":"+target, m[2])}, true
}

func isHTTPURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}
