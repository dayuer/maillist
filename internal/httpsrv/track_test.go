package httpsrv

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mailcheap/internal/core/model"
)

// GET /o/{token} 永远返回 1×1 GIF，签名有效才记一次打开。
func TestOpenPixel(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)

	response := server.get(t, "/o/"+tokenOf(server.signer.OpenURL(server.appURL(), message.ID)))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "image/gif" {
		t.Errorf("content type = %q", got)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Errorf("cache control = %q", got)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !bytes.Equal(content, pixelGIF) {
		t.Errorf("body = %d bytes, want the transparent GIF", len(content))
	}

	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.OpenedAt == nil {
		t.Error("the open was not recorded")
	}

	// A forged token still gets the pixel: a reader must never see a broken image.
	forged := base64.RawURLEncoding.EncodeToString([]byte("not-a-real-signature"))
	response = server.get(t, "/o/"+message.ID+"."+forged[:22])
	if response.StatusCode != http.StatusOK {
		t.Errorf("forged token status = %d, want 200", response.StatusCode)
	}
	_ = response.Body.Close()
}

// 已签名的点击 302 到目标；未签名的给中断页；非 http(s) 的 404。
func TestClickRedirectsOnlySignedLinks(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	target := "https://example.org/?utm_source=newsletter&ref=1"
	token := tokenOf(server.signer.ClickURL(server.appURL(), message.ID, target))

	signed := server.get(t, "/c/"+token)
	_ = signed.Body.Close()
	if signed.StatusCode != http.StatusFound || signed.Header.Get("Location") != target {
		t.Fatalf("signed = %d %q", signed.StatusCode, signed.Header.Get("Location"))
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClickedAt == nil {
		t.Error("the click was not recorded")
	}

	parts := strings.Split(token, ".")
	unsigned := parts[0] + "." + parts[1] + "." +
		base64.RawURLEncoding.EncodeToString([]byte("https://evil.example"))
	response := server.get(t, "/c/"+unsigned)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unsigned status = %d, want the interstitial page", response.StatusCode)
	}
	page := body(t, response)
	if !strings.Contains(page, "https://evil.example") {
		t.Error("the interstitial must show where the link goes")
	}
	if strings.Contains(page, "<script") {
		t.Errorf("the interstitial carries no script: %s", page)
	}

	javascript := parts[0] + "." + parts[1] + "." +
		base64.RawURLEncoding.EncodeToString([]byte("javascript:alert(1)"))
	if got := server.get(t, "/c/"+javascript).StatusCode; got != http.StatusNotFound {
		t.Errorf("javascript: target status = %d, want 404", got)
	}
	if got := server.get(t, "/c/not-a-token").StatusCode; got != http.StatusNotFound {
		t.Errorf("garbage status = %d, want 404", got)
	}
}

// 链接扫描器照样被跳转，但不计数（上游 isLikelyScanner）。
func TestScannersRedirectButDoNotCount(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	token := tokenOf(server.signer.ClickURL(server.appURL(), message.ID, "https://example.org/"))

	request := httptest.NewRequest(http.MethodGet, "/c/"+token, nil)
	request.Header.Set("User-Agent", "Mimecast Link Checker")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClickedAt != nil {
		t.Error("a scanner's click was counted")
	}
	var events int
	if err := server.store.Row(context.Background(),
		"SELECT count(*) FROM events WHERE message_id = ?", message.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("events = %d, want none", events)
	}
}

// HEAD 是链接检查器常用的：答案相同，但永不计为点击。
func TestClickHeadDoesNotCount(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	token := tokenOf(server.signer.ClickURL(server.appURL(), message.ID, "https://example.org/"))

	request := httptest.NewRequest(http.MethodHead, "/c/"+token, nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("HEAD status = %d, want 302", recorder.Code)
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClickedAt != nil {
		t.Error("HEAD counted as a click")
	}
}

// 打开会写 last_open_at、计一次打开并清 ignored_issues；重复打开不重复计数。
func TestOpenIsCountedOnce(t *testing.T) {
	server := newTestServer(t)
	message := server.seedMessage(t)
	token := tokenOf(server.signer.OpenURL(server.appURL(), message.ID))

	for i := 0; i < 3; i++ {
		_ = server.get(t, "/o/"+token).Body.Close()
	}
	contact, err := server.store.Contact(context.Background(), message.ContactID)
	if err != nil {
		t.Fatal(err)
	}
	if contact.Opens != 1 {
		t.Errorf("contact opens = %d, want 1", contact.Opens)
	}
	if contact.LastOpenAt == nil || contact.LastEngagedAt == nil {
		t.Errorf("contact = %+v", contact)
	}
	counters, _, err := server.store.CampaignCountersOf(context.Background(), message.CampaignID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Opened != 1 {
		t.Errorf("campaign opened = %d, want 1", counters.Opened)
	}
}

// 追踪像素在归档域名上照常工作。
func TestTrackingWorksOnTheArchiveHost(t *testing.T) {
	server := newTestServerWithArchiveHost(t, "news.example.com", "field-notes")
	message := server.seedMessage(t)
	response := server.getOnHost(t, "/o/"+tokenOf(server.signer.OpenURL(server.appURL(), message.ID)), "news.example.com")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	after, err := server.store.Message(context.Background(), message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.MessageSent || after.OpenedAt == nil {
		t.Errorf("message = %+v", after)
	}
}

// appURL 是配置里的站点地址，测试里用它签链接。
func (s *testServer) appURL() string { return testAppURL }
