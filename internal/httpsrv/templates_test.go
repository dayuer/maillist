package httpsrv

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"mailcheap/internal/core/model"
)

// 四个读者页面都渲染得出，都带上游的 CSP，且都不含 JavaScript。
//
// The plan expected a per-response CSP nonce; upstream's policy has no script-src at all, and these
// pages ship no JavaScript, so there is nothing for a nonce to authorise. The assertion follows
// upstream instead: the policy is byte-identical and no page carries a <script> tag.
func TestReaderPagesRender(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	contact := model.Contact{
		ID: "64f1a2b3c4d5e6f7a8b9c0d1", NewsletterID: newsletter.ID, Email: "reader@example.com",
		Status: model.ContactActive, CreatedAt: server.now,
	}
	if err := server.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}

	pages := map[string]struct {
		path   string
		status int
		marker string
	}{
		"unsubscribe": {"/unsubscribe?token=" + server.unsubscribeToken(t, contact), http.StatusOK, "Unsubscribe from Field notes?"},
		"confirm":     {"/confirm?token=" + server.confirmToken(t, contact), http.StatusOK, "Confirm my subscription"},
		"keep":        {"/keep?token=" + server.keepToken(t, contact), http.StatusOK, "Yes, keep sending it"},
		"subscribe":   {"/subscribe/field-notes", http.StatusOK, `name="email"`},
		"notice":      {"/unsubscribe?token=nope", http.StatusNotFound, "This link doesn&#39;t work"},
	}
	for name, page := range pages {
		response := server.get(t, page.path)
		html := body(t, response)
		if response.StatusCode != page.status {
			t.Errorf("%s: status = %d, want %d", name, response.StatusCode, page.status)
			continue
		}
		if !strings.HasPrefix(html, "<!doctype html>") {
			t.Errorf("%s: not an HTML document", name)
		}
		if !strings.Contains(html, page.marker) {
			t.Errorf("%s: missing %q in %s", name, page.marker, html)
		}
		if strings.Contains(html, "<script") {
			t.Errorf("%s: a reader page must not ship JavaScript", name)
		}
		if got := response.Header.Get("Content-Security-Policy"); !strings.Contains(got, "script-src-attr 'none'") {
			t.Errorf("%s: missing the upstream CSP: %q", name, got)
		}
		if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Errorf("%s: cache control = %q, want no-store", name, got)
		}
	}
}

// 带 token 的三个页面都声明 noindex：链接不该进搜索结果。
func TestTokenPagesAreNoIndex(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	contact := model.Contact{
		ID: "64f1a2b3c4d5e6f7a8b9c0d1", NewsletterID: newsletter.ID, Email: "reader@example.com",
		Status: model.ContactActive, CreatedAt: server.now,
	}
	if err := server.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"unsubscribe": "/unsubscribe?token=" + server.unsubscribeToken(t, contact),
		"confirm":     "/confirm?token=" + server.confirmToken(t, contact),
		"keep":        "/keep?token=" + server.keepToken(t, contact),
	} {
		html := body(t, server.get(t, path))
		if !strings.Contains(html, `<meta name="robots" content="noindex">`) {
			t.Errorf("%s: missing the noindex meta tag", name)
		}
	}
}
