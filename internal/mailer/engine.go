package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/smithy-go"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/providers"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/core/sign"
	"mailcheap/internal/core/validate"
	"mailcheap/internal/jobs"
	"mailcheap/internal/store"
)

// releaseTimeout bounds the bookkeeping a stopped batch still owes the database.
const releaseTimeout = 10 * time.Second

// Config is what the engine needs from internal/config.
type Config struct {
	AppURL         string
	SendingOn      bool
	SendRatePerSec int
	Limits         model.Limits
	// PollInterval is how often Run looks for work.
	PollInterval time.Duration
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
type TestResult struct {
	MessageID    string
	SESMessageID string
	SentAt       time.Time
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

// Engine sends queued messages through a Sender.
type Engine struct {
	store  *store.DB
	sender Sender
	signer *sign.Signer
	bucket *Bucket
	cfg    Config
	log    *slog.Logger
}

// NewEngine builds an engine. The bucket is shared by every caller, so the rate is a process-wide
// cap rather than a per-batch one.
func NewEngine(db *store.DB, sender Sender, signer *sign.Signer, cfg Config, log *slog.Logger) *Engine {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		store: db, sender: sender, signer: signer,
		bucket: NewBucket(cfg.SendRatePerSec), cfg: cfg, log: log,
	}
}

// Close stops the rate limiter.
func (e *Engine) Close() { e.bucket.Close() }

// SendBatch sends one batch. With no ids it claims up to rules.SendBatchSize messages itself, which
// is what Run uses; with ids it sends exactly those (the CLI's path).
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

	var messages []model.Message
	if len(messageIDs) == 0 {
		claimed, err := e.store.ClaimMessages(ctx, rules.SendBatchSize)
		if err != nil {
			return result, err
		}
		messages = claimed
	} else {
		claimed, err := e.store.ClaimMessagesByID(ctx, messageIDs)
		if err != nil {
			return result, err
		}
		messages = claimed
		result.Skipped = len(messageIDs) - len(claimed)
	}
	if len(messages) == 0 {
		return result, nil
	}

	suppressed, err := e.store.SuppressedEmails(ctx)
	if err != nil {
		return result, err
	}
	suppressedSet := make(map[string]bool, len(suppressed))
	for _, email := range suppressed {
		suppressedSet[email] = true
	}

	// A campaign that is already over its bounce or complaint limit pauses before anything else goes
	// out, as upstream does at the top of sendMessageBatch.
	campaigns := map[string]bool{}
	for _, message := range messages {
		if message.CampaignID == "" {
			continue
		}
		if _, seen := campaigns[message.CampaignID]; seen {
			continue
		}
		campaigns[message.CampaignID] = true
		if _, err := e.pauseIfUnhealthy(ctx, message.CampaignID); err != nil {
			return result, err
		}
	}

	consecutiveFailures := 0
	for i, message := range messages {
		// Whatever stops the batch, the messages it did not reach go back to the queue: left
		// claimed they would be recovered as unknown on the next start, which means silently
		// dropping a send (D14).
		if ctx.Err() != nil {
			result.Retry = true
			e.releaseAll(ctx, messages[i:])
			break
		}
		loaded, err := e.store.LoadMessageContext(ctx, message)
		if err != nil {
			e.releaseAll(ctx, messages[i:])
			return result, fmt.Errorf("load message %s: %w", message.ID, err)
		}
		// The campaign may have been paused or finished since the audience was chosen.
		if rules.IsCampaignKind(message.Kind) &&
			(loaded.Campaign == nil || loaded.Campaign.Status != model.CampaignSending) {
			result.Skipped++
			if err := e.store.ReleaseMessage(ctx, message.ID); err != nil {
				return result, err
			}
			continue
		}

		required := model.ContactActive
		if message.Kind == model.KindConfirmation {
			required = model.ContactPending
		}
		if loaded.Contact.Status != required || suppressedSet[message.Email] {
			if err := e.store.MarkMessageSuppressed(ctx, message.ID); err != nil {
				return result, err
			}
			result.Suppressed++
			continue
		}

		delivery, err := e.deliver(ctx, message, loaded, nil)
		if err != nil {
			// A cancelled context means we are stopping, not that the message is bad: it never
			// reached SES, so it goes back to the queue instead of being marked failed.
			//
			// Releasing the current message is safe only because deliver cannot report a
			// cancellation once it has called SES: the call runs on a detached context (see
			// deliver), so the only cancellation it can surface is the send slot's, from before the
			// request was made.
			if ctx.Err() != nil {
				e.releaseAll(ctx, messages[i:])
				result.Retry = true
				return result, nil
			}
			outcome, stop := e.handleSendError(ctx, message, loaded, err)
			result.Failed += outcome.failed
			result.Retry = result.Retry || outcome.retry
			if stop {
				e.releaseAll(ctx, messages[i+1:])
				break
			}
			consecutiveFailures += outcome.failed
			if outcome.failed > 0 && message.CampaignID != "" &&
				consecutiveFailures >= rules.MaxConsecutiveFailures {
				if err := e.pause(ctx, message.CampaignID,
					fmt.Sprintf("%d sends failed in a row", consecutiveFailures)); err != nil {
					return result, err
				}
				e.releaseAll(ctx, messages[i+1:])
				break
			}
			continue
		}
		if err := e.recordSent(ctx, message, loaded, delivery); err != nil {
			return result, err
		}
		result.Sent++
		consecutiveFailures = 0
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

// handleSendError classifies a failed send the way upstream does, from both the error code and the
// message (rules.SendErrorAction needs both).
func (e *Engine) handleSendError(ctx context.Context, message model.Message, loaded store.MessageContext,
	cause error) (outcome errorOutcome, stop bool) {
	action := rules.ErrorFail
	if message.CampaignID != "" {
		action = rules.SendErrorAction(errorCode(cause), cause.Error())
	}
	switch action {
	case rules.ErrorFail:
		if err := e.store.MarkMessageFailed(ctx, message.ID, cause.Error()); err != nil {
			e.log.Error("mark failed", "message", message.ID, "error", err)
		}
		if err := e.addFailedStats(ctx, message, loaded); err != nil {
			e.log.Error("failed stats", "message", message.ID, "error", err)
		}
		return errorOutcome{failed: 1}, false
	case rules.ErrorPause:
		if err := e.store.ReleaseMessage(ctx, message.ID); err != nil {
			e.log.Error("release", "message", message.ID, "error", err)
		}
		if err := e.pause(ctx, message.CampaignID, cause.Error()); err != nil {
			e.log.Error("pause", "campaign", message.CampaignID, "error", err)
		}
		return errorOutcome{}, true
	default: // retry
		if err := e.store.ReleaseMessage(ctx, message.ID); err != nil {
			e.log.Error("release", "message", message.ID, "error", err)
		}
		return errorOutcome{retry: true}, true
	}
}

// delivery is what a successful send produced.
type delivery struct {
	sesMessageID string
	sentAt       time.Time
}

// deliver renders and sends one message. content overrides the campaign body for test sends.
func (e *Engine) deliver(ctx context.Context, message model.Message, loaded store.MessageContext,
	contentOverride *model.Campaign) (delivery, error) {
	content, err := e.contentFor(message, loaded, contentOverride)
	if err != nil {
		return delivery{}, err
	}

	now := time.Now().UTC()
	reference := sign.Ref{ContactID: loaded.Contact.ID, NewsletterID: loaded.Newsletter.ID}
	input := rules.RecipientInput{
		AppURL:     e.cfg.AppURL,
		Campaign:   content,
		Newsletter: loaded.Newsletter,
		Contact:    loaded.Contact,
		MessageID:  message.ID,
		Kind:       message.Kind,
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

	if err := e.bucket.Take(ctx); err != nil {
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
	sesMessageID, err := e.sender.Send(sendCtx, email, tags)
	if err != nil {
		return delivery{}, err
	}
	return delivery{sesMessageID: sesMessageID, sentAt: now}, nil
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
		// The last-chance email belongs to the list cleanup, which arrives in phase 7.
		return model.Campaign{}, errors.New("last-chance emails are not implemented yet")
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
		if err := e.store.MarkMessageSent(writeCtx, message.ID, sent.sesMessageID, sent.sentAt); err != nil {
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
	unfinished, err := e.store.CountUnfinishedCampaignMessages(ctx, campaignID)
	if err != nil || unfinished > 0 {
		return err
	}
	moved, err := e.store.SetCampaignStatus(ctx, campaignID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignSent)
	if err != nil || !moved {
		return err
	}
	return e.store.SetCampaignSentAt(ctx, campaignID, time.Now().UTC())
}

// Run sends batches until ctx is cancelled. It never starts a batch after cancellation, and a batch
// already in flight finishes its writes, so a graceful shutdown cannot leave a message sent in SES
// but still claimed here without a status (design §3.1).
func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.cfg.PollInterval)
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
		// With sending off there is nothing to do, and that is a setting, not a failure: returning
		// the error would take the HTTP server down with the sender, and a fresh deployment starts
		// with sending off on purpose.
		if !e.cfg.SendingOn {
			continue
		}
		for {
			result, err := e.SendBatch(ctx, nil)
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
	} else {
		audience, err := e.store.SelectAudience(ctx, campaign.NewsletterID, campaign.AudienceLimit)
		if err != nil {
			return QueueResult{}, err
		}
		if len(audience) == 0 {
			return QueueResult{Reason: "No active contacts to send to"}, nil
		}

		// D15: the whole step is reserved up front, so two campaigns cannot overshoot the day
		// together — and in the same transaction as the first chunk of messages, so a crash cannot
		// spend the day on mail that was never written.
		day := store.Day(time.Now())
		pool, credentialMax, err := e.resolveCharge(ctx, charge)
		if err != nil {
			return QueueResult{}, err
		}
		messages := toMessages(campaign, audience)
		fits, inserted, err := e.store.ReserveAndInsert(ctx, day, pool, len(messages),
			e.cfg.Limits.MaxSendsPerDay, credentialMax, messages)
		if err != nil {
			return QueueResult{}, err
		}
		if !fits {
			return QueueResult{Reason: e.budgetReason(ctx, day, len(messages), e.cfg.Limits.MaxSendsPerDay, credentialMax)}, nil
		}
		if inserted < len(messages) {
			written, err := e.store.InsertMessages(ctx, messages[inserted:])
			if err != nil {
				// The rest never got written, so the day must not stay charged for it.
				e.refund(ctx, day, pool, len(messages)-inserted-written)
				return QueueResult{}, err
			}
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
	messages := toMessages(campaign, fresh)
	fits, inserted, err := e.store.ReserveAndInsert(ctx, day, pool, len(messages),
		e.cfg.Limits.MaxSendsPerDay, credentialMax, messages)
	if err != nil {
		return QueueResult{}, err
	}
	if !fits {
		return QueueResult{Reason: e.budgetReason(ctx, day, len(messages), e.cfg.Limits.MaxSendsPerDay, credentialMax)}, nil
	}
	if inserted < len(messages) {
		written, err := e.store.InsertMessages(ctx, messages[inserted:])
		if err != nil {
			e.refund(ctx, day, pool, len(messages)-inserted-written)
			return QueueResult{}, err
		}
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

	message := model.Message{
		ID: ids.New(), Kind: model.KindTest, CampaignID: campaignID,
		NewsletterID: newsletter.ID, ContactID: rules.TestContactID, Email: email,
		Provider: providers.Mailbox(email),
		Status:   model.MessagePending, CreatedAt: time.Now().UTC(),
	}
	if _, err := e.store.InsertMessages(ctx, []model.Message{message}); err != nil {
		return TestResult{}, err
	}
	claimed, err := e.store.ClaimMessagesByID(ctx, []string{message.ID})
	if err != nil || len(claimed) != 1 {
		return TestResult{}, fmt.Errorf("claim test message: %w", err)
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
	sent, err := e.deliver(ctx, message, loaded, &subject)
	if err != nil {
		if markErr := e.store.MarkMessageFailed(ctx, message.ID, err.Error()); markErr != nil {
			e.log.Error("mark failed", "message", message.ID, "error", markErr)
		}
		return TestResult{}, err
	}
	if err := e.store.MarkMessageSent(ctx, message.ID, sent.sesMessageID, sent.sentAt); err != nil {
		return TestResult{}, err
	}
	if err := e.store.SetCampaignTestSentAt(ctx, campaignID, sent.sentAt); err != nil {
		return TestResult{}, err
	}
	return TestResult{MessageID: message.ID, SESMessageID: sent.sesMessageID, SentAt: sent.sentAt}, nil
}

// sendTimeout bounds one call to SES. The request runs detached from the batch's context (see
// deliver), so it needs a deadline of its own or a hung connection would hold the shutdown open.
const sendTimeout = 30 * time.Second

// refund gives a day's budget back for messages that were charged for and never written. It is
// best-effort by design: the campaign is not queued either way, and the operator's next attempt
// reports the budget it actually finds.
func (e *Engine) refund(ctx context.Context, day, pool string, n int) {
	if n <= 0 {
		return
	}
	if err := e.store.Refund(ctx, day, pool, n); err != nil {
		e.log.Error("refund the day's budget", "day", day, "messages", n, "error", err)
	}
}

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

// budgetReason says which ceiling a step ran into.
//
// Both are checked before anything is written, and the global one first, so on the refusal path the
// engine asks which of them the day has actually reached. "MAX_SENDS_PER_DAY" and "this credential's
// daily_budget" are different answers to whoever reads the job's last_error, and guessing wrong
// sends them looking at the wrong number.
func (e *Engine) budgetReason(ctx context.Context, day string, messages, globalMax, credentialMax int) string {
	if globalMax > 0 {
		if reserved, err := e.store.Reserved(ctx, day, ""); err == nil && reserved+messages > globalMax {
			return fmt.Sprintf(
				"the daily send budget is spent: %d messages would go over MAX_SENDS_PER_DAY", messages)
		}
	}
	if credentialMax > 0 {
		return fmt.Sprintf(
			"the daily send budget is spent: %d messages would go over this credential's daily_budget of %d",
			messages, credentialMax)
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
			return nothingToSend(result, err)
		}
		if skip, err := e.scheduledSendIsStale(ctx, payload.CampaignID, payload.ScheduledAt); err != nil || skip {
			return err
		}
		result, err := e.Queue(ctx, payload.CampaignID, charge)
		return nothingToSend(result, err)
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
	message := model.Message{
		ID: ids.New(), Kind: kind, NewsletterID: newsletter.ID, ContactID: contact.ID,
		Email: contact.Email, Status: model.MessagePending, CreatedAt: time.Now().UTC(),
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
		return SignupResult{Skipped: "rate limited"}, nil
	}
	if _, err := e.SendBatch(ctx, []string{message.ID}); err != nil {
		return SignupResult{}, err
	}
	return SignupResult{MessageID: message.ID}, nil
}

// errSendingOff is upstream's wording, which rules.SendErrorAction also recognises.
var errSendingOff = errors.New("Sending is off (SENDING_ENABLED is not true)") //nolint:staticcheck // ST1005: upstream wording

// toMessages turns an audience into pending campaign messages.
func toMessages(campaign model.Campaign, contacts []model.Contact) []model.Message {
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
