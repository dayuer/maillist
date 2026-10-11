package httpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"mailcheap/internal/core/sns"
)

// maxWebhookBody is upstream's MAX_BODY_LENGTH: an SNS message is at most 256 KiB and the envelope
// adds a little.
const maxWebhookBody = 512 * 1024

// subscribeTimeout bounds the confirmation GET we make back to SNS.
const subscribeTimeout = 10 * time.Second

// Subscriber confirms an SNS subscription by fetching its SubscribeURL.
type Subscriber func(ctx context.Context, url string) error

// HTTPSubscriber is the real one; it refuses anything ValidSubscribeURL rejects.
func HTTPSubscriber(client *http.Client) Subscriber {
	if client == nil {
		client = &http.Client{Timeout: subscribeTimeout}
	}
	return func(ctx context.Context, url string) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return errSubscribeFailed
		}
		return nil
	}
}

// errSubscribeFailed is what the real Subscriber returns when SNS answers something other than 200.
var errSubscribeFailed = errors.New("subscription confirmation failed")

// sesWebhookLimit bounds one source's posts. The signature is checked after the certificate is
// downloaded, so an unsigned flood makes the process fetch a certificate per request — the rate
// limit is what caps that work, and it is per source rather than per process so a real SNS burst
// from Amazon's addresses is not refused by somebody else's flood.
var sesWebhookLimit = Limit{Name: "ses-webhook-ip", Count: 120, Window: time.Minute}

// handleSESWebhook answers POST /api/webhooks/ses. It needs an SNS HTTPS subscription with raw
// message delivery off, so the envelope is signed.
func (s *Server) handleSESWebhook(w http.ResponseWriter, r *http.Request) {
	if s.Config.SESTopicARN == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "SES webhook is not configured"})
		return
	}
	if s.Limiter.Limited(sesWebhookLimit, s.clientIP(r), s.clock()) {
		rateLimited(w, sesWebhookLimit)
		return
	}
	content, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Could not read the body"})
		return
	}
	if len(content) > maxWebhookBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Body is too large"})
		return
	}
	message := sns.Message{}
	if err := json.Unmarshal(content, &message); err != nil || message == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if message["TopicArn"] != s.Config.SESTopicARN {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Unknown topic"})
		return
	}
	if err := s.Verifier.Verify(r.Context(), message); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid signature"})
		return
	}

	switch message["Type"] {
	case "SubscriptionConfirmation":
		// Upstream checks the URL in the handler, before anything is fetched; the Subscriber only
		// has to perform the GET.
		if !sns.ValidSubscribeURL(message["SubscribeURL"]) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid SubscribeURL"})
			return
		}
		if err := s.Subscribe(r.Context(), message["SubscribeURL"]); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Subscription confirmation failed"})
			return
		}
		s.Log.Info("confirmed the SNS subscription", "topic", message["TopicArn"])
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "Notification":
		s.handleNotification(w, r, message)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleNotification parses the SES event out of the SNS envelope and applies it. A 5xx makes SNS
// retry, and applying is idempotent, so a retry is safe.
func (s *Server) handleNotification(w http.ResponseWriter, r *http.Request, message sns.Message) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message["Message"]), &event); err != nil || event == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "ignored": true})
		return
	}
	applied, err := s.Events.ApplyEvent(r.Context(), json.RawMessage(message["Message"]), s.clock())
	if err != nil {
		s.Log.Error("could not handle the SES event", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not handle the event"})
		return
	}
	body := map[string]any{"ok": true}
	if !applied.Handled {
		body["ignored"] = true
	}
	if applied.MessageID != "" {
		body["message_id"] = applied.MessageID
	}
	writeJSON(w, http.StatusOK, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
