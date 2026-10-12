package mcpsrv

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"mailcheap/internal/core/checks"
	"mailcheap/internal/core/content"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/importer"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/schedule"
	"mailcheap/internal/core/validate"
	"mailcheap/internal/importsvc"
	"mailcheap/internal/jobs"
	"mailcheap/internal/store"
	"mailcheap/internal/uploads"
)

// The write tools (design §9). Every one of them records what it did, with the credential's name as
// the actor (design §11, layer 3), and not one of them can send a campaign by itself: sending is a
// job the serve process's scheduler runs.

type createNewsletterIn struct {
	Name                string `json:"name" jsonschema:"display name"`
	Slug                string `json:"slug" jsonschema:"URL slug, unique"`
	FromEmail           string `json:"from_email" jsonschema:"the address SES sees on the wire"`
	FromName            string `json:"from_name,omitempty" jsonschema:"display name on the From header"`
	ReplyTo             string `json:"reply_to,omitempty"`
	PostalAddress       string `json:"postal_address,omitempty" jsonschema:"the postal address in the footer"`
	ArchiveEnabled      bool   `json:"archive_enabled,omitempty" jsonschema:"publish an archive of past issues"`
	RequireConfirmation bool   `json:"require_confirmation,omitempty" jsonschema:"new subscribers confirm by email"`
}

type newsletterOut struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	FromEmail string `json:"from_email"`
}

func createNewsletter(ctx context.Context, sess *session, credential store.Credential, in createNewsletterIn) (newsletterOut, error) {
	name := strings.TrimSpace(in.Name)
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if name == "" || slug == "" {
		return newsletterOut{}, errors.New("name and slug are required")
	}
	// The address SES sees goes through the same normalization as every other address: a From: header
	// with a trailing dot or a doubled @ is a send failure waiting for the first issue.
	fromEmail := normalizeEmail(in.FromEmail)
	if fromEmail == "" {
		return newsletterOut{}, errors.New("from_email must be an address SES can send from")
	}
	now := time.Now().UTC()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: slug, Name: name,
		FromName: strings.TrimSpace(in.FromName), FromEmail: fromEmail,
		ReplyTo: strings.TrimSpace(in.ReplyTo), PostalAddress: in.PostalAddress,
		ArchiveEnabled: in.ArchiveEnabled, RequireConfirmation: in.RequireConfirmation,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := sess.deps.Store.CreateNewsletter(ctx, newsletter); err != nil {
		return newsletterOut{}, err
	}
	sess.note(ctx, credential, "newsletter.create", newsletter.Slug, map[string]any{
		"id": newsletter.ID, "slug": newsletter.Slug, "name": newsletter.Name,
	})
	return newsletterOut{
		ID: newsletter.ID, Slug: newsletter.Slug, Name: newsletter.Name, FromEmail: newsletter.FromEmail,
	}, nil
}

// updateNewsletterIn: a field that is absent is left as it is, and a field that is present replaces
// what was there — including with an empty string, which is how a value is cleared. That is why
// these are pointers: for a string, "not mentioned" and "set to empty" are different instructions,
// and the caller has no flags to tell them apart by.
type updateNewsletterIn struct {
	Newsletter          string  `json:"newsletter" jsonschema:"newsletter id or slug"`
	Name                *string `json:"name,omitempty"`
	Slug                *string `json:"slug,omitempty"`
	FromName            *string `json:"from_name,omitempty"`
	FromEmail           *string `json:"from_email,omitempty"`
	ReplyTo             *string `json:"reply_to,omitempty"`
	PostalAddress       *string `json:"postal_address,omitempty"`
	ArchiveEnabled      *bool   `json:"archive_enabled,omitempty"`
	RequireConfirmation *bool   `json:"require_confirmation,omitempty"`
}

func updateNewsletter(ctx context.Context, sess *session, credential store.Credential, in updateNewsletterIn) (newsletterOut, error) {
	newsletter, err := newsletterByRef(ctx, sess.deps.Store, in.Newsletter)
	if err != nil {
		return newsletterOut{}, err
	}
	changed := []string{}
	set := func(name string, value *string, into *string) {
		if value == nil {
			return
		}
		*into = *value
		changed = append(changed, name)
	}
	set("name", in.Name, &newsletter.Name)
	set("from_name", in.FromName, &newsletter.FromName)
	set("reply_to", in.ReplyTo, &newsletter.ReplyTo)
	set("postal_address", in.PostalAddress, &newsletter.PostalAddress)
	if in.Slug != nil {
		newsletter.Slug = strings.ToLower(strings.TrimSpace(*in.Slug))
		changed = append(changed, "slug")
	}
	if in.FromEmail != nil {
		fromEmail := normalizeEmail(*in.FromEmail)
		if fromEmail == "" {
			return newsletterOut{}, errors.New("from_email must be an address SES can send from")
		}
		newsletter.FromEmail = fromEmail
		changed = append(changed, "from_email")
	}
	if in.ArchiveEnabled != nil {
		newsletter.ArchiveEnabled = *in.ArchiveEnabled
		changed = append(changed, "archive_enabled")
	}
	if in.RequireConfirmation != nil {
		newsletter.RequireConfirmation = *in.RequireConfirmation
		changed = append(changed, "require_confirmation")
	}
	if len(changed) == 0 {
		return newsletterOut{}, errors.New("nothing to change: pass at least one field")
	}
	if newsletter.FromEmail == "" || newsletter.Name == "" || newsletter.Slug == "" {
		return newsletterOut{}, errors.New("a newsletter needs a name, a slug and a from address")
	}
	newsletter.UpdatedAt = time.Now().UTC()
	if err := sess.deps.Store.UpdateNewsletter(ctx, newsletter); err != nil {
		return newsletterOut{}, err
	}
	sess.note(ctx, credential, "newsletter.update", newsletter.Slug, map[string]any{
		"id": newsletter.ID, "changed": changed,
	})
	return newsletterOut{
		ID: newsletter.ID, Slug: newsletter.Slug, Name: newsletter.Name, FromEmail: newsletter.FromEmail,
	}, nil
}

type createCampaignIn struct {
	Newsletter  string `json:"newsletter" jsonschema:"newsletter id or slug"`
	Subject     string `json:"subject"`
	Markdown    string `json:"markdown,omitempty" jsonschema:"the body as Markdown (exactly one of markdown or html)"`
	HTML        string `json:"html,omitempty" jsonschema:"the body as HTML, kept as it is (exactly one of markdown or html)"`
	PreviewText string `json:"preview_text,omitempty" jsonschema:"the inbox preview line"`
	ArchiveSlug string `json:"archive_slug,omitempty" jsonschema:"lowercase letters, digits and dashes"`
}

type campaignOut struct {
	ID         string `json:"id"`
	Newsletter string `json:"newsletter"`
	Subject    string `json:"subject"`
	Status     string `json:"status"`
	Format     string `json:"format"`
}

func createCampaign(ctx context.Context, sess *session, credential store.Credential, in createCampaignIn) (campaignOut, error) {
	newsletter, err := newsletterByRef(ctx, sess.deps.Store, in.Newsletter)
	if err != nil {
		return campaignOut{}, err
	}
	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		return campaignOut{}, errors.New("subject is required")
	}
	format, body, err := bodyOf(in.Markdown, in.HTML)
	if err != nil {
		return campaignOut{}, err
	}
	if len(body) > validate.MaxBodyBytes {
		return campaignOut{}, fmt.Errorf("the body is %d bytes; the limit is %d", len(body), validate.MaxBodyBytes)
	}
	archiveSlug := strings.TrimSpace(in.ArchiveSlug)
	if archiveSlug != "" && !sluggable(archiveSlug) {
		return campaignOut{}, errors.New(
			"archive_slug can only use lowercase letters, numbers and dashes (120 at most)")
	}

	campaign := model.NewCampaign(newsletter.ID, subject, time.Now().UTC())
	campaign.PreviewText = strings.TrimSpace(in.PreviewText)
	campaign.Format = format
	campaign.Body = body
	campaign.Source = "mcp"
	campaign.ThumbnailURL = firstImage(format, body)
	if archiveSlug != "" {
		unique, err := sess.deps.Store.UniqueArchiveSlug(ctx, archiveSlug)
		if err != nil {
			return campaignOut{}, err
		}
		campaign.ArchiveSlug = unique
	}
	if err := sess.deps.Store.CreateCampaign(ctx, campaign); err != nil {
		return campaignOut{}, err
	}
	sess.note(ctx, credential, "campaign.create", campaign.ID, map[string]any{
		"id": campaign.ID, "newsletter": newsletter.Slug, "subject": campaign.Subject,
		"format": string(campaign.Format),
	})
	return describe(campaign, newsletter.Slug), nil
}

type updateCampaignIn struct {
	Campaign    string  `json:"campaign" jsonschema:"campaign id, or the subject of a draft"`
	Subject     *string `json:"subject,omitempty"`
	PreviewText *string `json:"preview_text,omitempty"`
	Markdown    *string `json:"markdown,omitempty"`
	HTML        *string `json:"html,omitempty"`
	ArchiveSlug *string `json:"archive_slug,omitempty"`
	Limit       *int    `json:"limit,omitempty" jsonschema:"cap the audience at the next N contacts (0 = everyone)"`
}

func updateCampaign(ctx context.Context, sess *session, credential store.Credential, in updateCampaignIn) (campaignOut, error) {
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return campaignOut{}, err
	}
	changed := []string{}
	if in.Subject != nil {
		campaign.Subject = strings.TrimSpace(*in.Subject)
		changed = append(changed, "subject")
	}
	if in.PreviewText != nil {
		campaign.PreviewText = *in.PreviewText
		changed = append(changed, "preview_text")
	}
	if in.Markdown != nil || in.HTML != nil {
		markdown, html := "", ""
		if in.Markdown != nil {
			markdown = *in.Markdown
		}
		if in.HTML != nil {
			html = *in.HTML
		}
		format, body, err := bodyOf(markdown, html)
		if err != nil {
			return campaignOut{}, err
		}
		if len(body) > validate.MaxBodyBytes {
			return campaignOut{}, fmt.Errorf("the body is %d bytes; the limit is %d", len(body), validate.MaxBodyBytes)
		}
		campaign.Format, campaign.Body = format, body
		campaign.ThumbnailURL = firstImage(format, body)
		changed = append(changed, "body")
	}
	if in.ArchiveSlug != nil {
		archiveSlug := strings.TrimSpace(*in.ArchiveSlug)
		if archiveSlug != "" {
			if !sluggable(archiveSlug) {
				return campaignOut{}, errors.New(
					"archive_slug can only use lowercase letters, numbers and dashes (120 at most)")
			}
			unique, err := sess.deps.Store.UniqueArchiveSlug(ctx, archiveSlug)
			if err != nil {
				return campaignOut{}, err
			}
			archiveSlug = unique
		}
		campaign.ArchiveSlug = archiveSlug
		changed = append(changed, "archive_slug")
	}
	if in.Limit != nil {
		if *in.Limit < 0 {
			return campaignOut{}, errors.New("limit cannot be negative")
		}
		campaign.AudienceLimit = *in.Limit
		changed = append(changed, "limit")
	}
	if len(changed) == 0 {
		return campaignOut{}, errors.New("nothing to change: pass at least one field")
	}
	if campaign.Subject == "" {
		return campaignOut{}, errors.New("a campaign needs a subject")
	}
	campaign.UpdatedAt = time.Now().UTC()
	if err := sess.deps.Store.UpdateCampaign(ctx, campaign); err != nil {
		return campaignOut{}, err
	}
	newsletter, err := sess.deps.Store.Newsletter(ctx, campaign.NewsletterID)
	if err != nil {
		return campaignOut{}, err
	}
	sess.note(ctx, credential, "campaign.update", campaign.ID, map[string]any{
		"id": campaign.ID, "changed": changed,
	})
	return describe(campaign, newsletter.Slug), nil
}

type sendTestIn struct {
	Campaign string `json:"campaign" jsonschema:"campaign id, or the subject of a draft"`
	To       string `json:"to" jsonschema:"the address to send the test copy to"`
}

type sendTestOut struct {
	MessageID string `json:"messageId"`
	To        string `json:"to"`
	SentAt    string `json:"sentAt"`
}

// sendTest is the one send a tool makes itself, and the reason is in the question it answers: "did
// this render, and did it arrive?" has to come back with an answer, and a job id is not one.
//
// It is charged to the credential's budget like a campaign step (one message), so an agent that
// calls it in a loop runs out of its own allowance instead of turning into a bulk sender.
func sendTest(ctx context.Context, sess *session, credential store.Credential, in sendTestIn) (sendTestOut, error) {
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return sendTestOut{}, err
	}
	if err := sess.chargeOne(ctx, credential); err != nil {
		return sendTestOut{}, err
	}
	result, err := sess.deps.Mailer.Test(ctx, campaign.ID, in.To)
	if err != nil {
		// Nothing was sent, so the day must not stay charged for it — the engine refunds a step the
		// same way when messages it had reserved could not be written.
		if refundErr := sess.deps.Store.Refund(ctx, store.Day(time.Now()), credential.ID, 1); refundErr != nil {
			sess.deps.Log.Error("refund the test send", "credential", credential.ID, "error", refundErr)
		}
		return sendTestOut{}, err
	}
	sess.note(ctx, credential, "campaign.test", campaign.ID, map[string]any{
		"id": campaign.ID, "to": in.To, "messageId": result.MessageID,
	})
	return sendTestOut{
		MessageID: result.MessageID, To: in.To, SentAt: result.SentAt.UTC().Format(time.RFC3339),
	}, nil
}

type sendCampaignIn struct {
	Campaign string `json:"campaign" jsonschema:"campaign id, or the subject of a draft"`
	Limit    int    `json:"limit,omitempty" jsonschema:"send only the next N contacts (a stepped send)"`
}

type sendOut struct {
	JobID   string `json:"job_id"`
	Held    bool   `json:"held,omitempty"`
	Message string `json:"message"`
}

// sendCampaign checks, then queues. It does not send: the job it returns is what the serve process's
// scheduler picks up, and the campaign's own status is what says how far it got.
//
// There is no force argument, and there cannot be one: the checks run here are the same ones the
// engine enforces, and a blocking one ends the call (design §11, layer 0). The same goes for the
// status itself: without a limit this is a fresh send, which the engine's Queue only starts from
// draft, scheduled or paused, and with one it is a stepped send (QueueStep), which only continues a
// campaign that has gone out. Refusing here is what the CLI gets from the engine synchronously —
// and it leaves no job and no audit row, because nothing was sent.
func sendCampaign(ctx context.Context, sess *session, credential store.Credential, in sendCampaignIn) (sendOut, error) {
	if in.Limit < 0 {
		return sendOut{}, errors.New("limit cannot be negative")
	}
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return sendOut{}, err
	}
	if in.Limit > 0 {
		if campaign.Status != model.CampaignSent {
			return sendOut{}, fmt.Errorf(
				"the campaign is %s: only a sent one can be sent in steps", campaign.Status)
		}
	} else if !campaign.Status.Queueable() {
		return sendOut{}, fmt.Errorf(
			"the campaign is %s: only a draft, scheduled or paused one can be sent", campaign.Status)
	}
	if err := sess.checkCampaignIsSendable(ctx, campaign); err != nil {
		return sendOut{}, err
	}
	if err := sess.requireASender(); err != nil {
		return sendOut{}, err
	}
	if err := sess.checkBudget(ctx, credential, campaign, in.Limit); err != nil {
		return sendOut{}, err
	}

	payload := map[string]any{"campaignId": campaign.ID, "credentialId": credential.ID}
	if in.Limit > 0 {
		payload["additional"] = in.Limit
	}
	held, err := sess.enqueueSend(ctx, credential, campaign, payload)
	if err != nil {
		return sendOut{}, err
	}
	sess.note(ctx, credential, "campaign.send", campaign.ID, map[string]any{
		"id": campaign.ID, "limit": in.Limit, "credential": credential.Name,
	})
	return sendOut{
		JobID: held.jobID, Held: held.held,
		Message: "queued: the running server is sending it. Poll get_job with this id.",
	}, nil
}

type scheduleCampaignIn struct {
	Campaign string `json:"campaign" jsonschema:"campaign id, or the subject of a draft"`
	At       string `json:"at" jsonschema:"when to send, as an RFC3339 instant"`
}

type scheduleOut struct {
	JobID       string `json:"job_id"`
	ScheduledAt string `json:"scheduled_at"`
	Held        bool   `json:"held,omitempty"`
}

func scheduleCampaign(ctx context.Context, sess *session, credential store.Credential, in scheduleCampaignIn) (scheduleOut, error) {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(in.At))
	if err != nil {
		return scheduleOut{}, fmt.Errorf("at: %q is not an RFC3339 time", in.At)
	}
	at = at.UTC()
	if problem := schedule.Problem(at, time.Now().UTC()); problem != "" {
		return scheduleOut{}, errors.New(problem)
	}
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return scheduleOut{}, err
	}
	if err := sess.checkCampaignIsSendable(ctx, campaign); err != nil {
		return scheduleOut{}, err
	}
	if err := sess.requireASender(); err != nil {
		return scheduleOut{}, err
	}
	moved, err := sess.deps.Store.SetCampaignStatus(ctx, campaign.ID,
		[]model.CampaignStatus{model.CampaignDraft, model.CampaignScheduled}, model.CampaignScheduled)
	if err != nil {
		return scheduleOut{}, err
	}
	if !moved {
		return scheduleOut{}, fmt.Errorf(
			"the campaign is %s: only a draft or a scheduled one can be scheduled", campaign.Status)
	}
	if err := sess.deps.Store.SetCampaignSchedule(ctx, campaign.ID, &at); err != nil {
		return scheduleOut{}, err
	}
	held, err := sess.enqueueSend(ctx, credential, campaign, map[string]any{
		"campaignId": campaign.ID, "credentialId": credential.ID,
		"scheduledAt": at.Format(time.RFC3339),
	})
	if err != nil {
		return scheduleOut{}, err
	}
	sess.note(ctx, credential, "campaign.schedule", campaign.ID, map[string]any{
		"id": campaign.ID, "at": at.Format(time.RFC3339), "credential": credential.Name,
	})
	return scheduleOut{JobID: held.jobID, ScheduledAt: at.Format(time.RFC3339), Held: held.held}, nil
}

type resumeCampaignOut struct {
	JobID   string `json:"job_id"`
	Held    bool   `json:"held,omitempty"`
	Message string `json:"message"`
}

// resumeCampaign queues the rest of a paused campaign. The audience is the one the campaign already
// has — the reservation was made when it first went out — so this is a queue and not a new send.
func resumeCampaign(ctx context.Context, sess *session, credential store.Credential, in campaignRefIn) (resumeCampaignOut, error) {
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return resumeCampaignOut{}, err
	}
	if campaign.Status != model.CampaignPaused {
		return resumeCampaignOut{}, fmt.Errorf(
			"the campaign is %s: only a paused one can be resumed", campaign.Status)
	}
	if err := sess.requireASender(); err != nil {
		return resumeCampaignOut{}, err
	}
	if err := sess.checkBudget(ctx, credential, campaign, 0); err != nil {
		return resumeCampaignOut{}, err
	}
	held, err := sess.enqueueSend(ctx, credential, campaign, map[string]any{
		"campaignId": campaign.ID, "credentialId": credential.ID,
	})
	if err != nil {
		return resumeCampaignOut{}, err
	}
	sess.note(ctx, credential, "campaign.resume", campaign.ID, map[string]any{
		"id": campaign.ID, "credential": credential.Name,
	})
	return resumeCampaignOut{
		JobID: held.jobID, Held: held.held,
		Message: "queued: the running server is sending the rest. Poll get_job with this id.",
	}, nil
}

type pauseCampaignOut struct {
	Campaign string `json:"campaign"`
	Status   string `json:"status"`
}

func pauseCampaign(ctx context.Context, sess *session, credential store.Credential, in campaignRefIn) (pauseCampaignOut, error) {
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return pauseCampaignOut{}, err
	}
	moved, err := sess.deps.Store.SetCampaignStatus(ctx, campaign.ID,
		[]model.CampaignStatus{model.CampaignSending}, model.CampaignPaused)
	if err != nil {
		return pauseCampaignOut{}, err
	}
	if !moved {
		return pauseCampaignOut{}, fmt.Errorf(
			"the campaign is %s: only a sending one can be paused", campaign.Status)
	}
	sess.note(ctx, credential, "campaign.pause", campaign.ID, map[string]any{"id": campaign.ID})
	return pauseCampaignOut{Campaign: campaign.ID, Status: string(model.CampaignPaused)}, nil
}

type importContactsIn struct {
	Newsletter string      `json:"newsletter" jsonschema:"newsletter id or slug"`
	Rows       []importRow `json:"rows" jsonschema:"the contacts to import"`
	Source     string      `json:"source,omitempty" jsonschema:"what the new contacts' source says (default 'import')"`
	DryRun     bool        `json:"dry_run,omitempty" jsonschema:"report what would happen, and write nothing"`
}

// importRow is one contact, the same columns a provider's CSV export carries.
type importRow struct {
	Email          string   `json:"email"`
	Status         string   `json:"status,omitempty" jsonschema:"active, unsubscribed, bounced or complained"`
	SubscribedAt   string   `json:"subscribed_at,omitempty" jsonschema:"RFC3339, or YYYY-MM-DD"`
	EmailsReceived *int     `json:"emails_received,omitempty"`
	OpenRate       *float64 `json:"open_rate,omitempty" jsonschema:"0..1, or a percentage"`
	ClickRate      *float64 `json:"click_rate,omitempty"`
}

type importOut struct {
	importsvc.Result
	Newsletter string `json:"newsletter"`
}

// importContacts applies rows the caller already has. The CLI's `contact import` reads a CSV file
// from a path; an agent has rows, so that is what this takes — the same Merge, the same chunk size,
// the same dry run.
func importContacts(ctx context.Context, sess *session, credential store.Credential, in importContactsIn) (importOut, error) {
	newsletter, err := newsletterByRef(ctx, sess.deps.Store, in.Newsletter)
	if err != nil {
		return importOut{}, err
	}
	if len(in.Rows) == 0 {
		return importOut{}, errors.New("rows is empty")
	}
	// Upstream MAX_IMPORT_ROWS_PER_REQUEST: one call is one request, and a caller with more rows
	// makes more of them rather than one unbounded one.
	if len(in.Rows) > importer.MaxRowsPerRequest {
		return importOut{}, fmt.Errorf("rows has %d entries; one call imports at most %d",
			len(in.Rows), importer.MaxRowsPerRequest)
	}
	rows := make([]importer.Row, 0, len(in.Rows))
	skipped := 0
	for _, row := range in.Rows {
		normalized, ok := row.normalize()
		if !ok {
			skipped++
			continue
		}
		rows = append(rows, normalized)
	}
	rows = importer.Dedupe(rows)

	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "import"
	}
	result, err := importsvc.Apply(ctx, sess.deps.Store, newsletter.ID, source, rows, in.DryRun, skipped, nil)
	if err != nil {
		return importOut{}, err
	}
	sess.note(ctx, credential, "contact.import", newsletter.Slug, map[string]any{
		"newsletter": newsletter.Slug, "source": source, "rows": result.Rows,
		"created": result.Created, "updated": result.Updated, "suppressed": result.Suppressed,
		"dryRun": result.DryRun,
	})
	return importOut{Result: result, Newsletter: newsletter.Slug}, nil
}

// normalize reads one row into the importer's shape. ok is false when the address is not usable,
// which is the same rule the CSV path applies (importer.NormalizeRow's email check).
func (r importRow) normalize() (importer.Row, bool) {
	email := normalizeEmail(r.Email)
	if email == "" {
		return importer.Row{}, false
	}
	row := importer.Row{Email: email, Status: importer.NormalizeStatus(r.Status)}
	if strings.TrimSpace(r.SubscribedAt) != "" {
		at, ok := parseDate(r.SubscribedAt)
		if !ok {
			return importer.Row{}, false
		}
		row.SubscribedAt = &at
	}
	row.EmailsReceived = r.EmailsReceived
	if r.OpenRate != nil {
		row.OpenRate = clampRate(*r.OpenRate)
	}
	if r.ClickRate != nil {
		row.ClickRate = clampRate(*r.ClickRate)
	}
	return row, true
}

type suppressionIn struct {
	Email  string `json:"email"`
	Reason string `json:"reason,omitempty" jsonschema:"manual, bounce or complaint (default manual)"`
	Detail string `json:"detail,omitempty"`
}

type suppressionOut struct {
	Email   string `json:"email"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

// addSuppression stops every newsletter from sending to one address: suppressions are a set of
// addresses, not a per-newsletter list.
func addSuppression(ctx context.Context, sess *session, credential store.Credential, in suppressionIn) (suppressionOut, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return suppressionOut{}, errors.New("email is invalid")
	}
	reason := model.SuppressManual
	if strings.TrimSpace(in.Reason) != "" {
		reason = model.SuppressionReason(strings.ToLower(strings.TrimSpace(in.Reason)))
		switch reason {
		case model.SuppressManual, model.SuppressBounce, model.SuppressComplaint:
		default:
			return suppressionOut{}, errors.New("reason must be manual, bounce or complaint")
		}
	}
	if err := sess.deps.Store.AddSuppression(ctx, email, reason, in.Detail); err != nil {
		return suppressionOut{}, err
	}
	sess.note(ctx, credential, "suppression.add", email, map[string]any{
		"email": email, "reason": string(reason),
	})
	return suppressionOut{Email: email, Reason: string(reason), Detail: in.Detail}, nil
}

func removeSuppression(ctx context.Context, sess *session, credential store.Credential, in suppressionIn) (suppressionOut, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return suppressionOut{}, errors.New("email is invalid")
	}
	removed, err := sess.deps.Store.RemoveSuppression(ctx, email)
	if err != nil {
		return suppressionOut{}, err
	}
	sess.note(ctx, credential, "suppression.remove", email, map[string]any{
		"email": email, "removed": removed,
	})
	return suppressionOut{Email: email, Removed: removed}, nil
}

type uploadImageIn struct {
	Filename string `json:"filename" jsonschema:"the name the image is listed under"`
	Data     string `json:"data" jsonschema:"the image as base64, with or without a data: URL prefix"`
}

type uploadImageOut struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
	Bytes     int    `json:"bytes"`
}

func uploadImage(ctx context.Context, sess *session, credential store.Credential, in uploadImageIn) (uploadImageOut, error) {
	content, err := decodeImage(in.Data)
	if err != nil {
		return uploadImageOut{}, err
	}
	upload, err := uploads.Save(ctx, sess.deps.Store, sess.deps.Config.DataDir,
		strings.TrimSpace(in.Filename), content)
	if err != nil {
		return uploadImageOut{}, err
	}
	url := uploads.URL(sess.deps.Config.AppURL, upload)
	sess.note(ctx, credential, "upload.image", upload.FileName, map[string]any{
		"id": upload.ID, "mediaType": upload.MediaType, "bytes": upload.Bytes, "url": url,
	})
	return uploadImageOut{
		ID: upload.ID, URL: url, MediaType: upload.MediaType, Bytes: upload.Bytes,
	}, nil
}

// --- shared pieces --------------------------------------------------------

// held is what enqueueing a send did: the job's id, and whether SEND_APPROVAL=manual parked it for a
// human instead of letting it run.
type held struct {
	jobID string
	held  bool
}

// enqueueSend puts a send on the queue, or parks it for approval.
func (s *session) enqueueSend(ctx context.Context, credential store.Credential, campaign model.Campaign,
	payload map[string]any) (held, error) {
	runAt := time.Now().UTC()
	if scheduled, ok := payload["scheduledAt"].(string); ok {
		at, err := time.Parse(time.RFC3339, scheduled)
		if err != nil {
			return held{}, err
		}
		runAt = at
	}
	jobID, err := s.deps.Jobs.Enqueue(ctx, jobs.KindSendCampaign, payload, runAt)
	if err != nil {
		return held{}, err
	}
	if s.deps.Config.SendApproval != "manual" {
		return held{jobID: jobID}, nil
	}
	if err := s.deps.Jobs.Hold(ctx, jobID); err != nil {
		return held{}, err
	}
	return held{jobID: jobID, held: true}, nil
}

// requireASender refuses a send that nothing would pick up.
//
// MCP is not a sender: the job it queues is run by the serve process's scheduler, and if no serve
// process is stamping the heartbeat the job would sit there. Answering "queued" to that would look
// like a send to a caller that only sees a job id — and design §5.2 is explicit that sending belongs
// to the server, so there is no second sender to start here.
func (s *session) requireASender() error {
	running, err := s.deps.Store.ServeIsRunning(context.Background(), time.Now().UTC())
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	return errors.New("no mailcheap serve process is running, so nothing would send this. " +
		"Start the server (mailcheap serve, or the container), then send again")
}

// checkCampaignIsSendable runs the pre-send checklist and stops on a blocking failure. It is the
// same list `mailcheap campaign check` prints and the same one the engine enforces, so a tool call
// cannot get past a check the CLI would have stopped at.
func (s *session) checkCampaignIsSendable(ctx context.Context, campaign model.Campaign) error {
	if !s.deps.Config.SendingOn {
		return errors.New("Sending is off: set SENDING_ENABLED=true") //nolint:staticcheck // ST1005: upstream wording
	}
	report, _, err := preSendChecks(ctx, s.deps, campaign)
	if err != nil {
		return err
	}
	if blocked, bad := checks.Blocked(report); bad {
		return fmt.Errorf("%s: %s", blocked.ID, blocked.Message)
	}
	return nil
}

// checkBudget answers "would this fit" before the job is queued, so an agent learns immediately
// rather than through a job's last_error. Both ceilings are checked, the day's global one and the
// credential's own.
//
// Its count is an upper bound: the whole active audience, capped by the campaign's own limit and by
// this call's. The authoritative reservation is the engine's, made when the job runs — this one can
// refuse a step the engine would have accepted when the two numbers are close, and can never let
// through one the engine would refuse, which is the direction a pre-flight has to be wrong in.
func (s *session) checkBudget(ctx context.Context, credential store.Credential, campaign model.Campaign, limit int) error {
	wanted, err := s.audienceSize(ctx, campaign)
	if err != nil {
		return err
	}
	if limit > 0 && limit < wanted {
		wanted = limit
	}
	if wanted == 0 {
		return nil
	}
	day := store.Day(time.Now())
	if global := s.deps.Config.Limits.MaxSendsPerDay; global > 0 {
		reserved, err := s.deps.Store.Reserved(ctx, day, "")
		if err != nil {
			return err
		}
		if reserved+wanted > global {
			return fmt.Errorf(
				"the day's send budget is spent: %d of MAX_SENDS_PER_DAY are already reserved, and this would take %d more",
				reserved, wanted)
		}
	}
	if credential.DailyBudget > 0 {
		spent, err := s.deps.Store.Reserved(ctx, day, credential.ID)
		if err != nil {
			return err
		}
		if spent+wanted > credential.DailyBudget {
			return fmt.Errorf(
				"this credential's daily_budget is %d and %d of it is already reserved today; %d more would go over",
				credential.DailyBudget, spent, wanted)
		}
	}
	return nil
}

// audienceSize is what a full send of this campaign would write.
func (s *session) audienceSize(ctx context.Context, campaign model.Campaign) (int, error) {
	audience, err := s.deps.Store.SelectAudience(ctx, campaign.NewsletterID, campaign.AudienceLimit)
	if err != nil {
		return 0, err
	}
	return len(audience), nil
}

// chargeOne reserves one send against a credential's day, for the one tool that sends a single
// message itself.
func (s *session) chargeOne(ctx context.Context, credential store.Credential) error {
	fits, err := s.deps.Store.Reserve(ctx, store.Day(time.Now()), credential.ID, 1,
		s.deps.Config.Limits.MaxSendsPerDay, credential.DailyBudget)
	if err != nil {
		return err
	}
	if !fits {
		return fmt.Errorf(
			"this credential's daily_budget is %d and it is spent for today; test sends count toward it",
			credential.DailyBudget)
	}
	return nil
}

// bodyOf picks the format. Exactly one of markdown and html has to be there — the same rule the v1
// API applies, with the same words, so a client of either gets the same answer.
func bodyOf(markdown, html string) (model.Format, string, error) {
	hasMarkdown := strings.TrimSpace(markdown) != ""
	hasHTML := strings.TrimSpace(html) != ""
	if hasMarkdown == hasHTML {
		return "", "", errors.New("send either markdown or html, not both and not neither")
	}
	if hasHTML {
		return model.FormatHTML, html, nil
	}
	return model.FormatMarkdown, markdown, nil
}

func describe(campaign model.Campaign, newsletterSlug string) campaignOut {
	return campaignOut{
		ID: campaign.ID, Newsletter: newsletterSlug, Subject: campaign.Subject,
		Status: string(campaign.Status), Format: string(campaign.Format),
	}
}

// parseDate reads the two shapes an exported subscribe date arrives in.
func parseDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UTC(), true
		}
	}
	return time.Time{}, false
}

// clampRate reads a rate that may be a fraction or a percentage, the way the CSV importer does.
func clampRate(value float64) *float64 {
	if value > 1 {
		value /= 100
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return &value
}

// decodeImage reads the image out of the argument. A data: URL prefix is accepted because that is
// what a tool result containing a picture looks like.
func decodeImage(data string) ([]byte, error) {
	payload := strings.TrimSpace(data)
	if index := strings.Index(payload, ","); index >= 0 && strings.HasPrefix(payload, "data:") {
		payload = payload[index+1:]
	}
	if payload == "" {
		return nil, errors.New("data is empty")
	}
	// One base64 character is six bits: anything under this bound cannot decode to an image over the
	// limit, so the check happens before the allocation rather than after it.
	if len(payload) > (uploads.MaxBytes/3+1)*4 {
		return nil, fmt.Errorf("the image is over the %d byte limit", uploads.MaxBytes)
	}
	content, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("data is not valid base64")
	}
	return content, nil
}

// sluggable is ARCHIVE_SLUG_PATTERN: lowercase letters, digits and dashes, 120 at most.
func sluggable(value string) bool {
	if len(value) > 120 || value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// firstImage is upstream's firstImageUrl: the archive card's image, recorded when the draft is
// written so nothing has to walk every body later.
func firstImage(format model.Format, body string) string {
	return content.FirstImageOf(format, body)
}
