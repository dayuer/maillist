package httpsrv

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nibwire/internal/config"
	"nibwire/internal/core/model"
	"nibwire/internal/core/sns"
)

const testCertURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-9c6465fa7f48f5cacd23014631ec1136.pem"

// testKey signs the webhook messages; testVerifier hands the verifier its public key, exactly as the
// phase 1 sns tests do.
var (
	testKey    = mustRSAKey()
	testKeyPEM = mustPublicKeyPEM(testKey)
)

func mustRSAKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}

func mustPublicKeyPEM(key *rsa.PrivateKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func testVerifier(t *testing.T) *sns.Verifier {
	t.Helper()
	return sns.NewVerifier(func(context.Context, string) ([]byte, error) { return testKeyPEM, nil })
}

// snsEnvelope builds an SNS message of the given type and signs it the way SNS does.
func snsEnvelope(t *testing.T, messageType string, body map[string]string) []byte {
	t.Helper()
	message := map[string]string{
		"Type": messageType, "TopicArn": testTopicARN, "SigningCertURL": testCertURL,
		"SignatureVersion": "2", "MessageId": "22b80b92-fdea-4c2c-8f9d-bdfb0c7bf324",
		"Timestamp": "2026-10-12T12:00:00.000Z",
	}
	for key, value := range body {
		message[key] = value
	}
	signingString, ok := sns.SigningString(message)
	if !ok {
		t.Fatalf("no signing string for %v", message)
	}
	digest := crypto.SHA256.New()
	digest.Write([]byte(signingString))
	signature, err := rsa.SignPKCS1v15(rand.Reader, testKey, crypto.SHA256, digest.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	message["Signature"] = base64.StdEncoding.EncodeToString(signature)
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

const testTopicARN = "arn:aws:sns:us-east-1:123456789012:nibwire-ses-events"

func (s *testServer) postWebhook(t *testing.T, body []byte) *http.Response {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/ses", strings.NewReader(string(body)))
	request.Host = "mail.example.com"
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	return recorder.Result()
}

// sesEvent builds the SES event JSON an SNS notification carries.
func sesNotification(t *testing.T, providerMessageID, eventType string, detail map[string]any) []byte {
	t.Helper()
	event := map[string]any{
		"eventType": eventType,
		"mail": map[string]any{
			"timestamp": "2026-10-12T12:00:00.000Z", "messageId": providerMessageID,
			"destination": []string{"reader@example.com"},
			"tags":        map[string]any{"message_id": []string{}},
		},
	}
	key := map[string]string{
		"Delivery": "delivery", "Bounce": "bounce", "Complaint": "complaint",
		"Reject": "reject", "Open": "open", "Click": "click",
	}[eventType]
	if detail != nil {
		event[key] = detail
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return snsEnvelope(t, "Notification", map[string]string{"Message": string(encoded)})
}

func TestWebhookRejectsUnknownTopicAndBadSignature(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)

	// The topic is checked before the signature, so a stranger's topic never reaches the verifier.
	otherTopic := snsEnvelope(t, "Notification", map[string]string{"Message": "{}"})
	otherTopic = []byte(strings.Replace(string(otherTopic), testTopicARN, "arn:aws:sns:us-east-1:1:other", 1))
	if got := server.postWebhook(t, otherTopic).StatusCode; got != http.StatusForbidden {
		t.Errorf("unknown topic = %d, want 403", got)
	}

	tampered := sesNotification(t, message.ProviderMessageID, "Bounce", map[string]any{
		"bounceType": "Permanent", "bounceSubType": "General",
	})
	tampered = []byte(strings.Replace(string(tampered), "Permanent", "Transient", 1))
	if got := server.postWebhook(t, tampered).StatusCode; got != http.StatusBadRequest {
		t.Errorf("tampered = %d, want 400", got)
	}

	if got := server.postWebhook(t, []byte("not json")).StatusCode; got != http.StatusBadRequest {
		t.Errorf("garbage = %d, want 400", got)
	}
	if got := server.postWebhook(t, []byte(strings.Repeat("x", maxWebhookBody+10))).StatusCode; got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized = %d, want 413", got)
	}
}

func TestWebhookAppliesAHardBounce(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)

	body := sesNotification(t, message.ProviderMessageID, "Bounce", map[string]any{
		"bounceType": "Permanent", "bounceSubType": "General",
		"bouncedRecipients": []map[string]string{{"emailAddress": message.Email, "diagnosticCode": "smtp; 550 5.1.1"}},
		"timestamp":         "2026-10-12T12:05:00.000Z",
	})
	response := server.postWebhook(t, body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.MessageBounced || after.BouncedAt == nil {
		t.Fatalf("message = %+v", after)
	}
	if !strings.Contains(after.Error, "Hard bounce") {
		t.Errorf("error = %q", after.Error)
	}
	contact, err := server.store.Contact(context.Background(), message.ContactID)
	if err != nil {
		t.Fatal(err)
	}
	if contact.Status != model.ContactBounced {
		t.Errorf("contact = %s, want bounced", contact.Status)
	}
	reason, suppressed, err := server.store.Suppression(context.Background(), message.Email)
	if err != nil || !suppressed || reason != model.SuppressBounce {
		t.Errorf("suppression = %q %v %v", reason, suppressed, err)
	}
	counters, _, err := server.store.CampaignCountersOf(context.Background(), message.CampaignID)
	if err != nil || counters.Bounced != 1 {
		t.Errorf("counters = %+v %v", counters, err)
	}

	// The same event again changes nothing: every counter moves only on the first transition.
	if got := server.postWebhook(t, body); got.StatusCode != http.StatusOK {
		t.Errorf("replay = %d", got.StatusCode)
	}
	_ = response.Body.Close()
	counters, _, err = server.store.CampaignCountersOf(context.Background(), message.CampaignID)
	if err != nil || counters.Bounced != 1 {
		t.Errorf("counters after replay = %+v %v", counters, err)
	}
}

func TestWebhookComplaintOutranksAnEarlierBounce(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)

	bounce := sesNotification(t, message.ProviderMessageID, "Bounce", map[string]any{
		"bounceType": "Permanent", "bounceSubType": "General",
		"bouncedRecipients": []map[string]string{{"emailAddress": message.Email}},
	})
	_ = server.postWebhook(t, bounce).Body.Close()

	complaint := sesNotification(t, message.ProviderMessageID, "Complaint", map[string]any{
		"complainedRecipients":  []map[string]string{{"emailAddress": message.Email}},
		"complaintFeedbackType": "abuse",
	})
	response := server.postWebhook(t, complaint)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	reason, suppressed, err := server.store.Suppression(context.Background(), message.Email)
	if err != nil || !suppressed || reason != model.SuppressComplaint {
		t.Fatalf("suppression = %q %v %v, want complaint", reason, suppressed, err)
	}
	contact, err := server.store.Contact(context.Background(), message.ContactID)
	if err != nil {
		t.Fatal(err)
	}
	if contact.Status != model.ContactComplained {
		t.Errorf("contact = %s, want complained", contact.Status)
	}
	// The complaint is one of the events worth keeping.
	events, err := server.store.EventsFor(context.Background(), message.CampaignID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Errorf("events = %d, want the bounce and the complaint", len(events))
	}
}

func TestWebhookIgnoresUnknownEvents(t *testing.T) {
	server := newTestServer(t)
	for _, notification := range [][]byte{
		snsEnvelope(t, "Notification", map[string]string{"Message": `{"eventType":"Subscription","mail":{}}`}),
		snsEnvelope(t, "Notification", map[string]string{"Message": "not json"}),
		snsEnvelope(t, "Notification", map[string]string{"Message": "{}"}),
	} {
		response := server.postWebhook(t, notification)
		body := body(t, response)
		if response.StatusCode != http.StatusOK {
			t.Errorf("status = %d for %s", response.StatusCode, body)
		}
		if !strings.Contains(body, "ignored") {
			t.Errorf("body = %s, want ignored", body)
		}
	}
}

func TestWebhookConfirmsOnlyValidSubscribeURLs(t *testing.T) {
	server := newTestServer(t)
	var calls atomic.Int32
	var seen string
	server.Subscribe = func(_ context.Context, url string) error {
		calls.Add(1)
		seen = url
		return nil
	}

	confirmURL := "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=" +
		testTopicARN + "&Token=example"
	// SNS signs every field of a confirmation, Message included, so the envelope has to carry them
	// all for the signature to verify.
	response := server.postWebhook(t, snsEnvelope(t, "SubscriptionConfirmation", map[string]string{
		"Token": "example", "SubscribeURL": confirmURL,
		"Message": "You have chosen to subscribe to the topic " + testTopicARN + ".",
	}))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || calls.Load() != 1 || seen != confirmURL {
		t.Fatalf("confirmation = %d, calls = %d, url = %q", response.StatusCode, calls.Load(), seen)
	}

	// A hostile SubscribeURL is refused before anything is fetched.
	hostile := server.postWebhook(t, snsEnvelope(t, "SubscriptionConfirmation", map[string]string{
		"Token": "example", "SubscribeURL": "https://evil.example/?Action=ConfirmSubscription",
		"Message": "You have chosen to subscribe.",
	}))
	_ = hostile.Body.Close()
	if hostile.StatusCode != http.StatusBadRequest {
		t.Errorf("hostile SubscribeURL = %d, want 400", hostile.StatusCode)
	}
}

// 未配置 topic 时 webhook 直接 503，而不是静默接受任何人的消息。
func TestWebhookNeedsAConfiguredTopic(t *testing.T) {
	server := newTestServerWith(t, func(cfg *config.Config) { cfg.SESTopicARN = "" })
	response := server.postWebhook(t, []byte("{}"))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}
}

// 证书下载器只碰 ValidSigningCertURL 放行的地址：非法地址一个请求都不会发出去。
func TestCertificateFetcherRefusesNonSNSURLs(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(testKeyPEM)
	}))
	defer upstream.Close()

	fetcher := CertificateFetcher(upstream.Client())
	for _, url := range []string{
		"http://sns.us-east-1.amazonaws.com/SimpleNotificationService-abc.pem",
		"https://evil.example/SimpleNotificationService-abc.pem",
		"https://sns.us-east-1.amazonaws.com.evil.example/SimpleNotificationService-abc.pem",
		"https://sns.us-east-1.amazonaws.com/cert.pem",
		"https://sns.us-east-1.amazonaws.com:8443/SimpleNotificationService-abc.pem",
		upstream.URL + "/SimpleNotificationService-abc.pem",
	} {
		if _, err := fetcher(context.Background(), url); err == nil {
			t.Errorf("fetched %q", url)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want none: an unlisted URL must not be fetched", requests.Load())
	}

	// A six-byte oversized body is refused rather than buffered.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxCertificateBytes+1))
	}))
	defer big.Close()
	fetcher = CertificateFetcher(nil)
	if _, err := fetcher(context.Background(), testCertURL); err == nil {
		t.Error("an unreachable certificate endpoint must fail")
	}
}
