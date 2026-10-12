// Package mailer_test holds the phase 8 throughput tests.
//
// They are the completion criteria of the phase, and they are out here rather than in the package's
// own tests for one reason: they talk to a fake SES over HTTP with the real SESv2 client.
// internal/fakessesrv imports this package, so a test inside it cannot import the fake back; an
// external test package can, and what it measures is then the path a deployment uses — request bodies,
// a real HTTP client, the real quota reader, the real store.
package mailer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/ratectl"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/fakessesrv"
	"mailcheap/internal/mailer"
	"mailcheap/internal/store"
)

// latency is what the phase's criteria are written against: a send that takes 80 ms.
const latency = 80 * time.Millisecond

// workers is how many sends are in flight. It is not the 16 the invariant tests use, and the
// arithmetic is the point: n in flight at 80 ms each can do n/0.08 sends a second, so reaching the 450
// the account allows needs at least 36 — a concurrency of 16 tops out at 200 a second and would fail
// the criterion for a reason that has nothing to do with the code under test.
const workers = 64

// messagesFor picks the size of a test: the small one runs everywhere, the large one is what the phase
// is actually for, and it is opt-in because twenty thousand sends take a minute.
//
//	MAILCHEAP_LONG_TESTS=1 go test -run Throughput ./internal/mailer/
func messagesFor(t *testing.T, small, large int) int {
	t.Helper()
	if os.Getenv("MAILCHEAP_LONG_TESTS") != "" {
		return large
	}
	return small
}

// world is a deployment: a database, a fake SES over HTTP, the real SES clients pointed at it, and one
// campaign whose audience is already in the list.
type world struct {
	db       *store.DB
	engine   *mailer.Engine
	control  *ratectl.Controller
	fake     *mailer.Fake
	server   *httptest.Server
	campaign model.Campaign
	contacts []model.Contact
}

func newWorld(t *testing.T, contacts int, options ...fakessesrv.Option) *world {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "throughput.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	signer, err := sign.New("throughput-test-secret-at-least-32-chars")
	if err != nil {
		t.Fatal(err)
	}
	fake := mailer.NewFake()
	server := httptest.NewServer(fakessesrv.New(fake, options...).Handler())
	t.Cleanup(server.Close)

	// The SDK signs what it sends; the fake does not check the signature.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	sesOptions := mailer.SESOptions{
		Region: "eu-west-1", Endpoint: server.URL, AccessKeyID: "test", SecretKey: "test",
	}
	sender, err := mailer.NewSES(ctx, sesOptions, "", true)
	if err != nil {
		t.Fatal(err)
	}
	// The account's own numbers, read the way a deployment reads them.
	reader, err := mailer.NewSESQuota(ctx, sesOptions)
	if err != nil {
		t.Fatal(err)
	}
	quota, err := reader.Quota(ctx)
	if err != nil {
		t.Fatalf("the quota reader could not read the fake: %v", err)
	}
	control := ratectl.New(ratectl.Config{Headroom: 0.9})
	control.SetQuota(quota, time.Now().UTC())

	engine := mailer.NewEngine(db, sender, signer, mailer.Config{
		AppURL: "https://mail.example.com", SendingOn: true,
		SendRatePerSec: int(control.Rate(time.Now().UTC())),
		Limits: model.Limits{
			MaxEmailBytes: 100_000, BounceRate: 0.02, ComplaintRate: 0.0008,
			MaxSendsPerDay: 1_000_000, WelcomeEmailsPer10Minute: 100,
		},
		PollInterval:    10 * time.Millisecond,
		SendConcurrency: workers,
		RateControl:     control,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(engine.Close)

	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "field-notes", Name: "Field notes",
		FromName: "Jane Doe", FromEmail: "jane@example.net", PostalAddress: "1 Main St, Paris",
	}
	if err := db.CreateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}
	audience := make([]model.Contact, 0, contacts)
	for i := 0; i < contacts; i++ {
		contact := model.Contact{
			ID: ids.New(), NewsletterID: newsletter.ID,
			Email: fmt.Sprintf("reader%d@example.com", i), Status: model.ContactActive,
			CreatedAt: time.Now().UTC(),
		}
		if err := db.CreateContact(ctx, contact); err != nil {
			t.Fatal(err)
		}
		audience = append(audience, contact)
	}
	campaign := model.NewCampaign(newsletter.ID, "Issue 12", time.Now().UTC())
	campaign.Body = "# Hello\n\nRead [this](https://example.com)."
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}

	return &world{db: db, engine: engine, control: control, fake: fake, server: server,
		campaign: campaign, contacts: audience}
}

// drain sends everything queued, the way the serve process's sender does, and reports how long it took.
func (w *world) drain(t *testing.T, within time.Duration) time.Duration {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	deadline := start.Add(within)
	for {
		result, err := w.engine.SendBatch(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		// A batch that was throttled disposes of nothing and asks to be run again; a batch that found
		// nothing to claim is the end.
		if result.Done() == 0 && !result.Retry {
			return time.Since(start)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d messages had been accepted when the time ran out",
				w.fake.SentCount(), len(w.contacts))
		}
	}
}

// exactlyOnce fails when anyone got a second copy, or nobody's copy arrived.
func (w *world) exactlyOnce(t *testing.T, want int) {
	t.Helper()
	sent := w.fake.SentPerRecipient()
	if len(sent) != want {
		t.Fatalf("%d addresses were written to, want %d", len(sent), want)
	}
	for email, count := range sent {
		if count != 1 {
			t.Errorf("%s received %d copies", email, count)
		}
	}
}

// 完成标志第一条：假 SES 80 ms 延迟、上报 500/s，全部在 `N / 450 × 1.2` 秒内发完，零重复、零失败。
//
// The rate is a promise in both directions: no slower than the account allows, and no faster. The
// lower bound is the half that would be easy to lose — a deployment that trickles at one a second
// would pass an upper bound on its own.
func TestSendingPacesItselfToTheAccountAndLosesNothing(t *testing.T) {
	messages := messagesFor(t, 600, 20_000)
	w := newWorld(t, messages,
		fakessesrv.WithQuota(fakessesrv.Quota{MaxSendRate: 500, Max24HourSend: -1}),
		fakessesrv.WithLatency(latency, latency/10))
	ctx := context.Background()

	if queued, err := w.engine.Queue(ctx, w.campaign.ID, mailer.Charge{}); err != nil {
		t.Fatal(err)
	} else if !queued.Queued || queued.Recipients != messages {
		t.Fatalf("queued = %+v", queued)
	}

	const allowed = 500 * 0.9
	ideal := time.Duration(float64(messages) / allowed * float64(time.Second))
	elapsed := w.drain(t, 10*time.Minute)

	// The budget is the criterion's 1.2, plus a fixed allowance for everything that is not sending:
	// the claims, the settlements, the suppressions read and the process itself. A fixed number rather
	// than a percentage, because none of those scale with the send — and it is what keeps the small
	// run, which CI does, from measuring the machine it is on.
	budget := ideal*12/10 + 250*time.Millisecond
	if elapsed > budget {
		t.Fatalf("%d messages took %s; the account allows %v a second, so %s is the budget",
			messages, elapsed, allowed, budget)
	}
	if elapsed < ideal*9/10 {
		t.Fatalf("%d messages took %s, faster than the %s the rate allows: the limit is not being kept",
			messages, elapsed, ideal*9/10)
	}
	if failures := len(w.fake.Sent()) - w.fake.SentCount(); failures != 0 || len(w.fake.Sent()) != messages {
		t.Fatalf("%d sends were accepted of %d messages", w.fake.SentCount(), messages)
	}
	w.exactlyOnce(t, messages)
	if limits := limitsOf(t, w); limits.Throttled != 0 {
		t.Fatalf("%d sends were throttled although the fake enforces nothing: the rate was exceeded",
			limits.Throttled)
	}
	t.Logf("%d messages in %s (%v a second, target %v)", messages, elapsed,
		float64(messages)/elapsed.Seconds(), allowed)
}

// 完成标志第二条：假 SES 实际上只放行 300/s，而上报的仍是 500。限速要在 60 秒内收敛，每封最终恰好一次。
//
// Converging is not "reaching exactly 300": AIMD settles below a limit it cannot see, and creeping back
// up until it is refused again is how it stays there. What is asserted is what that means in
// practice — everything is sent, once, inside the minute, and the refusals stop being an appreciable
// share of the work rather than continuing at the rate the reported number would suggest.
func TestTheRateConvergesWhenTheAccountAllowsLessThanItReports(t *testing.T) {
	messages := messagesFor(t, 1200, 6000)
	w := newWorld(t, messages,
		fakessesrv.WithQuota(fakessesrv.Quota{MaxSendRate: 500, Max24HourSend: -1}),
		fakessesrv.WithEnforcedRate(300))
	ctx := context.Background()

	if queued, err := w.engine.Queue(ctx, w.campaign.ID, mailer.Charge{}); err != nil {
		t.Fatal(err)
	} else if !queued.Queued {
		t.Fatalf("queued = %+v", queued)
	}

	elapsed := w.drain(t, time.Minute)
	if elapsed > time.Minute {
		t.Fatalf("the run took %s: the rate did not converge inside the minute", elapsed)
	}
	limits := limitsOf(t, w)
	if limits.Throttled == 0 {
		t.Fatal("the fake never refused anything, so this test did not exercise a lying account")
	}
	// A deployment that never backed off would be refused roughly a third of the time (450 asked for,
	// 300 allowed); backing off once is enough to bring it under the limit.
	if limits.Throttled > messages/10 {
		t.Fatalf("%d of %d sends were refused: the backoff did not converge",
			limits.Throttled, messages)
	}
	w.exactlyOnce(t, messages)
	t.Logf("%d messages in %s (%v a second against an enforced 300), %d refusals",
		messages, elapsed, float64(messages)/elapsed.Seconds(), limits.Throttled)
}

// 完成标志第三条：账号剩下的额度装不下这一步时，整次拒绝——不建 message、不动 status。
func TestASpentAllowanceRefusesTheWholeStep(t *testing.T) {
	w := newWorld(t, 1000, fakessesrv.WithQuota(fakessesrv.Quota{
		MaxSendRate: 500, Max24HourSend: 1500, SentLast24Hours: 1000,
	}))
	ctx := context.Background()

	queued, err := w.engine.Queue(ctx, w.campaign.ID, mailer.Charge{})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Queued {
		t.Fatalf("queued = %+v, want a refusal: the account has 500 sends left and this step is 1000",
			queued)
	}
	campaign, err := w.db.Campaign(ctx, w.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.Status != model.CampaignDraft {
		t.Fatalf("status = %s, want the campaign left alone", campaign.Status)
	}
	var messages int
	if err := w.db.Row(ctx, `SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 0 {
		t.Fatalf("%d messages were written for a step that was refused", messages)
	}
}

// limitsOf is what the fake actually did, read over HTTP: the test asserts on what a caller can see.
func limitsOf(t *testing.T, w *world) fakessesrv.Limits {
	t.Helper()
	response, err := w.server.Client().Get(w.server.URL + "/_limits")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var limits fakessesrv.Limits
	if err := json.NewDecoder(response.Body).Decode(&limits); err != nil {
		t.Fatal(err)
	}
	return limits
}
