package sns

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

const (
	certURL  = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-9c6465fa7f48f5cacd23014631ec1136.pem"
	topicARN = "arn:aws:sns:us-east-1:123456789012:nibwire-ses-events"
)

// Upstream's test generates an RSA pair and hands the verifier the exported public key, not a
// certificate, so both PEM kinds must parse.
var (
	testKey      *rsa.PrivateKey
	testKeyPEM   []byte
	otherTestKey *rsa.PrivateKey
)

func init() {
	var err error
	if testKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&testKey.PublicKey)
	if err != nil {
		panic(err)
	}
	testKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if otherTestKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
}

func notification() Message {
	return Message{
		"Type":           "Notification",
		"MessageId":      "22b80b92-fdea-4c2c-8f9d-bdfb0c7bf324",
		"TopicArn":       topicARN,
		"Subject":        "Amazon SES Email Event Notification",
		"Message":        `{"eventType":"Delivery","mail":{"messageId":"0100abc"}}`,
		"Timestamp":      "2026-10-04T12:00:00.000Z",
		"UnsubscribeURL": "https://sns.us-east-1.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=" + topicARN + ":1",
	}
}

func confirmation() Message {
	return Message{
		"Type":         "SubscriptionConfirmation",
		"MessageId":    "165545c9-2a5c-472c-8df2-7ff2be2b3b1b",
		"Token":        "example-subscription-token",
		"TopicArn":     topicARN,
		"Message":      "You have chosen to subscribe to the topic " + topicARN + ".",
		"SubscribeURL": "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=" + topicARN + "&Token=example",
		"Timestamp":    "2026-10-04T12:00:00.000Z",
	}
}

// signMessage is upstream signMessage: adds the certificate URL and version, signs the signing
// string, and appends the signature.
func signMessage(t *testing.T, m Message, version string, key *rsa.PrivateKey) Message {
	t.Helper()
	signed := Message{"SigningCertURL": certURL, "SignatureVersion": version}
	for k, v := range m {
		signed[k] = v
	}
	signingString, ok := SigningString(signed)
	if !ok {
		t.Fatalf("no signing string for %v", signed)
	}
	hash := crypto.SHA256
	if version == "1" {
		hash = crypto.SHA1
	}
	digest := hash.New()
	digest.Write([]byte(signingString))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, hash, digest.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	signed["Signature"] = base64.StdEncoding.EncodeToString(sig)
	return signed
}

func clone(m Message) Message {
	out := Message{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func testVerifier(t *testing.T) (*Verifier, *[]string) {
	t.Helper()
	var requests []string
	v := NewVerifier(func(_ context.Context, u string) ([]byte, error) {
		requests = append(requests, u)
		return testKeyPEM, nil
	})
	return v, &requests
}

func verify(t *testing.T, v *Verifier, m Message) bool {
	t.Helper()
	return v.Verify(context.Background(), m) == nil
}

// tests/sns.test.mjs "the signing string follows the AWS format for each message type".
func TestTheSigningStringFollowsTheAWSFormat(t *testing.T) {
	lines := func(keys []string, m Message) string {
		var parts []string
		for _, k := range keys {
			parts = append(parts, k, m[k])
		}
		parts = append(parts, "")
		return strings.Join(parts, "\n")
	}

	n := notification()
	if got, ok := SigningString(n); !ok ||
		got != lines([]string{"Message", "MessageId", "Subject", "Timestamp", "TopicArn", "Type"}, n) {
		t.Errorf("notification signing string =\n%q\nok=%v", got, ok)
	}
	withoutSubject := clone(n)
	delete(withoutSubject, "Subject")
	if got, ok := SigningString(withoutSubject); !ok ||
		got != lines([]string{"Message", "MessageId", "Timestamp", "TopicArn", "Type"}, withoutSubject) {
		t.Errorf("subject-less signing string =\n%q\nok=%v", got, ok)
	}
	c := confirmation()
	if got, ok := SigningString(c); !ok ||
		got != lines([]string{"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"}, c) {
		t.Errorf("confirmation signing string =\n%q\nok=%v", got, ok)
	}

	// Upstream also rejects `Message: undefined` and `Token: 42`; a map[string]string can hold
	// neither, so the absent-field case stands in for both.
	for _, bad := range []Message{
		withValue(n, "Type", "Other"),
		withValue(n, "Type", "constructor"),
		withoutKey(n, "Message"),
		withoutKey(c, "Token"),
		nil,
	} {
		if got, ok := SigningString(bad); ok {
			t.Errorf("SigningString(%v) = %q, want not ok", bad, got)
		}
	}
}

func withValue(m Message, key, value string) Message {
	out := clone(m)
	out[key] = value
	return out
}

func withoutKey(m Message, key string) Message {
	out := clone(m)
	delete(out, key)
	return out
}

// tests/sns.test.mjs "signature versions 1 (SHA1) and 2 (SHA256) verify".
func TestSignatureVersionsOneAndTwoVerify(t *testing.T) {
	v, _ := testVerifier(t)
	withoutSubject := clone(notification())
	delete(withoutSubject, "Subject")

	for name, m := range map[string]Message{
		"notification v1":          signMessage(t, notification(), "1", testKey),
		"notification v2":          signMessage(t, notification(), "2", testKey),
		"notification no subject":  signMessage(t, withoutSubject, "2", testKey),
		"confirmation v1":          signMessage(t, confirmation(), "1", testKey),
		"unsubscribe confirmation": signMessage(t, withValue(confirmation(), "Type", "UnsubscribeConfirmation"), "2", testKey),
	} {
		if !verify(t, v, m) {
			t.Errorf("%s did not verify", name)
		}
	}
}

// tests/sns.test.mjs "tampered, forged, and malformed messages are rejected".
func TestTamperedForgedAndMalformedMessagesAreRejected(t *testing.T) {
	v, _ := testVerifier(t)
	signed := signMessage(t, notification(), "2", testKey)

	rejected := map[string]Message{
		"changed message":   withValue(signed, "Message", strings.Replace(signed["Message"], "Delivery", "Bounce", 1)),
		"changed topic":     withValue(signed, "TopicArn", topicARN+"-other"),
		"changed subject":   withValue(signed, "Subject", "Changed"),
		"changed type":      withValue(signed, "Type", "SubscriptionConfirmation"),
		"other message id":  withValue(signed, "Signature", signMessage(t, withValue(notification(), "MessageId", "other"), "2", testKey)["Signature"]),
		"other key":         withValue(signed, "Signature", signMessage(t, notification(), "2", otherTestKey)["Signature"]),
		"wrong version":     withValue(signed, "SignatureVersion", "1"),
		"version 3":         withValue(signed, "SignatureVersion", "3"),
		"version prototype": withValue(signed, "SignatureVersion", "constructor"),
		"no signature":      withoutKey(signed, "Signature"),
		"garbage signature": withValue(signed, "Signature", "bm90IGEgc2lnbmF0dXJl"),
		"empty message":     nil,
	}
	for name, m := range rejected {
		if verify(t, v, m) {
			t.Errorf("%s was accepted", name)
		}
	}
}

// tests/sns.test.mjs "certificates are only fetched from https://sns.<region>.amazonaws.com/
// SimpleNotificationService-*.pem".
func TestCertificatesAreOnlyFetchedFromSNSPaths(t *testing.T) {
	v, requests := testVerifier(t)
	const certPath = "/SimpleNotificationService-9c6465fa7f48f5cacd23014631ec1136.pem"
	for _, u := range []string{
		"http://sns.us-east-1.amazonaws.com" + certPath,
		"https://sns.us-east-1.amazonaws.com.evil.com" + certPath,
		"https://evil.com/sns.us-east-1.amazonaws.com" + certPath,
		"https://sns.amazonaws.com" + certPath,
		"https://s3.us-east-1.amazonaws.com" + certPath,
		"https://sns.s3.amazonaws.com" + certPath,
		"https://sns.s3-us-west-2.amazonaws.com" + certPath,
		"https://sns.s3.us-west-2.amazonaws.com" + certPath,
		"https://sns.s3-website-us-east-1.amazonaws.com" + certPath,
		"https://sns.us_east_1.amazonaws.com" + certPath,
		"https://sns.cn-north-1.amazonaws.com.cn" + certPath,
		"https://sns.us-east-1.amazonaws.com:8443" + certPath,
		"https://user:pass@sns.us-east-1.amazonaws.com" + certPath,
		"https://sns.us-east-1.amazonaws.com/cert.pem",
		"https://sns.us-east-1.amazonaws.com/bucket/SimpleNotificationService-abc.pem",
		"https://sns.us-east-1.amazonaws.com/SimpleNotificationService-abc.txt",
		"https://sns.us-east-1.amazonaws.com/SimpleNotificationService-.pem",
		"https://sns.us-east-1.amazonaws.com" + certPath + "?x=1",
		"https://sns.us-east-1.amazonaws.com" + certPath + "#x",
		"not a url",
		"",
	} {
		if ValidSigningCertURL(u) {
			t.Errorf("accepted cert url %q", u)
		}
		message := signMessage(t, withValue(notification(), "SigningCertURL", u), "2", testKey)
		if verify(t, v, message) {
			t.Errorf("verified with cert url %q", u)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("a rejected URL must never be fetched: %v", *requests)
	}

	for _, u := range []string{
		certURL,
		"https://sns.eu-central-1.amazonaws.com" + certPath,
		"https://sns.us-gov-west-1.amazonaws.com" + certPath,
	} {
		if !ValidSigningCertURL(u) {
			t.Errorf("rejected cert url %q", u)
		}
	}
}

// tests/sns.test.mjs "subscription URLs must point at SNS over https".
func TestSubscriptionURLsMustPointAtSNSOverHTTPS(t *testing.T) {
	subscribe := confirmation()["SubscribeURL"]
	for u, want := range map[string]bool{
		subscribe: true,
		strings.Replace(subscribe, "https:", "http:", 1):           false,
		"https://example.com/?Action=ConfirmSubscription":          false,
		"https://sns.s3.amazonaws.com/?Action=ConfirmSubscription": false,
		"": false,
	} {
		if got := ValidSubscribeURL(u); got != want {
			t.Errorf("ValidSubscribeURL(%q) = %v, want %v", u, got, want)
		}
	}
}

// tests/sns.test.mjs "certificates are cached per URL and failed downloads are retried".
func TestCertificatesAreCachedAndFailedDownloadsRetried(t *testing.T) {
	calls := 0
	v := NewVerifier(func(_ context.Context, _ string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("network down")
		}
		return testKeyPEM, nil
	})
	signed := signMessage(t, notification(), "2", testKey)
	if verify(t, v, signed) {
		t.Error("a failed download must not verify")
	}
	if !verify(t, v, signed) {
		t.Error("the retry must verify")
	}
	if !verify(t, v, signMessage(t, confirmation(), "2", testKey)) {
		t.Error("the cached certificate must be reused")
	}
	if calls != 2 {
		t.Errorf("downloads = %d, want 2", calls)
	}
}

// tests/sns.test.mjs "a certificate download that isn't PEM is rejected".
func TestACertificateDownloadThatIsNotPEMIsRejected(t *testing.T) {
	v := NewVerifier(func(_ context.Context, _ string) ([]byte, error) {
		return []byte("<html>Not found</html>"), nil
	})
	if verify(t, v, signMessage(t, notification(), "2", testKey)) {
		t.Error("a non-PEM certificate must not verify")
	}
}
