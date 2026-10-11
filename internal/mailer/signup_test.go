package mailer

import (
	"context"
	"strings"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/rules"
)

// ptrTime is the pointer form the record types use for optional timestamps.
func ptrTime(at time.Time) *time.Time { return &at }

// pendingContact is a contact waiting to confirm.
func pendingContact(t *testing.T, f *fixture, email string) model.Contact {
	t.Helper()
	contact := model.Contact{
		ID: ids.New(), NewsletterID: f.newsletter.ID, Email: email,
		Status: model.ContactPending, SubscribedAt: ptrTime(time.Now().UTC()), CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateContact(context.Background(), contact); err != nil {
		t.Fatal(err)
	}
	return contact
}

// confirmingNewsletter switches the newsletter to confirmed opt-in.
func (f *fixture) confirmingNewsletter(t *testing.T) model.Newsletter {
	t.Helper()
	newsletter := f.newsletter
	newsletter.RequireConfirmation = true
	if err := f.db.UpdateNewsletter(context.Background(), newsletter); err != nil {
		t.Fatal(err)
	}
	return newsletter
}

// seedConfirmation writes a confirmation message with a chosen age, so the window can be tested
// without waiting ten minutes.
func seedConfirmation(t *testing.T, f *fixture, contact model.Contact, age time.Duration) {
	t.Helper()
	message := model.Message{
		ID: ids.New(), Kind: model.KindConfirmation, NewsletterID: f.newsletter.ID,
		ContactID: contact.ID, Email: contact.Email, Status: model.MessageSent,
		CreatedAt: time.Now().UTC().Add(-age),
	}
	if _, err := f.db.InsertMessages(context.Background(), []model.Message{message}); err != nil {
		t.Fatal(err)
	}
}

// 确认信每联系人最多 3 封（上游 MAX_CONFIRMATIONS_PER_CONTACT），且不加追踪。
//
// The cap counts every confirmation the contact ever got; the ten-minute window is a second,
// independent gate. Two earlier ones outside the window leave room for exactly one more.
func TestConfirmationEmailsAreCapped(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	newsletter := f.confirmingNewsletter(t)
	contact := pendingContact(t, f, "pending@example.com")

	seedConfirmation(t, f, contact, 2*rules.WelcomeWindow)
	seedConfirmation(t, f, contact, 3*rules.WelcomeWindow)

	result, err := f.engine.QueueConfirmation(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != "" {
		t.Fatalf("third send skipped: %s", result.Skipped)
	}
	sent := f.fake.Sent()[0]
	if sent.Tags["kind"] != string(model.KindConfirmation) {
		t.Errorf("kind = %q", sent.Tags["kind"])
	}
	// Untracked (rules.Untracked): the links have to work exactly as sent.
	if strings.Contains(sent.Email.HTML, "/c/") || strings.Contains(sent.Email.HTML, "/o/") {
		t.Error("a confirmation email must carry no tracking redirect or pixel")
	}
	if !strings.Contains(sent.Email.HTML, "/confirm?token=") {
		t.Error("the confirmation link is missing")
	}
	if strings.Contains(sent.Email.HTML, "{{confirm_url}}") {
		t.Error("the confirmation placeholder was not filled")
	}

	fourth, err := f.engine.QueueConfirmation(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Skipped != "already sent" {
		t.Fatalf("fourth = %+v, want already sent", fourth)
	}
	if f.fake.SentCount() != 1 {
		t.Fatalf("sent = %d, want 1", f.fake.SentCount())
	}
}

// 10 分钟内已发过就不再发；窗口之外可以再发（上游 WELCOME_WINDOW_MS）。
func TestConfirmationWindow(t *testing.T) {
	ctx := context.Background()

	recent := newFixture(t, 0)
	newsletter := recent.confirmingNewsletter(t)
	contact := pendingContact(t, recent, "recent@example.com")
	seedConfirmation(t, recent, contact, time.Minute)
	result, err := recent.engine.QueueConfirmation(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != "already sent" {
		t.Fatalf("inside the window = %+v", result)
	}

	old := newFixture(t, 0)
	newsletter = old.confirmingNewsletter(t)
	contact = pendingContact(t, old, "old@example.com")
	seedConfirmation(t, old, contact, 2*rules.WelcomeWindow)
	result, err = old.engine.QueueConfirmation(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != "" {
		t.Fatalf("outside the window = %+v", result)
	}
	if old.fake.SentCount() != 1 {
		t.Fatalf("sent = %d, want 1", old.fake.SentCount())
	}
}

// 欢迎信只发给 active 联系人，且每联系人一封。
func TestWelcomeEmailsAreOncePerContact(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	newsletter := f.newsletter
	contact := model.Contact{
		ID: ids.New(), NewsletterID: newsletter.ID, Email: "new@example.com",
		Status: model.ContactActive, SubscribedAt: ptrTime(time.Now().UTC()), CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateContact(ctx, contact); err != nil {
		t.Fatal(err)
	}

	first, err := f.engine.QueueWelcome(ctx, newsletter, contact)
	if err != nil || first.Skipped != "" {
		t.Fatalf("first = %+v %v", first, err)
	}
	if f.fake.SentCount() != 1 {
		t.Fatalf("sent = %d", f.fake.SentCount())
	}
	second, err := f.engine.QueueWelcome(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if second.Skipped != "already sent" {
		t.Fatalf("second = %+v", second)
	}

	// A newsletter that confirms signups has no welcome email at all: the confirmation is the one
	// email a new subscriber gets.
	pending := pendingContact(t, f, "waiting@example.com")
	skipped, err := f.engine.QueueWelcome(ctx, f.confirmingNewsletter(t), pending)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.Skipped != "welcome email is off" {
		t.Fatalf("confirming newsletter = %+v", skipped)
	}

	// A contact who is not active gets nothing either, even where a welcome email exists.
	waiting := model.Contact{
		ID: ids.New(), NewsletterID: newsletter.ID, Email: "not-active@example.com",
		Status: model.ContactPending, CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateContact(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	skipped, err = f.engine.QueueWelcome(ctx, newsletter, waiting)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.Skipped != "contact is not active" {
		t.Fatalf("pending contact = %+v", skipped)
	}
}

// 欢迎信关掉时什么都不发。
func TestWelcomeEmailOffIsSkipped(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	newsletter := f.newsletter
	newsletter.WelcomeEnabled = false
	if err := f.db.UpdateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}
	contact := model.Contact{
		ID: ids.New(), NewsletterID: newsletter.ID, Email: "new@example.com",
		Status: model.ContactActive, CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateContact(ctx, contact); err != nil {
		t.Fatal(err)
	}
	result, err := f.engine.QueueWelcome(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != "welcome email is off" {
		t.Fatalf("result = %+v", result)
	}
}

// 欢迎+确认信共享每 newsletter 每 10 分钟 100 封的上限。
func TestSignupEmailsShareTheNewsletterCap(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	newsletter := f.newsletter

	seed := make([]model.Message, 0, f.engine.cfg.Limits.WelcomeEmailsPer10Minute)
	for i := 0; i < f.engine.cfg.Limits.WelcomeEmailsPer10Minute; i++ {
		seed = append(seed, model.Message{
			ID: ids.New(), Kind: model.KindWelcome, NewsletterID: newsletter.ID,
			ContactID: ids.New(), Email: "someone@example.com",
			Status: model.MessageSent, CreatedAt: time.Now().UTC(),
		})
	}
	if _, err := f.db.InsertMessages(ctx, seed); err != nil {
		t.Fatal(err)
	}

	contact := model.Contact{
		ID: ids.New(), NewsletterID: newsletter.ID, Email: "new@example.com",
		Status: model.ContactActive, CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateContact(ctx, contact); err != nil {
		t.Fatal(err)
	}
	result, err := f.engine.QueueWelcome(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != "rate limited" {
		t.Fatalf("result = %+v, want rate limited", result)
	}
	if f.fake.SentCount() != 0 {
		t.Fatalf("sent = %d, want none", f.fake.SentCount())
	}
}

// 被抑制的地址不发欢迎信。
func TestSuppressedContactsGetNoSignupEmail(t *testing.T) {
	f := newFixture(t, 0)
	ctx := context.Background()
	newsletter := f.newsletter
	contact := model.Contact{
		ID: ids.New(), NewsletterID: newsletter.ID, Email: "bounced@example.com",
		Status: model.ContactActive, CreatedAt: time.Now().UTC(),
	}
	if err := f.db.CreateContact(ctx, contact); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AddSuppression(ctx, contact.Email, model.SuppressBounce, "ses"); err != nil {
		t.Fatal(err)
	}
	result, err := f.engine.QueueWelcome(ctx, newsletter, contact)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != "suppressed" {
		t.Fatalf("result = %+v", result)
	}
}

// 发送关掉时两封信都不发。
func TestSignupEmailsRefuseWhenSendingIsOff(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	f.engine.cfg.SendingOn = false
	if _, err := f.engine.QueueWelcome(ctx, f.newsletter, f.contacts[0]); err == nil {
		t.Error("welcome sent while off")
	}
	if _, err := f.engine.QueueConfirmation(ctx, f.newsletter, f.contacts[0]); err == nil {
		t.Error("confirmation sent while off")
	}
}
