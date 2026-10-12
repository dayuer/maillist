package httpsrv

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"nibwire/internal/core/model"
)

// GET 永不改状态：邮件客户端会把 List-Unsubscribe 的地址当普通链接打开。
func TestUnsubscribeGETChangesNothing(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)

	response := server.get(t, "/unsubscribe?token="+server.unsubscribeToken(t, contact))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	page := body(t, response)
	if !strings.Contains(page, "Unsubscribe from Field notes?") {
		t.Errorf("the page must ask first: %s", page)
	}
	if !strings.Contains(page, `action="/api/unsubscribe?token=`) {
		t.Errorf("the form must post the one-click endpoint: %s", page)
	}
	if !strings.Contains(page, `<input type="hidden" name="source" value="page">`) {
		t.Errorf("the form must say where it came from: %s", page)
	}

	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactActive {
		t.Fatalf("a GET changed the contact: %s", after.Status)
	}
}

// RFC 8058 一键退订：POST 改状态，且重试幂等。
func TestUnsubscribePOSTIsIdempotent(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)
	token := server.unsubscribeToken(t, contact)

	first := server.post(t, "/api/unsubscribe?token="+token)
	_ = first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", first.StatusCode)
	}
	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactUnsubscribed || after.UnsubscribedAt == nil {
		t.Fatalf("contact = %+v", after)
	}

	// A mail app that retries must not see an error.
	second := server.post(t, "/api/unsubscribe?token="+token)
	_ = second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", second.StatusCode)
	}
}

// 页面表单走 303 回退订页，并带上状态。
func TestUnsubscribePageFormRedirects(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)
	token := server.unsubscribeToken(t, contact)

	response := server.postForm(t, "/api/unsubscribe?token="+token, url.Values{"source": {"page"}})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if !strings.HasPrefix(location, "/unsubscribe?") || !strings.Contains(location, "status=done") {
		t.Fatalf("location = %q", location)
	}
}

// 退订之后再打开链接，页面上说的是"你已经退订了"，而不是再问一次。
func TestUnsubscribePageAfterUnsubscribing(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)
	token := server.unsubscribeToken(t, contact)
	_ = server.post(t, "/api/unsubscribe?token="+token).Body.Close()

	// html/template escapes the apostrophe, so the assertion spells it the way the page does.
	page := body(t, server.get(t, "/unsubscribe?token="+token))
	if !strings.Contains(page, "You&#39;re unsubscribed") {
		t.Errorf("page = %s", page)
	}
	if strings.Contains(page, "<form") {
		t.Errorf("an already-unsubscribed reader must not be asked again: %s", page)
	}
}

// 徽章与投诉状态不被退订链接改写。
func TestUnsubscribeLeavesBouncedContactsAlone(t *testing.T) {
	server := newTestServer(t)
	contact := server.seedSubscribedContact(t)
	contact.Status = model.ContactBounced
	if err := server.store.UpdateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	_ = server.post(t, "/api/unsubscribe?token="+server.unsubscribeToken(t, contact)).Body.Close()

	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactBounced {
		t.Fatalf("contact = %s, want it left as bounced", after.Status)
	}
}

func TestUnsubscribeRejectsBadTokensAndRateLimits(t *testing.T) {
	server := newTestServer(t)
	bad := server.post(t, "/api/unsubscribe?token=nope")
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("bad token = %d, want 400", bad.StatusCode)
	}
	missing := server.get(t, "/unsubscribe?token=nope")
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("bad token page = %d, want 404", missing.StatusCode)
	}

	contact := server.seedSubscribedContact(t)
	token := server.unsubscribeToken(t, contact)
	last := 0
	for i := 0; i < unsubscribeLimit.Count+1; i++ {
		response := server.postFormFrom(t, "/api/unsubscribe?token="+token, nil, "203.0.113.7")
		last = response.StatusCode
		_ = response.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after %d attempts = %d, want 429", unsubscribeLimit.Count+1, last)
	}
	if got := server.postFormFrom(t, "/api/unsubscribe?token="+token, nil, "198.51.100.9").StatusCode; got == http.StatusTooManyRequests {
		t.Error("another address must keep its own budget")
	} else {
		_ = got
	}
}

// 退订会给最近一次发给他们的 campaign 记一次 unsubscribed（链接本身不带 campaign）。
func TestUnsubscribeCreditsTheLatestCampaign(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	token := server.unsubscribeToken(t, model.Contact{
		ID: message.ContactID, NewsletterID: message.NewsletterID,
	})
	_ = server.post(t, "/api/unsubscribe?token="+token).Body.Close()

	counters, _, err := server.store.CampaignCountersOf(context.Background(), message.CampaignID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Unsubscribed != 1 {
		t.Errorf("campaign unsubscribed = %d, want 1", counters.Unsubscribed)
	}
}
