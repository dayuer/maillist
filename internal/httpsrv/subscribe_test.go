package httpsrv

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"nibwire/internal/core/model"
	"nibwire/internal/mailer"
)

func signupForm(email string) url.Values { return url.Values{"email": {email}} }

// 公开订阅表单：新地址落库并发欢迎信（未开启双重确认时）。
func TestSubscribeAddsAContactAndSendsWelcome(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)

	response := server.postForm(t, "/subscribe/field-notes", signupForm("New@Example.com"))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if page := body(t, response); !strings.Contains(page, "You&#39;re subscribed") {
		t.Errorf("page = %s", page)
	}

	newsletter, err := server.store.NewsletterBySlug(context.Background(), "field-notes")
	if err != nil {
		t.Fatal(err)
	}
	contact, err := server.store.ContactByEmail(context.Background(), newsletter.ID, "new@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if contact.Status != model.ContactActive || contact.Source != "website" {
		t.Fatalf("contact = %+v", contact)
	}
	if server.fake.SentCount() != 1 || server.fake.Sent()[0].Tags["kind"] != string(model.KindWelcome) {
		t.Fatalf("sent = %+v", server.fake.Sent())
	}
}

// 站点注册洪水：告警，但不拒绝（#48，上游 app/archive/actions.js 的那条 sendAlert）。
//
// 超过共享上限的注册照样放行——拒绝等于让一个洪水者把所有真实注册挡在外面——所以告警是这件事唯一的
// 可见面。
func TestASignupFloodAlertsWithoutRefusing(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)
	ctx := context.Background()

	// The newsletter is already over its shared limit: that is other signups, not this one. (Posting
	// three hundred times would need sixty visitors first, because the per-visitor limit refuses.)
	for i := 0; i < subscribeNewsletterLimit.Count; i++ {
		server.Limiter.Limited(subscribeNewsletterLimit, "field-notes", server.now)
	}

	response := server.postForm(t, "/subscribe/field-notes", signupForm("reader@example.com"))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the signup through: past the shared limit it alerts rather than "+
			"refuses", response.StatusCode)
	}
	_ = response.Body.Close()
	newsletter, err := server.store.NewsletterBySlug(ctx, "field-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.ContactByEmail(ctx, newsletter.ID, "reader@example.com"); err != nil {
		t.Fatalf("the signup did not go through: %v", err)
	}

	// The operator hears about it: once, with the numbers and the next step.
	rows, err := server.store.Alerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "signup-flood:field-notes" ||
		rows[0].Title != "Website signup flood" {
		t.Fatalf("alert rows = %+v", rows)
	}
	alert := emailed(t, server, "Website signup flood")
	for _, want := range []string{"300", "Field notes", "still go through", "contact list"} {
		if !strings.Contains(alert.Email.Text, want) {
			t.Errorf("the alert does not say %q: %s", want, alert.Email.Text)
		}
	}
}

// emailed asserts the operator got an email with this alert title and returns what it said: the
// detail lives in the email and the log and nowhere else, so this is where a test reads it back.
func emailed(t *testing.T, server *testServer, title string) mailer.SentEmail {
	t.Helper()
	for _, sent := range server.fake.Sent() {
		if sent.Email.Subject == "[Nibwire] "+title {
			return sent
		}
	}
	t.Fatalf("no email with the subject %q: %+v", "[Nibwire] "+title, server.fake.Sent())
	return mailer.SentEmail{}
}

// 双重确认开启时发确认信，并告诉读者去哪儿找。
func TestSubscribeSendsAConfirmationWhenRequired(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	newsletter.RequireConfirmation = true
	if err := server.store.UpdateNewsletter(context.Background(), newsletter); err != nil {
		t.Fatal(err)
	}

	response := server.postForm(t, "/subscribe/field-notes", signupForm("reader@gmail.com"))
	page := body(t, response)
	if !strings.Contains(page, "Confirm your subscription") {
		t.Errorf("page = %s", page)
	}
	if !strings.Contains(page, "Gmail") || !strings.Contains(page, "mail.google.com") {
		t.Errorf("the page must offer the reader's webmail: %s", page)
	}

	contact, err := server.store.ContactByEmail(context.Background(), newsletter.ID, "reader@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	if contact.Status != model.ContactPending {
		t.Fatalf("contact = %s, want pending", contact.Status)
	}
	if server.fake.SentCount() != 1 || server.fake.Sent()[0].Tags["kind"] != string(model.KindConfirmation) {
		t.Fatalf("sent = %+v", server.fake.Sent())
	}
}

// 蜜罐字段被填就当作机器人：什么都不写。
func TestSubscribeHoneypot(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)

	form := url.Values{"email": {"bot@example.com"}, "website": {"http://spam.example"}}
	response := server.postForm(t, "/subscribe/field-notes", form)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	newsletter, err := server.store.NewsletterBySlug(context.Background(), "field-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.ContactByEmail(context.Background(), newsletter.ID, "bot@example.com"); err == nil {
		t.Fatal("the honeypot address was stored")
	}
	if server.fake.SentCount() != 0 {
		t.Fatalf("sent = %d", server.fake.SentCount())
	}
}

func TestSubscribeRejectsBadInput(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)

	bad := server.postForm(t, "/subscribe/field-notes", signupForm("not-an-email"))
	if page := body(t, bad); !strings.Contains(page, "Enter a valid email") {
		t.Errorf("page = %s", page)
	}
	missing := server.get(t, "/subscribe/no-such-newsletter")
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("unknown newsletter = %d, want 404", missing.StatusCode)
	}
	// A newsletter that isn't published takes no signups.
	archived, err := server.store.NewsletterBySlug(context.Background(), "field-notes")
	if err != nil {
		t.Fatal(err)
	}
	archived.ArchiveEnabled = false
	if err := server.store.UpdateNewsletter(context.Background(), archived); err != nil {
		t.Fatal(err)
	}
	hidden := server.postForm(t, "/subscribe/field-notes", signupForm("reader@example.com"))
	_ = hidden.Body.Close()
	if hidden.StatusCode != http.StatusNotFound {
		t.Errorf("unpublished newsletter = %d, want 404", hidden.StatusCode)
	}
}

// 每 IP 10 分钟 5 次；第 6 次被拒。
func TestSubscribeIsRateLimitedPerIP(t *testing.T) {
	server := newTestServer(t)
	server.seedNewsletter(t)

	last := 0
	for i := 0; i < subscribeIPLimit.Count+1; i++ {
		response := server.postFormFrom(t, "/subscribe/field-notes",
			signupForm("reader@example.com"), "203.0.113.7")
		last = response.StatusCode
		_ = response.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after %d signups = %d, want 429", subscribeIPLimit.Count+1, last)
	}
}

// 确认页：GET 只显示按钮，POST 才把联系人变成 active。
func TestConfirmFlow(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	newsletter.RequireConfirmation = true
	if err := server.store.UpdateNewsletter(context.Background(), newsletter); err != nil {
		t.Fatal(err)
	}
	contact := model.Contact{
		ID: "64f1a2b3c4d5e6f7a8b9c0d1", NewsletterID: newsletter.ID, Email: "reader@example.com",
		Status: model.ContactPending, CreatedAt: server.now,
	}
	if err := server.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	token := server.confirmToken(t, contact)

	page := server.get(t, "/confirm?token="+token)
	if got := body(t, page); !strings.Contains(got, "Confirm my subscription") {
		t.Errorf("page = %s", got)
	}
	still, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != model.ContactPending {
		t.Fatalf("a GET confirmed the contact: %s", still.Status)
	}

	confirmed := server.post(t, "/confirm?token="+token)
	if got := body(t, confirmed); !strings.Contains(got, "You&#39;re in") {
		t.Errorf("page = %s", got)
	}
	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactActive || after.ConfirmedAt == nil {
		t.Fatalf("contact = %+v", after)
	}
	// No welcome email: a newsletter that confirms signups says it all in the confirmation email
	// (rules.WelcomeContent returns nothing while RequireConfirmation is on).
	if server.fake.SentCount() != 0 {
		t.Fatalf("sent = %+v, want none", server.fake.Sent())
	}
}

// 确认之后发欢迎信的情形：联系人曾是 pending，而 newsletter 不再要求确认。
func TestConfirmSendsTheWelcomeWhenTheNewsletterHasOne(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	contact := model.Contact{
		ID: "64f1a2b3c4d5e6f7a8b9c0d1", NewsletterID: newsletter.ID, Email: "reader@example.com",
		Status: model.ContactPending, CreatedAt: server.now,
	}
	if err := server.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	response := server.post(t, "/confirm?token="+server.confirmToken(t, contact))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	sent := server.fake.Sent()
	if len(sent) != 1 || sent[0].Tags["kind"] != string(model.KindWelcome) {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestConfirmRejectsBadTokens(t *testing.T) {
	server := newTestServer(t)
	for _, path := range []string{"/confirm?token=nope", "/keep?token=nope"} {
		response := server.get(t, path)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, response.StatusCode)
		}
		posted := server.post(t, path)
		_ = posted.Body.Close()
		if posted.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, posted.StatusCode)
		}
	}
}

// keep 页：GET 不改状态，POST 把 dormant 联系人救回来。
func TestKeepFlow(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	contact := model.Contact{
		ID: "64f1a2b3c4d5e6f7a8b9c0d1", NewsletterID: newsletter.ID, Email: "cold@example.com",
		Status: model.ContactDormant, CreatedAt: server.now,
	}
	if err := server.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	token := server.keepToken(t, contact)

	if got := body(t, server.get(t, "/keep?token="+token)); !strings.Contains(got, "Yes, keep sending it") {
		t.Errorf("page = %s", got)
	}
	still, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != model.ContactDormant {
		t.Fatalf("a GET revived the contact: %s", still.Status)
	}

	kept := server.post(t, "/keep?token="+token)
	if got := body(t, kept); !strings.Contains(got, "You&#39;ll keep getting") {
		t.Errorf("page = %s", got)
	}
	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactActive {
		t.Fatalf("contact = %s, want active", after.Status)
	}
}

// 退订过的人不能靠 keep 链接回来。
func TestKeepDoesNotReverseAnUnsubscribe(t *testing.T) {
	server := newTestServer(t)
	newsletter := server.seedNewsletter(t)
	contact := model.Contact{
		ID: "64f1a2b3c4d5e6f7a8b9c0d1", NewsletterID: newsletter.ID, Email: "gone@example.com",
		Status: model.ContactUnsubscribed, CreatedAt: server.now,
	}
	if err := server.store.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	response := server.post(t, "/keep?token="+server.keepToken(t, contact))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
	after, err := server.store.Contact(context.Background(), contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.ContactUnsubscribed {
		t.Fatalf("contact = %s, want it left unsubscribed", after.Status)
	}
}
