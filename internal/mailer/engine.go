package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/smithy-go"

	"nibwire/internal/alerts"
	"nibwire/internal/core/cleanup"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/providers"
	"nibwire/internal/core/ratectl"
	"nibwire/internal/core/route"
	"nibwire/internal/core/rules"
	"nibwire/internal/core/sign"
	"nibwire/internal/core/slug"
	"nibwire/internal/core/validate"
	"nibwire/internal/jobs"
	"nibwire/internal/store"
)

// releaseTimeout bounds the bookkeeping a stopped batch still owes the database.
const releaseTimeout = 10 * time.Second

// Config is what the engine needs from internal/config.
type Config struct {
	AppURL string
	// ArchiveHosts is the ARCHIVE_HOSTS value, so the Read-online link points at the newsletter's own
	// archive domain when it has one (audit F2).
	ArchiveHosts   string
	SendingOn      bool
	SendRatePerSec int
	Limits         model.Limits
	// PollInterval is how often Run looks for work.
	PollInterval time.Duration
	// StrayInterval is how often Run looks for messages on a channel with no lane; zero means the
	// default (see strayInterval).
	StrayInterval time.Duration
	// SendConcurrency is how many sends may be in flight at once (SEND_CONCURRENCY). One makes the
	// engine behave exactly as it did before this phase: a single file of messages.
	SendConcurrency int
	// RateControl is the controller the quota poller writes readings into and this engine reports
	// throttles and successes to. It is one object on purpose: the rate the account's quota implies,
	// the rate a throttle halved and the rate the bucket enforces all have to be the same number
	// (D8-1, D17). Optional — without it the bucket keeps SendRatePerSec, which is what the tests that
	// do not care about the rate do.
	RateControl *ratectl.Controller
	// Alerts tells the operator what went wrong. It is optional: the CLI builds an engine with no
	// server behind it, and there is nobody to alert there.
	Alerts *alerts.Notifier
	// ChannelLimits are the configured channels' own ceilings, by name (channels.max_per_second). The
	// default channel's are SendRatePerSec and RateControl above.
	ChannelLimits map[string]ChannelLimit
	// ChannelConcurrency is how many sends each configured channel may have in flight at once; the
	// default channel's is SendConcurrency. Zero means SendConcurrency.
	ChannelConcurrency int
	// ChannelProblems are the enabled channels that could not be built, by name (App.ChannelProblems).
	// A campaign message on one of them is held and its campaign paused, with the problem as reason.
	ChannelProblems map[string]error
}

// BatchResult is what one batch did (upstream sendMessageBatch's result).
type BatchResult struct {
	Sent       int
	Failed     int
	Suppressed int
	Skipped    int
	// Retry is set when sending stopped for a reason the next run can fix.
	Retry bool
}

// Done is how many messages the batch disposed of, one way or another.
func (r BatchResult) Done() int { return r.Sent + r.Failed + r.Suppressed + r.Skipped }

// SendingRate is how fast this deployment may send and how much of the account's 24-hour allowance is
// left. `campaign check` prints it, and the estimate it implies, because "queue 200,000 recipients"
// should not look like the same thing as "queue 200" (D17, D18).
type SendingRate struct {
	// PerSecond is the current ceiling, in sends per second.
	PerSecond float64 `json:"perSecond"`
	// Remaining24h is what the account has left in its 24-hour window, or ratectl.Unlimited.
	Remaining24h int `json:"remaining24h"`
	// Known is false before a quota has been read: PerSecond is then the floor rather than a reading,
	// and Remaining24h is 0 rather than a promise.
	Known bool `json:"quotaKnown"`
}

// Estimate is how long n sends take at this rate.
func (r SendingRate) Estimate(n int) time.Duration {
	rate := r.PerSecond
	if rate <= 0 {
		rate = 1
	}
	return time.Duration(float64(n) / rate * float64(time.Second))
}

// SendingRate reports the rate and the allowance.
//
// Without a controller there is no quota to read — this process paces its sends by the bucket alone —
// so the ceiling is the bucket's own rate and the allowance is not claimed to be known.
func (e *Engine) SendingRate() SendingRate {
	// A nil engine is a server built without a mailer, which is what a tools-only test does: there is
	// no rate, and asking must not be a nil dereference.
	if e == nil {
		return SendingRate{}
	}
	// The default channel's, until step 2c reports a rate per channel.
	l := e.defaultLane()
	if l.control == nil {
		return SendingRate{PerSecond: l.bucket.Rate()}
	}
	_, known := l.control.Quota()
	return SendingRate{
		PerSecond:    l.control.Rate(time.Now().UTC()),
		Remaining24h: l.control.Remaining24h(),
		Known:        known,
	}
}

// sesQuotaRefusal is D18's third ceiling: the account's own 24-hour allowance, which is the one SES
// enforces and the only one nothing else here can see.
//
// It is checked before anything is written, so a step that does not fit is refused whole — no
// messages, no charge against the day, and the campaign stays where it is. Sending the part that fits
// would leave the campaign looking half sent with no record of why, and the operator is the one who
// gets to decide to split a send: that is what --limit is for.
//
// It is asked per channel, of the account behind the channel the mail would go out through: one
// account's allowance running out must not stop another channel's mail.
func (e *Engine) sesQuotaRefusal(charge store.Charge) string {
	for _, channel := range charge.Channels() {
		n := charge[channel]
		l, ok := e.laneFor(channel)
		if !ok || l.control == nil {
			continue
		}
		// An allowance nobody has read is not an allowance of zero. ratectl answers 0 there, and
		// refusing every send on that number would turn "the quota could not be read" into "the
		// deployment cannot send at all" — the opposite of what D8-2 chose: one alert and one email
		// per second, slowly, which is what the rate floor is for.
		if _, known := l.control.Quota(); !known {
			continue
		}
		remaining := l.control.Remaining24h()
		if n <= remaining {
			continue
		}
		account := "the SES account"
		if channel != model.DefaultChannel {
			account = "the SES account behind channel " + channel
		}
		if remaining <= 0 {
			return fmt.Sprintf("%s has no sends left in its 24-hour window and this step is %d: the "+
				"window opens as the previous day's sends age out", account, n)
		}
		return fmt.Sprintf("%s has %d sends left in its 24-hour window and this step is %d: send it "+
			"in steps of at most %d, or wait for the window to reset", account, remaining, n, remaining)
	}
	return ""
}

// QueueResult is what Queue and QueueStep report back.
type QueueResult struct {
	Recipients int
	LastChance int
	Added      int
	// Queued is false when the campaign could not start; Reason says why.
	Queued bool
	Reason string
}

// TestResult is one test email.
//
// `nibwire campaign test --json` prints it as is, so the keys are pinned: ProviderMessageID keeps
// its pre-channel key, SESMessageID, for CLI compatibility.
type TestResult struct {
	MessageID         string    `json:"MessageID"`
	ProviderMessageID string    `json:"SESMessageID"`
	SentAt            time.Time `json:"SentAt"`
}

// Charge says whose daily budget a step spends (design §11, layer 2). The zero value is the system
// pool — the CLI's own sends, with only MAX_SENDS_PER_DAY above them — and a set CredentialID is an
// `api` key or an `mcp` token, which carries a daily_budget of its own underneath the global one.
//
// The credential is read again when the step runs, not captured when it was queued: a job for a
// scheduled send can sit for hours, and a budget that was lowered or a credential that was revoked
// in the meantime has to apply to it.
type Charge struct {
	CredentialID string
}

// system is the charge with no credential behind it.
func (c Charge) system() bool { return c.CredentialID == "" }

// Engine sends queued messages, each through the channel it was queued on.
type Engine struct {
	store *store.DB
	// lanes are the channels' lanes by name; order is the same lanes, the default channel's first.
	lanes  map[string]*lane
	order  []*lane
	signer *sign.Signer
	cfg    Config
	log    *slog.Logger
}

// NewEngine builds an engine that sends through channels, which must not be nil. Each channel gets a
// lane — a bucket, and a controller when the engine follows quotas — shared by every caller, so each
// rate is a process-wide cap on that channel rather than a per-batch one.
func NewEngine(db *store.DB, channels *Registry, signer *sign.Signer, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.SendRatePerSec < 1 {
		// A bucket with a rate of zero would never hand out a slot. One a second is what an unknown
		// account gets anyway (D8-2), and the controller corrects it as soon as there is a reading.
		cfg.SendRatePerSec = 1
	}
	if log == nil {
		log = slog.Default()
	}
	lanes, order := newLanes(channels, cfg)
	return &Engine{store: db, lanes: lanes, order: order, signer: signer, cfg: cfg, log: log}
}

// Close stops every lane's rate limiter.
func (e *Engine) Close() {
	for _, l := range e.order {
		l.bucket.Close()
	}
}

// SendBatch sends one batch. With no ids every lane claims its own channel's messages,
// rules.SendBatchSize at a time, until its channel has none left (the CLI's drain); with ids it sends
// exactly those, each through its own channel's lane (welcome and confirmation emails). The lanes run
// side by side and their results are added up.
func (e *Engine) SendBatch(ctx context.Context, messageIDs []string) (BatchResult, error) {
	result := BatchResult{}
	// A cancelled context means "stop", never "failed": nothing is claimed, nothing is lost, and the
	// caller is told to stop rather than handed an error it would report as a broken send. This is
	// reachable even when the caller checks first — a select over a done context and a ready ticker
	// may pick either — so the check has to live here too.
	if ctx.Err() != nil {
		result.Retry = true
		return result, nil
	}
	if !e.cfg.SendingOn {
		result.Retry = true
		return result, errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording
	}

	if len(messageIDs) == 0 {
		return e.sendAll(ctx)
	}
	claimed, err := e.store.ClaimMessagesByID(ctx, messageIDs)
	if err != nil {
		return result, err
	}
	// Each message goes out through its own channel's lane. One on a channel this engine has no
	// lane for is a stray: it is failed, or held with its campaign paused (sendStrays).
	byChannel := map[string][]model.Message{}
	for _, message := range claimed {
		byChannel[messageChannel(message)] = append(byChannel[messageChannel(message)], message)
	}
	var parts []batchPart
	for _, l := range e.order {
		mine := byChannel[l.name()]
		delete(byChannel, l.name())
		if len(mine) > 0 {
			parts = append(parts, func(ctx context.Context) (BatchResult, error) {
				return e.runLane(ctx, l, mine, nil)
			})
		}
	}
	var strays []model.Message
	for _, message := range claimed {
		if _, stray := byChannel[messageChannel(message)]; stray {
			strays = append(strays, message)
		}
	}
	if len(strays) > 0 {
		parts = append(parts, func(ctx context.Context) (BatchResult, error) {
			return e.sendStrays(ctx, strays)
		})
	}
	result, err = together(ctx, parts)
	result.Skipped += len(messageIDs) - len(claimed)
	return result, err
}

// batchPart is one lane's share of a batch, or the strays'.
type batchPart func(context.Context) (BatchResult, error)

// together runs every part at once and adds up what they did. The parts are independent — one
// channel's lane neither waits for nor stops another's — so each finishes on its own, and the first
// error (in lane order) is the batch's.
func together(ctx context.Context, parts []batchPart) (BatchResult, error) {
	if len(parts) == 1 {
		return parts[0](ctx)
	}
	results := make([]BatchResult, len(parts))
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	for i, part := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = part(ctx)
		}()
	}
	wg.Wait()
	total := BatchResult{}
	var firstErr error
	for i := range parts {
		total.add(results[i])
		if firstErr == nil {
			firstErr = errs[i]
		}
	}
	return total, firstErr
}

// add counts another part's work into r.
func (r *BatchResult) add(other BatchResult) {
	r.Sent += other.Sent
	r.Failed += other.Failed
	r.Suppressed += other.Suppressed
	r.Skipped += other.Skipped
	r.Retry = r.Retry || other.Retry
}

// sendAll is a batch with no ids: every lane claims and sends its own channel's messages, at once,
// and the strays are dealt with alongside.
func (e *Engine) sendAll(ctx context.Context) (BatchResult, error) {
	parts := make([]batchPart, 0, len(e.order)+1)
	for _, l := range e.order {
		parts = append(parts, func(ctx context.Context) (BatchResult, error) { return e.sendLane(ctx, l) })
	}
	parts = append(parts, e.sendStrayBatch)
	return together(ctx, parts)
}

// sendLane claims and sends one lane's messages until its channel has none left or the lane has to
// stop.
func (e *Engine) sendLane(ctx context.Context, l *lane) (BatchResult, error) {
	claim := func(ctx context.Context) ([]model.Message, error) {
		return e.store.ClaimChannelMessages(ctx, l.name(), rules.SendBatchSize)
	}
	first, err := claim(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		// A shutdown that landed before the first claim: nothing was claimed, and a stop is not a
		// failure.
		return BatchResult{Retry: true}, nil
	case err != nil:
		return BatchResult{}, err
	case len(first) == 0:
		return BatchResult{}, nil
	}
	return e.runLane(ctx, l, first, claim)
}

// messageChannel is the channel a message goes out through; empty is the default channel, as it is
// when a message is written.
func messageChannel(message model.Message) string {
	if message.Channel == "" {
		return model.DefaultChannel
	}
	return message.Channel
}

// runLane sends messages, all of them on l's channel, through l: its bucket, its workers and its
// channel. claimMore is where the next chunk comes from, or nil when messages is all there is.
func (e *Engine) runLane(ctx context.Context, l *lane, messages []model.Message,
	claimMore func(context.Context) ([]model.Message, error)) (BatchResult, error) {
	result := BatchResult{}
	suppressed, err := e.store.SuppressedEmails(ctx)
	if err != nil {
		return result, err
	}
	suppressedSet := make(map[string]bool, len(suppressed))
	for _, email := range suppressed {
		suppressedSet[email] = true
	}

	// Every campaign this call touches settles at the end, so the set spans the chunks.
	campaigns := map[string]bool{}
	prepare := func(chunk []model.Message) error {
		// A campaign that is already over its bounce or complaint limit pauses before anything else
		// goes out, as upstream does at the top of sendMessageBatch.
		for _, message := range chunk {
			if message.CampaignID == "" {
				continue
			}
			if _, seen := campaigns[message.CampaignID]; seen {
				continue
			}
			campaigns[message.CampaignID] = true
			if _, err := e.pauseIfUnhealthy(ctx, message.CampaignID); err != nil {
				return err
			}
		}
		return nil
	}
	if err := prepare(messages); err != nil {
		// Nothing was handed out, so nothing may stay claimed: left claimed, these would be recovered
		// as unknown — mail that is neither sent nor going to be (D14).
		e.releaseAll(ctx, messages)
		return result, err
	}

	// claimMore is nil when there is nothing more to ask for: the explicit-ids path has been given
	// exactly what it is to send, and a queue that came back empty has nothing left.
	//
	// Claiming on demand rather than in waves is what keeps the pool busy. A batch that claimed fifty
	// messages, sent them and only then claimed the next fifty would stand idle for one request
	// latency at every boundary — at 80 ms a send and 450 a second, that is a fifth of the account's
	// rate thrown away, and it gets worse the faster the account is.

	// The lane's batch is a dispatcher and a pool of workers, both its own: a lane that is throttled
	// or waiting on a slow rate holds only its own workers.
	//
	// The dispatcher is this goroutine: it hands one message at a time to a worker, collects what comes
	// back, and decides whether to hand out any more. Every decision that can stop a batch — the
	// context, a campaign that paused itself, too many failures in a row, a bookkeeping error — is
	// taken here, in one place, on one goroutine, which is what keeps "stop" from meaning four
	// different things.
	//
	// What was never handed out goes back to the queue: left claimed they would be recovered as
	// unknown on the next start, which means silently dropping a send (D14). What a worker is holding
	// is not released — it finishes, because deliver only reports a cancellation from before the
	// request was made (see deliver).
	concurrency := e.laneConcurrency(l)
	if concurrency > len(messages) {
		concurrency = len(messages)
	}
	jobs := make(chan model.Message)
	outcomes := make(chan sendOutcome, concurrency)
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for message := range jobs {
				outcomes <- e.sendOne(ctx, l, message, suppressedSet)
			}
		}()
	}

	chunk := messages
	var (
		next                int
		outstanding         int
		stopped             bool
		firstErr            error
		consecutiveFailures int
	)
	apply := func(out sendOutcome) {
		if out.sent {
			result.Sent++
		}
		result.Failed += out.failed
		result.Suppressed += out.suppressed
		result.Skipped += out.skipped
		result.Retry = result.Retry || out.retry
		switch {
		case out.failed > 0:
			consecutiveFailures += out.failed
		case out.sent:
			consecutiveFailures = 0
		}
		if out.err != nil && firstErr == nil {
			firstErr = out.err
		}
		if out.stop {
			stopped = true
		}
		if out.err != nil || (out.failed > 0 && out.campaignID != "" &&
			consecutiveFailures >= rules.MaxConsecutiveFailures) {
			stopped = true
			if out.err == nil {
				if err := e.pause(ctx, out.campaignID,
					fmt.Sprintf("%d sends failed in a row", consecutiveFailures)); err != nil {
					firstErr = err
				}
			}
		}
	}

	for {
		if ctx.Err() != nil {
			result.Retry = true
			stopped = true
		}
		if !stopped && next < len(chunk) {
			select {
			case jobs <- chunk[next]:
				next++
				outstanding++
				continue
			case out := <-outcomes:
				outstanding--
				apply(out)
				continue
			}
		}
		if !stopped && claimMore != nil {
			// This chunk is exhausted. Claim the next one now, while the workers are still finishing
			// it: the claim is a write, not a send, and it is the only thing that would otherwise
			// stand between one chunk and the next.
			more, err := claimMore(ctx)
			switch {
			case err != nil && ctx.Err() != nil:
				// The claim is where a shutdown that landed between two chunks shows up, and a stop
				// is not a failure: nothing was claimed and the next run picks up the rest.
				result.Retry, stopped = true, true
			case err != nil:
				firstErr, stopped = err, true
			case len(more) == 0:
				claimMore = nil
			default:
				// The new chunk becomes the dispatcher's before its preparation runs, so a failure
				// there leaves it to be released by the same code as any other message the batch
				// never handed out.
				chunk, next = more, 0
				if err := prepare(more); err != nil {
					firstErr, stopped = err, true
					break
				}
				continue
			}
		}
		if outstanding == 0 {
			break
		}
		// Nothing more goes out: wait for what is in flight and count it.
		apply(<-outcomes)
		outstanding--
	}
	close(jobs)
	workers.Wait()

	if next < len(chunk) {
		e.releaseAll(ctx, chunk[next:])
	}
	if firstErr != nil {
		return result, firstErr
	}

	// Settling is the last bookkeeping of a batch that may have just been cancelled, so it runs on
	// the same detached context as the other write-backs.
	for campaignID := range campaigns {
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			return e.settle(writeCtx, campaignID)
		}); err != nil {
			return result, err
		}
	}
	return result, nil
}

// sendOutcome is what one worker did with one message. The dispatcher decides what to do with it; the
// worker only reports.
type sendOutcome struct {
	sent       bool
	skipped    int
	suppressed int
	failed     int
	retry      bool
	// stop tells the dispatcher to hand out nothing more: this is a condition the next message would
	// meet too.
	stop bool
	// err is a failure of the bookkeeping itself — the batch cannot go on — and campaignID is the
	// campaign the failure counter belongs to.
	err        error
	campaignID string
}

// sendOne is one message from a claimed batch, all the way to its write-back: the context it needs,
// whether it may still go out, the send itself, and what became of it.
//
// It runs on a worker goroutine, so it may not touch anything the dispatcher owns. Everything it does
// is either its own memory, a store write, or the sender — all of which are already safe for
// concurrent use.
func (e *Engine) sendOne(ctx context.Context, l *lane, message model.Message,
	suppressed map[string]bool) sendOutcome {
	// Anything that leaves this message claimed when the worker returns is a message the next start
	// recovers as unknown — a silently dropped send (D14). So every failure from here on releases it,
	// on the detached context the bookkeeping uses, because the usual reason to be on one of these
	// paths is that the batch's context was just cancelled.
	abandon := func(err error) sendOutcome {
		e.releaseAll(ctx, []model.Message{message})
		if ctx.Err() != nil {
			// The usual reason to be here is that the batch's context was just cancelled, and a stop
			// is not a failure: Run's select picks at random between a done context and a ready
			// ticker, so reporting this as an error would call a graceful shutdown a broken send.
			return sendOutcome{retry: true, stop: true, campaignID: message.CampaignID}
		}
		return sendOutcome{err: err, stop: true, campaignID: message.CampaignID}
	}

	loaded, err := e.store.LoadMessageContext(ctx, message)
	if err != nil {
		return abandon(fmt.Errorf("load message %s: %w", message.ID, err))
	}
	// The campaign may have been paused or finished since the audience was chosen.
	if rules.IsCampaignKind(message.Kind) &&
		(loaded.Campaign == nil || loaded.Campaign.Status != model.CampaignSending) {
		if err := e.releaseMessage(ctx, message.ID); err != nil {
			return abandon(err)
		}
		return sendOutcome{skipped: 1}
	}

	required := model.ContactActive
	if message.Kind == model.KindConfirmation {
		required = model.ContactPending
	}
	if loaded.Contact.Status != required || suppressed[message.Email] {
		if err := e.markSuppressed(ctx, message.ID); err != nil {
			return abandon(err)
		}
		return sendOutcome{suppressed: 1}
	}

	delivery, err := e.deliver(ctx, l, message, loaded, nil)
	if err != nil {
		// A cancelled context means we are stopping, not that the message is bad: it never reached
		// SES, so it goes back to the queue instead of being marked failed.
		//
		// Releasing it is safe only because deliver cannot report a cancellation once it has called
		// SES: the call runs on a detached context (see deliver), so the only cancellation it can
		// surface is the send slot's, from before the request was made.
		if ctx.Err() != nil {
			e.releaseAll(ctx, []model.Message{message})
			return sendOutcome{retry: true, stop: true}
		}
		outcome, stop := e.handleSendError(ctx, l, message, loaded, err)
		return sendOutcome{
			failed: outcome.failed, retry: outcome.retry, stop: stop, campaignID: message.CampaignID,
		}
	}
	// The channel took it: that is the event its rate creeps back up on (D8-1).
	l.onSuccess()
	if err := e.recordSent(ctx, message, loaded, delivery); err != nil {
		// The message went out and its record did not. Releasing it would send it twice, so it stays
		// claimed: the next start recovers it as unknown, which is "we do not know" and never "send it
		// again" (D14).
		return sendOutcome{err: err, stop: true, campaignID: message.CampaignID}
	}
	return sendOutcome{sent: true, campaignID: message.CampaignID}
}

// releaseMessage, markSuppressed and the counters run on the detached context: they are the
// bookkeeping of a batch that is stopping, and losing them is what makes a message look unclaimed to
// the next start rather than merely unrecorded.
func (e *Engine) releaseMessage(ctx context.Context, id string) error {
	return e.writeBack(ctx, func(writeCtx context.Context) error {
		return e.store.ReleaseMessage(writeCtx, id)
	})
}

func (e *Engine) markSuppressed(ctx context.Context, id string) error {
	return e.writeBack(ctx, func(writeCtx context.Context) error {
		return e.store.MarkMessageSuppressed(writeCtx, id)
	})
}

// writeBack runs the bookkeeping a completed SES call owes the database.
//
// It runs on a context detached from the batch's, because a stop is the usual reason to be here:
// the caller's context was just cancelled (SIGTERM), and a write that failed would leave a message
// that SES accepted looking unclaimed. The next start would call it unknown — a sent email losing
// its record, which is exactly what D14 exists to prevent (design §3.1).
func (e *Engine) writeBack(ctx context.Context, write func(context.Context) error) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	return write(writeCtx)
}

// releaseAll puts claimed messages the batch never reached back in the queue, on a detached context
// for the same reason: a release that failed would turn them into unknown on the next start.
func (e *Engine) releaseAll(ctx context.Context, messages []model.Message) {
	if len(messages) == 0 {
		return
	}
	_ = e.writeBack(ctx, func(writeCtx context.Context) error {
		for _, message := range messages {
			if err := e.store.ReleaseMessage(writeCtx, message.ID); err != nil {
				e.log.Error("release", "message", message.ID, "error", err)
			}
		}
		return nil
	})
}

// errorOutcome is what one failed send asks for: whether to stop the batch, and how to count it.
type errorOutcome struct {
	failed int
	retry  bool
}

// handleSendError classifies a failed send through the reading of the channel that refused it
// (ClassifySES for SES). Only what the channel's Send returned is classified (a sendError): an error
// the engine raised itself — rendering, signing, the rate limiter — fails the message, because the
// channel's rules know nothing about it. And only campaign messages are classified: a message outside
// a campaign (welcome, confirmation, test) always fails, since there is no campaign to pause.
func (e *Engine) handleSendError(ctx context.Context, l *lane, message model.Message,
	loaded store.MessageContext, cause error) (outcome errorOutcome, stop bool) {
	action := rules.ErrorFail
	var refused *sendError
	if message.CampaignID != "" && errors.As(cause, &refused) {
		action = l.channel.Classify(refused.err)
	}
	// Every write below runs on the detached context. We know what became of this message, so the
	// record of it must not be lost to a shutdown that arrived while it was being written: a message
	// left claimed is one the next start recovers as unknown, and a pause that was not recorded leaves
	// a hard-failing campaign looking like it is still sending.
	write := func(what string, f func(context.Context) error) {
		if err := e.writeBack(ctx, f); err != nil {
			e.log.Error(what, "message", message.ID, "error", err)
		}
	}
	switch action {
	case rules.ErrorFail:
		write("mark failed", func(writeCtx context.Context) error {
			return e.store.MarkMessageFailed(writeCtx, message.ID, cause.Error())
		})
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			return e.addFailedStats(writeCtx, message, loaded)
		}); err != nil {
			e.log.Error("failed stats", "message", message.ID, "error", err)
		}
		return errorOutcome{failed: 1}, false
	case rules.ErrorPause:
		write("release", func(writeCtx context.Context) error {
			return e.store.ReleaseMessage(writeCtx, message.ID)
		})
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			return e.pause(writeCtx, message.CampaignID, cause.Error())
		}); err != nil {
			e.log.Error("pause", "campaign", message.CampaignID, "error", err)
		}
		return errorOutcome{}, true
	case rules.ErrorThrottle:
		// The account is being asked for more than its rate. SES accepted nothing and rejected
		// nothing, so this is neither a sent message nor a failed one: it goes back to the queue, and
		// the batch stops, because the next message would meet the same wall (D8-3).
		write("release", func(writeCtx context.Context) error {
			return e.store.ReleaseMessage(writeCtx, message.ID)
		})
		l.onThrottle()
		return errorOutcome{retry: true}, true
	case rules.ErrorQuota:
		// The day's allowance is spent, so no amount of waiting will send this. The message is not a
		// failed delivery — nothing was wrong with it — so it goes back to the queue and the campaign
		// pauses with a reason the operator can act on. Resuming after the allowance resets sends the
		// rest.
		write("release", func(writeCtx context.Context) error {
			return e.store.ReleaseMessage(writeCtx, message.ID)
		})
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			return e.pause(writeCtx, message.CampaignID, rules.QuotaPauseReason)
		}); err != nil {
			e.log.Error("pause", "campaign", message.CampaignID, "error", err)
		}
		return errorOutcome{}, true
	default: // retry
		write("release", func(writeCtx context.Context) error {
			return e.store.ReleaseMessage(writeCtx, message.ID)
		})
		return errorOutcome{retry: true}, true
	}
}

// delivery is what a successful send produced.
type delivery struct {
	providerMessageID string
	sentAt            time.Time
}

// deliver renders one message and sends it through l, which is its channel's lane. content
// overrides the campaign body for test sends. What the channel's Send returned comes back as a
// sendError; every other error is the engine's own.
func (e *Engine) deliver(ctx context.Context, l *lane, message model.Message, loaded store.MessageContext,
	contentOverride *model.Campaign) (delivery, error) {
	content, err := e.contentFor(message, loaded, contentOverride)
	if err != nil {
		return delivery{}, err
	}

	now := time.Now().UTC()
	reference := sign.Ref{ContactID: loaded.Contact.ID, NewsletterID: loaded.Newsletter.ID}
	input := rules.RecipientInput{
		AppURL:       e.cfg.AppURL,
		ArchiveHosts: e.cfg.ArchiveHosts,
		Campaign:     content,
		Newsletter:   loaded.Newsletter,
		Contact:      loaded.Contact,
		MessageID:    message.ID,
		Kind:         message.Kind,
	}
	// Confirmation and last-chance links have to work, so those emails go out untracked.
	if rules.Untracked(message.Kind) {
		input.MessageID = ""
	}
	switch message.Kind {
	case model.KindConfirmation:
		token, err := e.signer.ContactToken(sign.KindConfirm, reference, now)
		if err != nil {
			return delivery{}, err
		}
		input.ConfirmURL = e.cfg.AppURL + "/confirm?token=" + token
	case model.KindLastChance:
		token, err := e.signer.ContactToken(sign.KindKeep, reference, now)
		if err != nil {
			return delivery{}, err
		}
		input.KeepURL = e.cfg.AppURL + "/keep?token=" + token
	}

	email, err := rules.BuildRecipientEmail(e.signer, input)
	if err != nil {
		return delivery{}, err
	}
	email.Headers["Feedback-ID"] = rules.FeedbackID(message.CampaignID, message.Kind, loaded.Newsletter.Slug)

	tags := map[string]string{"message_id": message.ID, "kind": string(message.Kind)}
	if message.CampaignID != "" {
		tags["campaign_id"] = message.CampaignID
	}

	if err := l.bucket.Take(ctx); err != nil {
		return delivery{}, err
	}
	// The send itself runs on a context detached from the batch's, with its own deadline.
	//
	// A shutdown stops the engine claiming new messages; it does not abandon a request that is
	// already in flight. Abandoning it would leave a message SES may have accepted looking unsent,
	// and the restart would send it again — the one failure D14 exists to prevent. So the request
	// finishes and is written back. If the process is killed outright, the message stays claimed:
	// RecoverClaimed turns that into unknown, which is "we do not know" and never "send it again".
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	providerMessageID, err := l.channel.Send(sendCtx, email, tags)
	if err != nil {
		return delivery{}, &sendError{err: err}
	}
	return delivery{providerMessageID: providerMessageID, sentAt: now}, nil
}

// contentFor picks the body of a message: the campaign, or the newsletter's welcome or confirmation
// email.
func (e *Engine) contentFor(message model.Message, loaded store.MessageContext,
	override *model.Campaign) (model.Campaign, error) {
	if override != nil {
		return *override, nil
	}
	switch message.Kind {
	case model.KindWelcome:
		welcome, ok := rules.WelcomeContent(loaded.Newsletter)
		if !ok {
			return model.Campaign{}, errors.New("the welcome email is off or incomplete")
		}
		return model.Campaign{
			Format: model.FormatMarkdown, Subject: welcome.Subject,
			PreviewText: welcome.PreviewText, Body: welcome.Markdown,
		}, nil
	case model.KindConfirmation:
		confirmation := rules.ConfirmationContent(loaded.Newsletter)
		return model.Campaign{
			Format: model.FormatMarkdown, Subject: confirmation.Subject,
			PreviewText: confirmation.PreviewText, Body: confirmation.Markdown,
		}, nil
	case model.KindLastChance:
		// The last-chance email is the list cleanup's: one question and one button, for a contact who
		// has ignored the last few issues (core/cleanup).
		content := cleanup.LastChanceContent(loaded.Newsletter)
		return model.Campaign{
			Format: model.FormatMarkdown, Subject: content.Subject,
			PreviewText: content.PreviewText, Body: content.Markdown,
		}, nil
	default:
		if loaded.Campaign == nil {
			return model.Campaign{}, fmt.Errorf("message %s has no campaign", message.ID)
		}
		return *loaded.Campaign, nil
	}
}

// recordSent writes back a message SES accepted, and moves the counters that follow from it.
func (e *Engine) recordSent(ctx context.Context, message model.Message, loaded store.MessageContext,
	sent delivery) error {
	return e.writeBack(ctx, func(writeCtx context.Context) error {
		if err := e.store.MarkMessageSent(writeCtx, message.ID, sent.providerMessageID, sent.sentAt); err != nil {
			return err
		}
		if loaded.Campaign != nil {
			if err := e.addSentStats(writeCtx, message, loaded.Campaign.ID); err != nil {
				return err
			}
		}
		// The sentinel test contact is not a real contact, so nothing is recorded for it. This is
		// what keeps a test send out of the campaign report (design §6.2).
		if message.ContactID != "" && message.ContactID != rules.TestContactID {
			if err := e.store.RecordContactSend(writeCtx, message.ContactID, sent.sentAt, message.Kind); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *Engine) addSentStats(ctx context.Context, message model.Message, campaignID string) error {
	if message.Kind == model.KindLastChance {
		return e.store.AddLastChanceStats(ctx, campaignID, store.LastChanceCounters{Sent: 1})
	}
	return e.store.AddCampaignStats(ctx, campaignID, store.CampaignCounters{Sent: 1})
}

func (e *Engine) addFailedStats(ctx context.Context, message model.Message, loaded store.MessageContext) error {
	if loaded.Campaign == nil {
		return nil
	}
	if message.Kind == model.KindLastChance {
		return e.store.AddLastChanceStats(ctx, loaded.Campaign.ID, store.LastChanceCounters{Failed: 1})
	}
	return e.store.AddCampaignStats(ctx, loaded.Campaign.ID, store.CampaignCounters{Failed: 1})
}

// pause stops a campaign, but only one that is still sending: a campaign the operator already
// paused or a send that just finished keeps its status.
func (e *Engine) pause(ctx context.Context, campaignID, reason string) error {
	moved, err := e.store.SetCampaignStatus(ctx, campaignID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignPaused)
	if err != nil {
		return err
	}
	if moved {
		e.log.Warn("campaign paused", "campaign", campaignID, "reason", reason)
		// A pause is the one thing an operator may need to act on within the hour, so its cooldown is a
		// minute rather than the usual one (upstream pauseCampaign).
		e.cfg.Alerts.Send(ctx, alerts.Alert{
			Key: "campaign-paused-" + campaignID, Title: "Campaign paused",
			Detail: reason, Cooldown: time.Minute,
		})
	}
	return nil
}

// pauseIfUnhealthy checks the bounce and complaint limits and pauses the campaign when they are
// exceeded (upstream pauseIfUnhealthy).
func (e *Engine) pauseIfUnhealthy(ctx context.Context, campaignID string) (bool, error) {
	campaign, err := e.store.Campaign(ctx, campaignID)
	if err != nil {
		return false, err
	}
	if campaign.Status != model.CampaignSending {
		return false, nil
	}
	stats, lastChance, err := e.store.CampaignCountersOf(ctx, campaignID)
	if err != nil {
		return false, err
	}
	reason := rules.CampaignPauseReason(stats.PauseStats(lastChance), e.cfg.Limits)
	if reason == "" {
		return false, nil
	}
	return true, e.pause(ctx, campaignID, string(reason)+" is over the limit")
}

// settle finishes a campaign whose messages have all been disposed of (upstream settleCampaign).
func (e *Engine) settle(ctx context.Context, campaignID string) error {
	if paused, err := e.pauseIfUnhealthy(ctx, campaignID); err != nil || paused {
		return err
	}
	// Reading "is anything unfinished" and moving the campaign are one statement in the store: two of
	// them leave a window in which another process releases or claims a message, and the campaign ends
	// up sent with work still pending under it (audit M1, phase 9 #35).
	moved, err := e.store.SettleCampaign(ctx, campaignID)
	if err != nil || !moved {
		return err
	}
	return e.store.SetCampaignSentAt(ctx, campaignID, time.Now().UTC())
}

// Run sends batches until ctx is cancelled. It never starts a batch after cancellation, and a batch
// already in flight finishes its writes, so a graceful shutdown cannot leave a message sent in SES
// but still claimed here without a status (design §3.1).
//
// Each lane polls on its own, and so do the strays: a channel that is throttled or slow keeps only
// its own loop waiting, and another channel's mail goes out as soon as it is queued (multi-channel
// design §6). The strays poll far less often (strayInterval): they are rare, nothing about them is
// urgent, and their claim is a write that scans the pending rows. The first loop to fail stops the
// others, and its error is Run's.
func (e *Engine) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type loop struct {
		part     batchPart
		interval time.Duration
	}
	loops := make([]loop, 0, len(e.order)+1)
	for _, l := range e.order {
		loops = append(loops, loop{func(ctx context.Context) (BatchResult, error) { return e.sendLane(ctx, l) },
			e.cfg.PollInterval})
	}
	loops = append(loops, loop{e.sendStrayBatch, e.strayInterval()})
	errs := make(chan error, len(loops))
	var wg sync.WaitGroup
	for _, each := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.poll(ctx, each.part, each.interval); err != nil {
				errs <- err
				cancel()
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}

// minStrayInterval is the shortest default interval between two stray claims.
const minStrayInterval = 10 * time.Second

// strayInterval is how often Run looks for strays: Config.StrayInterval, or by default forty poll
// intervals and never less than minStrayInterval.
//
// The stray claim matches no index until step 2c's migration adds one on messages.channel, so on a
// deployment whose every message is on a lane it scans the whole pending queue to find nothing, and
// holds the one write connection while it does. Done every PollInterval, that halved the default
// lane's claims at 200,000 pending (#80 review). A stray waits at most this long; a CLI drain
// (SendBatch) still looks for them on every batch.
func (e *Engine) strayInterval() time.Duration {
	if e.cfg.StrayInterval > 0 {
		return e.cfg.StrayInterval
	}
	return max(40*e.cfg.PollInterval, minStrayInterval)
}

// poll runs one part of the batch until ctx is cancelled: on every tick of interval, as many times as
// it finds work.
func (e *Engine) poll(ctx context.Context, part batchPart, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		// A select whose context is done and whose ticker also fired picks either one at random, so
		// the ticker case can land us here after a shutdown has begun. Calling SendBatch would be
		// harmless (it refuses a cancelled context) but pointless, so stop here.
		if ctx.Err() != nil {
			return nil
		}
		// Sending off is a setting, not a failure: SendBatch refuses so that a caller asking to send
		// hears why, but this loop just idles. Returning that refusal would make serve stop the
		// archive, the unsubscribe links and MCP along with it — this process still serves readers,
		// runs the scheduler and answers the admin surface, and any supervisor would restart a
		// non-zero exit forever.
		if !e.cfg.SendingOn {
			continue
		}
		// SendBatch refused a cancelled context on the way in; a part does not, so the loop asks.
		for ctx.Err() == nil {
			result, err := part(ctx)
			if err != nil {
				return err
			}
			if result.Done() == 0 || result.Retry {
				break
			}
		}
	}
}

// Queue is upstream queueCampaign: check the campaign, choose the audience, reserve the day's
// budget, create the messages, and move the campaign to sending.
func (e *Engine) Queue(ctx context.Context, campaignID string, charge Charge) (QueueResult, error) {
	if !e.cfg.SendingOn {
		return QueueResult{}, errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording
	}
	campaign, err := e.store.Campaign(ctx, campaignID)
	if err != nil {
		return QueueResult{}, err
	}
	if !campaign.Status.Queueable() {
		return QueueResult{}, fmt.Errorf("campaign is already %s", campaign.Status)
	}
	newsletter, err := e.store.Newsletter(ctx, campaign.NewsletterID)
	if err != nil {
		return QueueResult{}, err
	}
	if problem := rules.SendProblem(e.signer, e.cfg.AppURL, campaign, &newsletter,
		e.cfg.Limits.MaxEmailBytes); problem != "" {
		return QueueResult{}, errors.New(problem)
	}

	var recipients, lastChance int
	if campaign.Status == model.CampaignPaused {
		// "Draft or scheduled: selects the audience. Paused: resumes with the audience it already
		// has." Upstream skips the walk for a paused campaign, and so must this: a contact who
		// became active while it sat paused does not belong to an issue that is already half sent,
		// and the budget was reserved when the audience was first chosen.
		if recipients, lastChance, err = e.store.CountCampaignMessages(ctx, campaignID); err != nil {
			return QueueResult{}, err
		}
		if recipients+lastChance == 0 {
			return QueueResult{Reason: "No active contacts to send to"}, nil
		}
		// The audience was chosen and the day reserved when this campaign first started, so neither of
		// those is decided again — but the account's own allowance has moved since, and resuming on a
		// spent one would only produce a batch of throttles.
		//
		// What the resume spends is what is still queued, not the whole list: asking for the rest of a
		// campaign that has fifty messages left would refuse a resume that fits.
		// What is still queued, per channel — what this resume spends, and what the account's
		// allowance and the day's budgets are asked about. Its total is the "unfinished" count this
		// used to read separately.
		unfinishedCharge, err := e.store.ChargeOfCampaign(ctx, campaignID)
		if err != nil {
			return QueueResult{}, err
		}
		if refusal := e.sesQuotaRefusal(unfinishedCharge); refusal != "" {
			return QueueResult{Reason: refusal}, nil
		}
		// And if the campaign was reserved on an earlier day, today's ledger has never seen these
		// messages: charge today for what is left, in one transaction with the record that it did, or
		// refuse the step whole. Without this a campaign queued yesterday and resumed today would
		// spend today's allowance while today's ledger stayed empty, and MAX_SENDS_PER_DAY — the whole
		// of a managed deployment's quota split — would be no limit at all.
		day := store.Day(time.Now())
		if campaign.ReservedDay != day {
			// A day later: nothing about these messages has reached today's ledger. Charge today for
			// what is left, in one transaction with the record that it happened, or refuse the step
			// whole. (Same day, the Queue that started this campaign paid, and charging again would
			// double-count.)
			pool, credentialMax, err := e.resolveCharge(ctx, charge)
			if err != nil {
				return QueueResult{}, err
			}
			limits := e.dayLimits(credentialMax)
			fits, err := e.store.ReserveForResume(ctx, day, pool, campaignID, unfinishedCharge, limits)
			if err != nil {
				return QueueResult{}, err
			}
			if !fits {
				return QueueResult{Reason: e.budgetReason(ctx, day, pool, unfinishedCharge, limits)}, nil
			}
		}
	} else {
		// The archive page's address is decided as the issue goes out (upstream assignArchiveSlug), so
		// the page exists before the first email links to it.
		if err := e.assignArchiveSlug(ctx, &campaign, newsletter); err != nil {
			return QueueResult{}, err
		}

		// With the list cleanup on, the audience limit counts issue recipients only and is applied
		// while the audience is walked (core/cleanup.SplitAudience), so the query must return the
		// whole list and let the walk stop where the limit is.
		selectLimit := campaign.AudienceLimit
		if cleanup.SettingsOf(newsletter).Enabled {
			selectLimit = 0
		}
		audience, err := e.store.SelectAudience(ctx, campaign.NewsletterID, selectLimit)
		if err != nil {
			return QueueResult{}, err
		}
		if len(audience) == 0 {
			return QueueResult{Reason: "No active contacts to send to"}, nil
		}
		previous, err := e.store.ReachedContacts(ctx, campaign.ID)
		if err != nil {
			return QueueResult{}, err
		}
		pick, err := e.pickerFor(ctx)
		if err != nil {
			return QueueResult{}, err
		}
		messages, err := e.stepMessages(ctx, campaign, newsletter, audience, previous, pick)
		if err != nil {
			return QueueResult{}, err
		}
		stepCharge := store.ChargeOf(messages)
		// A rule that sends part of this issue to a channel this deployment cannot send through is
		// refused whole, before anything is written or charged, and says which channel.
		if refusal := e.channelRefusal(stepCharge); refusal != "" {
			return QueueResult{Reason: refusal}, nil
		}

		// D15: the whole step is reserved up front, so two campaigns cannot overshoot the day
		// together — and in the same transaction as the first chunk of messages, so a crash cannot
		// spend the day on mail that was never written. A last-chance email is one of those sends: it
		// costs the same quota, so it is reserved the same way.
		day := store.Day(time.Now())
		pool, credentialMax, err := e.resolveCharge(ctx, charge)
		if err != nil {
			return QueueResult{}, err
		}
		if len(messages) == 0 {
			return QueueResult{Reason: "No active contacts to send to"}, nil
		}
		// Checked before the day is charged: a step the account cannot send must not spend the
		// deployment's own budget either, or a refused send would keep the next one from going out.
		if refusal := e.sesQuotaRefusal(stepCharge); refusal != "" {
			return QueueResult{Reason: refusal}, nil
		}
		// The reservation and every message of the step are written together, so there is nothing left
		// to write here and nothing to refund if the process dies: either the step is in and paid for,
		// or it is not there at all (audit M5, phase 9 #38).
		limits := e.dayLimits(credentialMax)
		fits, _, err := e.store.ReserveAndInsert(ctx, day, pool, limits, messages)
		if err != nil {
			return QueueResult{}, err
		}
		if !fits {
			return QueueResult{Reason: e.budgetReason(ctx, day, pool, stepCharge, limits)}, nil
		}
		if recipients, lastChance, err = e.store.CountCampaignMessages(ctx, campaignID); err != nil {
			return QueueResult{}, err
		}
		if recipients+lastChance == 0 {
			return QueueResult{Reason: "No active contacts to send to"}, nil
		}
	}

	moved, err := e.store.SetCampaignStatus(ctx, campaignID,
		[]model.CampaignStatus{campaign.Status}, model.CampaignSending)
	if err != nil {
		return QueueResult{}, err
	}
	if !moved {
		return QueueResult{}, errors.New("the campaign changed, refresh and try again")
	}
	// Upstream sets the recipient counts rather than incrementing them: a paused campaign that
	// resumes re-selects the audience, and an increment would count it twice.
	if err := e.store.SetCampaignRecipients(ctx, campaignID, recipients, lastChance); err != nil {
		return QueueResult{}, err
	}
	if err := e.store.SetCampaignStartedAt(ctx, campaignID, time.Now().UTC()); err != nil {
		return QueueResult{}, err
	}
	return QueueResult{Recipients: recipients, LastChance: lastChance, Queued: true}, nil
}

// assignArchiveSlug gives an issue the address its archive page will live at, unless it already has
// one. Upstream does this when the issue goes out (lib/archive-slugs.js): the address comes from the
// subject as it reads now, and the page therefore exists before the first email points at it.
//
// A newsletter with no archive is left alone — a slug it will never serve is just a field that looks
// meaningful. The slugify fallback is "issue", so a campaign whose subject is punctuation still has a
// page rather than an empty address.
func (e *Engine) assignArchiveSlug(ctx context.Context, campaign *model.Campaign,
	newsletter model.Newsletter) error {
	if !newsletter.ArchiveEnabled || campaign.ArchiveSlug != "" {
		return nil
	}
	slug := slug.Slug(campaign.Subject, 80)
	if slug == "" {
		slug = "issue"
	}
	unique, err := e.store.UniqueArchiveSlug(ctx, slug)
	if err != nil {
		return err
	}
	if err := e.store.SetCampaignArchiveSlug(ctx, campaign.ID, unique); err != nil {
		return err
	}
	campaign.ArchiveSlug = unique
	return nil
}

// stepMessages is one step's messages: the issue for most of the audience, the last-chance email for
// the contacts the list cleanup calls cold, and nothing for the ones who are waiting on that email or
// have already gone dormant.
//
// previous is what this campaign already sent each contact, which is what stops a resume or a later
// step from writing the same message twice. The contacts the split puts to sleep are marked dormant
// here, and counted so the campaign's report says how many left the list quietly.
func (e *Engine) stepMessages(ctx context.Context, campaign model.Campaign, newsletter model.Newsletter,
	audience []model.Contact, previous map[string]model.MessageKind, pick picker) ([]model.Message, error) {
	settings := cleanup.SettingsOf(newsletter)
	if !settings.Enabled {
		return toMessages(campaign, audience, pick), nil
	}
	split := cleanup.SplitAudience(audience, cleanup.SplitInput{
		Settings: settings, Limit: campaign.AudienceLimit, Previous: previous, Now: time.Now().UTC(),
	})
	if len(split.Dormant) > 0 {
		dormant := make([]string, 0, len(split.Dormant))
		for _, contact := range split.Dormant {
			dormant = append(dormant, contact.ID)
		}
		// A sign of life since the list was read leaves the contact alone, so the number that moved is
		// the one that counts — upstream reads modifiedCount for the same reason.
		moved, err := e.store.MakeDormant(ctx, dormant)
		if err != nil {
			return nil, err
		}
		if moved > 0 {
			if err := e.store.AddLastChanceStats(ctx, campaign.ID,
				store.LastChanceCounters{Dormant: moved}); err != nil {
				return nil, err
			}
		}
	}
	messages := toMessages(campaign, split.Issue, pick)
	for _, contact := range split.LastChance {
		messages = append(messages, model.Message{
			ID: ids.New(), Kind: model.KindLastChance,
			CampaignID: campaign.ID, NewsletterID: campaign.NewsletterID,
			ContactID: contact.ID, Email: contact.Email,
			Provider: providers.Mailbox(contact.Email),
			Channel:  pick.pick(contact.ID, contact.Email),
			Status:   model.MessagePending, CreatedAt: time.Now().UTC(),
		})
	}
	return messages, nil
}

// QueueStep is upstream sendToMoreContacts: a sent campaign goes to the next most engaged slice. It
// takes the top of the audience again and the one-message-per-contact index skips whoever already
// received it.
func (e *Engine) QueueStep(ctx context.Context, campaignID string, additional int, charge Charge) (QueueResult, error) {
	if !e.cfg.SendingOn {
		return QueueResult{}, errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording
	}
	campaign, err := e.store.Campaign(ctx, campaignID)
	if err != nil {
		return QueueResult{}, err
	}
	if campaign.Status != model.CampaignSent {
		return QueueResult{}, errors.New("wait until the current step has gone out")
	}
	newsletter, err := e.store.Newsletter(ctx, campaign.NewsletterID)
	if err != nil {
		return QueueResult{}, err
	}
	if problem := rules.SendProblem(e.signer, e.cfg.AppURL, campaign, &newsletter,
		e.cfg.Limits.MaxEmailBytes); problem != "" {
		return QueueResult{}, errors.New(problem)
	}

	sentSoFar, _, err := e.store.CountCampaignMessages(ctx, campaignID)
	if err != nil {
		return QueueResult{}, err
	}
	limit := rules.NextStepLimit(sentSoFar, additional)
	audience, err := e.store.SelectAudience(ctx, campaign.NewsletterID, limit)
	if err != nil {
		return QueueResult{}, err
	}
	reached, err := e.store.ReachedContacts(ctx, campaignID)
	if err != nil {
		return QueueResult{}, err
	}
	fresh := make([]model.Contact, 0, len(audience))
	for _, contact := range audience {
		if _, already := reached[contact.ID]; !already {
			fresh = append(fresh, contact)
		}
	}
	if len(fresh) == 0 {
		return QueueResult{Reason: "Everyone has received it already"}, nil
	}
	day := store.Day(time.Now())
	pool, credentialMax, err := e.resolveCharge(ctx, charge)
	if err != nil {
		return QueueResult{}, err
	}
	// The fresh ones are the ones this step has not reached, so there is no "already got it" to
	// consult: the split judges each of them on their own.
	pick, err := e.pickerFor(ctx)
	if err != nil {
		return QueueResult{}, err
	}
	messages, err := e.stepMessages(ctx, campaign, newsletter, fresh, nil, pick)
	if err != nil {
		return QueueResult{}, err
	}
	stepCharge := store.ChargeOf(messages)
	if refusal := e.channelRefusal(stepCharge); refusal != "" {
		return QueueResult{Reason: refusal}, nil
	}
	if refusal := e.sesQuotaRefusal(stepCharge); refusal != "" {
		return QueueResult{Reason: refusal}, nil
	}
	// As above: the whole step and its reservation are one write.
	limits := e.dayLimits(credentialMax)
	fits, _, err := e.store.ReserveAndInsert(ctx, day, pool, limits, messages)
	if err != nil {
		return QueueResult{}, err
	}
	if !fits {
		return QueueResult{Reason: e.budgetReason(ctx, day, pool, stepCharge, limits)}, nil
	}
	recipients, lastChance, err := e.store.CountCampaignMessages(ctx, campaignID)
	if err != nil {
		return QueueResult{}, err
	}

	moved, err := e.store.SetCampaignStatus(ctx, campaignID,
		[]model.CampaignStatus{model.CampaignSent}, model.CampaignSending)
	if err != nil {
		return QueueResult{}, err
	}
	if !moved {
		return QueueResult{}, errors.New("the campaign changed, refresh and try again")
	}
	// Also a set, for the same reason as Queue: recipients is the campaign's total, not this step's.
	if err := e.store.SetCampaignRecipients(ctx, campaignID, recipients, lastChance); err != nil {
		return QueueResult{}, err
	}
	if limit > 0 {
		if err := e.store.SetCampaignAudienceLimit(ctx, campaignID, limit); err != nil {
			return QueueResult{}, err
		}
	}
	return QueueResult{
		Recipients: recipients, LastChance: lastChance,
		Added: recipients - sentSoFar, Queued: true,
	}, nil
}

// Test is upstream sendTestEmail: one message to one address, under the sentinel contact id so it
// stays out of every report.
func (e *Engine) Test(ctx context.Context, campaignID, to string) (TestResult, error) {
	if !e.cfg.SendingOn {
		return TestResult{}, errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording
	}
	campaign, err := e.store.Campaign(ctx, campaignID)
	if err != nil {
		return TestResult{}, err
	}
	newsletter, err := e.store.Newsletter(ctx, campaign.NewsletterID)
	if err != nil {
		return TestResult{}, err
	}
	email := validate.NormalizeEmail(to)
	if email == "" {
		return TestResult{}, errors.New("the test email address is invalid")
	}
	if problem := rules.SendProblem(e.signer, e.cfg.AppURL, campaign, &newsletter,
		e.cfg.Limits.MaxEmailBytes); problem != "" {
		return TestResult{}, errors.New(problem)
	}

	// A test has no contact, so the rules are matched on the address being tested — the same stand-in
	// they use for a confirmation (#76).
	pick, err := e.pickerFor(ctx)
	if err != nil {
		return TestResult{}, err
	}
	message := model.Message{
		ID: ids.New(), Kind: model.KindTest, CampaignID: campaignID,
		NewsletterID: newsletter.ID, ContactID: rules.TestContactID, Email: email,
		Provider: providers.Mailbox(email),
		Channel:  e.oneOffChannel(pick, rules.TestContactID, email),
		Status:   model.MessagePending, CreatedAt: time.Now().UTC(),
	}
	if _, err := e.store.InsertMessages(ctx, []model.Message{message}); err != nil {
		return TestResult{}, err
	}
	claimed, err := e.store.ClaimMessagesByID(ctx, []string{message.ID})
	if err != nil {
		return TestResult{}, fmt.Errorf("claim test message: %w", err)
	}
	if len(claimed) != 1 {
		// Not a store error: the claim found nothing. The insert above is INSERT OR IGNORE, so a test
		// send to the same campaign and address a second time writes no row and has nothing to claim —
		// and the old single check wrapped the nil error, which printed as %!w(<nil>) and told nobody
		// anything (phase 9 #51).
		return TestResult{}, fmt.Errorf(
			"the test message for %s was not written: this campaign has already sent a test to that "+
				"address (one message per contact per campaign)", message.Email)
	}
	// The sentinel contact has no row, so the lookup goes without it and the stand-in is set below.
	loaded, err := e.store.LoadMessageContext(ctx, model.Message{
		CampaignID: message.CampaignID, NewsletterID: message.NewsletterID,
	})
	if err != nil {
		return TestResult{}, err
	}
	loaded.Contact = model.Contact{ID: rules.TestContactID, Email: email, Status: model.ContactActive}

	subject := campaign
	subject.Subject = "[Test] " + campaign.Subject
	// A test goes out through the channel its message is on: the default one until step 2c routes.
	l, ok := e.laneFor(message.Channel)
	if !ok {
		err := fmt.Errorf("no channel named %s is loaded", message.Channel)
		// The message is already claimed; left so, the next start would recover it as unknown.
		if markErr := e.store.MarkMessageFailed(ctx, message.ID, err.Error()); markErr != nil {
			e.log.Error("mark failed", "message", message.ID, "error", markErr)
		}
		return TestResult{}, err
	}
	sent, err := e.deliver(ctx, l, message, loaded, &subject)
	if err != nil {
		if markErr := e.store.MarkMessageFailed(ctx, message.ID, err.Error()); markErr != nil {
			e.log.Error("mark failed", "message", message.ID, "error", markErr)
		}
		return TestResult{}, err
	}
	if err := e.store.MarkMessageSent(ctx, message.ID, sent.providerMessageID, sent.sentAt); err != nil {
		return TestResult{}, err
	}
	if err := e.store.SetCampaignTestSentAt(ctx, campaignID, sent.sentAt); err != nil {
		return TestResult{}, err
	}
	return TestResult{MessageID: message.ID, ProviderMessageID: sent.providerMessageID, SentAt: sent.sentAt}, nil
}

// sendTimeout bounds one call to SES. The request runs detached from the batch's context (see
// deliver), so it needs a deadline of its own or a hung connection would hold the shutdown open.
const sendTimeout = 30 * time.Second

// resolveCharge turns a charge into the pool a step reserves against and the ceiling that sits under
// the global one: the credential's own daily_budget, or 0 when the charge is the system pool.
//
// A credential that is gone or revoked is an error rather than a silent fallback to the global pool.
// This is the path a scheduled send takes hours after an agent queued it, and "the token that asked
// for this was revoked" is exactly the case the operator revoked it for.
func (e *Engine) resolveCharge(ctx context.Context, charge Charge) (pool string, credentialMax int, err error) {
	if charge.system() {
		return "", 0, nil
	}
	credential, err := e.store.Credential(ctx, charge.CredentialID)
	if err != nil {
		return "", 0, fmt.Errorf("this send was queued by a credential that no longer exists: %w", err)
	}
	if credential.Revoked() {
		return "", 0, fmt.Errorf("this send was queued by credential %q, which has since been revoked",
			credential.Name)
	}
	return credential.ID, credential.DailyBudget, nil
}

// dayLimits are what a reservation is checked against: the deployment's own day total, the
// credential's, and each channel's own max_per_day (0 for a channel that has none, the default
// channel included — its account's 24-hour quota is the ceiling there, and it is checked as a quota).
func (e *Engine) dayLimits(credentialMax int) store.Limits {
	channels := map[string]int{}
	for name, limit := range e.cfg.ChannelLimits {
		if limit.MaxPerDay > 0 {
			channels[name] = limit.MaxPerDay
		}
	}
	return store.Limits{Global: e.cfg.Limits.MaxSendsPerDay, Credential: credentialMax, Channels: channels}
}

// picker decides which channel a message goes out through, from the routing rules as they stand.
type picker struct {
	rules []route.Rule
}

// pickerFor reads the rules once for a whole step: a campaign of a hundred thousand messages must not
// ask the database a hundred thousand times for something that cannot change underneath it.
func (e *Engine) pickerFor(ctx context.Context) (picker, error) {
	rules, err := e.store.RoutesAsRules(ctx)
	if err != nil {
		return picker{}, err
	}
	return picker{rules: rules}, nil
}

// pick is the channel a message to this address goes through: the first rule that matches, or the
// default channel when none does — a deployment with no rules sends exactly as it did before there
// were any.
func (p picker) pick(contactID, email string) string {
	return route.Pick(p.rules, contactID, email)
}

// cannotSendThrough is why this process cannot send through a channel, or "" when it can. A channel
// that is disabled, or that could not be built, has no lane: nothing would ever claim its mail, so a
// message recorded on it would sit there.
func (e *Engine) cannotSendThrough(channel string) string {
	if _, ok := e.laneFor(channel); ok {
		return ""
	}
	if problem := e.cfg.ChannelProblems[channel]; problem != nil {
		return fmt.Sprintf("channel %s could not be loaded (%v)", channel, problem)
	}
	return fmt.Sprintf("channel %s is not loaded in this process", channel)
}

// channelRefusal is why a step cannot be queued as it stands: the rules sent part of it to a channel
// this deployment cannot send through. The step is refused whole rather than quietly sent another
// way — which channel mail leaves through is the operator's decision, and 2d's fallback chain is what
// reassigns it once a channel is known to be down.
func (e *Engine) channelRefusal(charge store.Charge) string {
	for _, channel := range charge.Channels() {
		if reason := e.cannotSendThrough(channel); reason != "" {
			return reason + ": fix it, or change the rule that sends there, then queue again"
		}
	}
	return ""
}

// oneOffChannel is the channel for a message that is not part of a campaign: a confirmation, a
// welcome, a test. It follows the rules too — there is no second set of rules for single messages —
// except that it falls back to the default channel when the rule sends it to a channel this process
// cannot use: a subscriber waiting for a confirmation that will never come is worse than one sent the
// old way, and a one-off has no campaign to pause and no operator watching it (#76).
func (e *Engine) oneOffChannel(p picker, contactID, email string) string {
	channel := p.pick(contactID, email)
	if reason := e.cannotSendThrough(channel); reason != "" {
		e.log.Warn("a one-off message falls back to the default channel",
			"channel", channel, "to", email, "reason", reason)
		return model.DefaultChannel
	}
	return channel
}

// budgetReason says which ceiling a step ran into, and how much room is left under it.
//
// Both are checked before anything is written, and the global one first, so on the refusal path the
// engine asks which of them the day has actually reached. "MAX_SENDS_PER_DAY" and "this credential's
// daily_budget" are different answers to whoever reads the job's last_error, and guessing wrong sends
// them looking at the wrong number — but the number they need to act on is the room that is left, not
// the ceiling's name. That is what tells them to wait for tomorrow or to send a smaller step.
func (e *Engine) budgetReason(ctx context.Context, day, pool string, charge store.Charge,
	limits store.Limits) string {
	// A channel's own ceiling is looked at first: it is the one an operator can do something about,
	// by raising that channel's --max-per-day or by moving the rule off it.
	if byChannel, err := e.store.ReservedByChannel(ctx, day, pool); err == nil {
		for _, channel := range charge.Channels() {
			channelMax := limits.Channels[channel]
			if channelMax <= 0 || byChannel[channel]+charge[channel] <= channelMax {
				continue
			}
			return fmt.Sprintf("channel %s sends at most %d a day and %d of it is reserved, so there "+
				"is room for %d more and this step needs %d",
				channel, channelMax, byChannel[channel],
				max(channelMax-byChannel[channel], 0), charge[channel])
		}
	}
	messages := charge.Total()
	if limits.Global > 0 {
		if reserved, err := e.store.Reserved(ctx, day, ""); err == nil && reserved+messages > limits.Global {
			return fmt.Sprintf("the daily send budget is spent: %d of MAX_SENDS_PER_DAY are already "+
				"reserved, so there is room for %d more today and this step is %d",
				reserved, max(limits.Global-reserved, 0), messages)
		}
	}
	if limits.Credential > 0 {
		spent, err := e.store.Reserved(ctx, day, pool)
		if err == nil {
			return fmt.Sprintf("this credential's daily_budget is %d and %d of it is already reserved "+
				"today, so there is room for %d more and this step is %d",
				limits.Credential, spent, max(limits.Credential-spent, 0), messages)
		}
		return fmt.Sprintf("this credential's daily_budget of %d is smaller than this step of %d",
			limits.Credential, messages)
	}
	return fmt.Sprintf("the daily send budget is spent: %d messages would go over MAX_SENDS_PER_DAY", messages)
}

// startEarly is lib/scheduling.js START_EARLY_MS: within a minute of the picked time, the scheduled
// send starts instead of waiting again.
const startEarly = time.Minute

// RunJob is the scheduler's handler: it turns one due job into work. An error sends the job back to
// the queue with its backoff.
func (e *Engine) RunJob(ctx context.Context, job jobs.Job) error {
	switch job.Kind {
	case jobs.KindSendCampaign:
		var payload struct {
			CampaignID string `json:"campaignId"`
			// Additional turns the job into the next step of a send in steps.
			Additional int `json:"additional"`
			// ScheduledAt is set only by `campaign schedule`: the time the operator picked. The job
			// does nothing when the campaign is no longer scheduled for it (lib/scheduling.js
			// runScheduledSend), which is how unscheduling, rescheduling or an earlier step wins.
			ScheduledAt string `json:"scheduledAt"`
			// CredentialID is set by the MCP surface (design §11, layer 2): the step spends that
			// credential's daily_budget as well as the day's global one. Empty is the CLI's own send,
			// which has only MAX_SENDS_PER_DAY above it.
			CredentialID string `json:"credentialId"`
		}
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("send_campaign payload: %w", err)
		}
		if payload.CampaignID == "" {
			return errors.New("send_campaign job has no campaignId")
		}
		charge := Charge{CredentialID: payload.CredentialID}
		if payload.Additional > 0 {
			result, err := e.QueueStep(ctx, payload.CampaignID, payload.Additional, charge)
			return e.sendJobFailed(ctx, job, payload.CampaignID, nothingToSend(result, err))
		}
		if skip, err := e.scheduledSendIsStale(ctx, payload.CampaignID, payload.ScheduledAt); err != nil || skip {
			return e.sendJobFailed(ctx, job, payload.CampaignID, err)
		}
		result, err := e.Queue(ctx, payload.CampaignID, charge)
		return e.sendJobFailed(ctx, job, payload.CampaignID, nothingToSend(result, err))
	case jobs.KindSendWelcome:
		var payload struct {
			NewsletterID string `json:"newsletterId"`
			ContactID    string `json:"contactId"`
		}
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("send_welcome payload: %w", err)
		}
		newsletter, err := e.store.Newsletter(ctx, payload.NewsletterID)
		if err != nil {
			return err
		}
		contact, err := e.store.Contact(ctx, payload.ContactID)
		if err != nil {
			return err
		}
		_, err = e.QueueWelcome(ctx, newsletter, contact)
		return err
	default:
		return fmt.Errorf("unknown job kind %q", job.Kind)
	}
}

// sendJobFailed alerts when a send job has run out of attempts: nothing will retry it, and a campaign
// that never went out is exactly what an operator has to hear about (upstream alerts when a scheduled
// send fails to start, and this is the same moment reached from the other end).
func (e *Engine) sendJobFailed(ctx context.Context, job jobs.Job, campaignID string, err error) error {
	if err == nil {
		return nil
	}
	// Attempts is how many tries this job has already had; the scheduler counts this one after it
	// records the failure, so the last try is the one where job.Attempts is already at the limit.
	if job.Attempts+1 >= jobs.MaxAttempts() {
		e.cfg.Alerts.Send(ctx, alerts.Alert{
			Key: "send-job-" + campaignID, Title: "A send stopped",
			Detail: fmt.Sprintf("%s on campaign %s gave up after %d attempts. %s",
				job.Kind, campaignID, job.Attempts+1, err),
			Cooldown: time.Minute,
		})
	}
	return err
}

// nothingToSend turns a refusal that is not an error into one, so the job fails with the reason
// rather than completing having sent nothing.
//
// A job that finishes without sending looks exactly like a send that worked — the campaign's own
// status is the only other evidence — and "the daily send budget is spent" is the answer whoever
// queued it needs. The job retries on its backoff and then goes dead with the reason in its
// last_error, which is where `jobs list` and the MCP get_job tool read it.
func nothingToSend(result QueueResult, err error) error {
	if err != nil || result.Queued || result.Reason == "" {
		return err
	}
	return errors.New(result.Reason)
}

// scheduledSendIsStale reports whether a scheduled send's wake-up has been overtaken by events: the
// campaign was unscheduled, rescheduled, already started or discarded since the job was queued. A
// job with no scheduledAt is a direct send (the operator or `approve`), and is never stale here.
func (e *Engine) scheduledSendIsStale(ctx context.Context, campaignID, scheduledAt string) (bool, error) {
	if scheduledAt == "" {
		return false, nil
	}
	wanted, err := time.Parse(time.RFC3339, scheduledAt)
	if err != nil {
		// A malformed payload is the job's problem, not the campaign's: report it so the scheduler
		// retries and eventually dead-letters it, rather than silently dropping a send.
		return true, fmt.Errorf("send_campaign job has a bad scheduledAt %q: %w", scheduledAt, err)
	}
	campaign, err := e.store.Campaign(ctx, campaignID)
	if err != nil {
		return true, err
	}
	if campaign.Status != model.CampaignScheduled || campaign.ScheduledAt == nil ||
		!campaign.ScheduledAt.Equal(wanted) {
		e.log.Info("scheduled send skipped", "campaign", campaignID, "status", campaign.Status)
		return true, nil
	}
	// The scheduler only leases a job once its run_at has passed, so this is belt and braces for a
	// clock that moved or a job requeued early.
	return campaign.ScheduledAt.After(time.Now().UTC().Add(startEarly)), nil
}

// SignupResult is what queueing a welcome or confirmation email did; Skipped says why it didn't go.
type SignupResult struct {
	MessageID string
	Skipped   string
}

// QueueWelcome is upstream queueWelcomeEmail: one welcome email per contact, only to an active one,
// and only while the newsletter's welcome email is switched on and complete.
func (e *Engine) QueueWelcome(ctx context.Context, newsletter model.Newsletter, contact model.Contact) (SignupResult, error) {
	if !e.cfg.SendingOn {
		return SignupResult{}, errSendingOff
	}
	content, ok := rules.WelcomeContent(newsletter)
	if !ok {
		return SignupResult{Skipped: "welcome email is off"}, nil
	}
	if contact.Status != model.ContactActive {
		return SignupResult{Skipped: "contact is not active"}, nil
	}
	if problem := rules.SendProblem(e.signer, e.cfg.AppURL, model.Campaign{
		Format: model.FormatMarkdown, Subject: content.Subject,
		PreviewText: content.PreviewText, Body: content.Markdown,
	}, &newsletter, e.cfg.Limits.MaxEmailBytes); problem != "" {
		e.log.Warn("welcome email not sent", "newsletter", newsletter.Slug, "problem", problem)
		return SignupResult{Skipped: problem}, nil
	}
	return e.queueSignup(ctx, newsletter, contact, model.KindWelcome)
}

// QueueConfirmation is upstream queueConfirmationEmail: at most rules.MaxConfirmationsPerContact per
// contact, never twice inside the welcome window, and only to a contact waiting to confirm.
func (e *Engine) QueueConfirmation(ctx context.Context, newsletter model.Newsletter, contact model.Contact) (SignupResult, error) {
	if !e.cfg.SendingOn {
		return SignupResult{}, errSendingOff
	}
	if contact.Status != model.ContactPending {
		return SignupResult{Skipped: "contact is not pending"}, nil
	}
	content := rules.ConfirmationContent(newsletter)
	if problem := rules.SendProblem(e.signer, e.cfg.AppURL, model.Campaign{
		Format: model.FormatMarkdown, Subject: content.Subject,
		PreviewText: content.PreviewText, Body: content.Markdown,
	}, &newsletter, e.cfg.Limits.MaxEmailBytes); problem != "" {
		e.log.Warn("confirmation email not sent", "newsletter", newsletter.Slug, "problem", problem)
		return SignupResult{Skipped: problem}, nil
	}
	return e.queueSignup(ctx, newsletter, contact, model.KindConfirmation)
}

// queueSignup is the shared tail of the two: the caps, then one message and one batch.
func (e *Engine) queueSignup(ctx context.Context, newsletter model.Newsletter, contact model.Contact,
	kind model.MessageKind) (SignupResult, error) {
	if _, suppressed, err := e.store.Suppression(ctx, contact.Email); err != nil {
		return SignupResult{}, err
	} else if suppressed {
		return SignupResult{Skipped: "suppressed"}, nil
	}

	window := time.Now().UTC().Add(-rules.WelcomeWindow)
	if kind == model.KindWelcome {
		sent, err := e.store.HasMessageOfKind(ctx, contact.ID, model.KindWelcome)
		if err != nil {
			return SignupResult{}, err
		}
		if sent {
			return SignupResult{Skipped: "already sent"}, nil
		}
	} else {
		count, err := e.store.CountContactMessages(ctx, contact.ID, model.KindConfirmation)
		if err != nil {
			return SignupResult{}, err
		}
		recent, err := e.store.HasMessageOfKindSince(ctx, contact.ID, model.KindConfirmation, window)
		if err != nil {
			return SignupResult{}, err
		}
		if recent || count >= rules.MaxConfirmationsPerContact {
			return SignupResult{Skipped: "already sent"}, nil
		}
	}

	// Welcome and confirmation emails share one cap per newsletter, so a signup flood cannot burn
	// the sending domain's reputation, and one newsletter's flood doesn't hold up another's. The cap
	// is applied by the insert itself: two signups arriving together must not both read a count below
	// it and both write (the phase 1-3 audit's ST-I6).
	// A confirmation or a welcome follows the same rules the campaign mail does; the provider was
	// missing here until 2c-2, which `campaign report` grouped as "other" for every signup.
	pick, err := e.pickerFor(ctx)
	if err != nil {
		return SignupResult{}, err
	}
	message := model.Message{
		ID: ids.New(), Kind: kind, NewsletterID: newsletter.ID, ContactID: contact.ID,
		Email: contact.Email, Provider: providers.Mailbox(contact.Email),
		Channel: e.oneOffChannel(pick, contact.ID, contact.Email),
		Status:  model.MessagePending, CreatedAt: time.Now().UTC(),
	}
	inserted, err := e.store.InsertSignupMessage(ctx, message, newsletter.ID,
		[]model.MessageKind{model.KindWelcome, model.KindConfirmation}, window,
		e.cfg.Limits.WelcomeEmailsPer10Minute)
	if err != nil {
		return SignupResult{}, err
	}
	if !inserted {
		e.log.Warn("signup emails capped", "newsletter", newsletter.Slug,
			"limit", e.cfg.Limits.WelcomeEmailsPer10Minute)
		e.cfg.Alerts.Send(ctx, alerts.Alert{
			Key: "welcome-cap:" + newsletter.Slug, Title: "Signup emails capped",
			Detail: fmt.Sprintf("Over %d welcome and confirmation emails for %s in 10 minutes. "+
				"New signups get none until it slows down.",
				e.cfg.Limits.WelcomeEmailsPer10Minute, newsletter.Name),
		})
		return SignupResult{Skipped: "rate limited"}, nil
	}
	if _, err := e.SendBatch(ctx, []string{message.ID}); err != nil {
		return SignupResult{}, err
	}
	return SignupResult{MessageID: message.ID}, nil
}

// errSendingOff is upstream's wording, which rules.SendErrorAction also recognises.
var errSendingOff = errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording

// toMessages turns an audience into pending campaign messages. Each one's channel is decided here,
// once, by the rules as they stand: `provider_message_id` freezes it afterwards, so a message that
// has been sent cannot be moved to another channel (design §4.2).
func toMessages(campaign model.Campaign, contacts []model.Contact, pick picker) []model.Message {
	now := time.Now().UTC()
	messages := make([]model.Message, 0, len(contacts))
	for _, contact := range contacts {
		messages = append(messages, model.Message{
			ID:           ids.New(),
			Kind:         model.KindCampaign,
			CampaignID:   campaign.ID,
			NewsletterID: campaign.NewsletterID,
			ContactID:    contact.ID,
			Email:        contact.Email,
			// The provider is recorded now, so `campaign report` can group without re-deriving it
			// from the address for every row (lib/sending.js toMessages).
			Provider:  providers.Mailbox(contact.Email),
			Channel:   pick.pick(contact.ID, contact.Email),
			Status:    model.MessagePending,
			CreatedAt: now,
		})
	}
	return messages
}

// errorCode is the SES error code, or "" for an error that never reached SES.
func errorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}
