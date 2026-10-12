package mcpsrv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"nibwire/internal/core/checks"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
	"nibwire/internal/core/rules"
	"nibwire/internal/core/stats"
	"nibwire/internal/core/validate"
	"nibwire/internal/mailer"
	"nibwire/internal/statsquery"
	"nibwire/internal/store"
)

// The read tools (design §9). Every list takes PageArgs; the getters take a reference an operator
// would recognise — an id, or the name they know it by.

type listNewslettersIn struct {
	PageArgs
}

type listNewslettersOut struct {
	page[newsletterView]
}

func listNewsletters(ctx context.Context, sess *session, _ store.Credential, in listNewslettersIn) (listNewslettersOut, error) {
	limit, offset, err := in.resolve()
	if err != nil {
		return listNewslettersOut{}, err
	}
	all, err := sess.deps.Store.Newsletters(ctx)
	if err != nil {
		return listNewslettersOut{}, err
	}
	// Newsletters are a handful by nature, and the store has no page query for them; slicing the
	// list keeps one implementation of the page shape rather than adding a query nothing else wants.
	return listNewslettersOut{paginate(slice(views(all), offset), limit, offset)}, nil
}

type getNewsletterIn struct {
	Newsletter string `json:"newsletter" jsonschema:"newsletter id or slug"`
}

func getNewsletter(ctx context.Context, sess *session, _ store.Credential, in getNewsletterIn) (newsletterView, error) {
	newsletter, err := newsletterByRef(ctx, sess.deps.Store, in.Newsletter)
	if err != nil {
		return newsletterView{}, err
	}
	return viewNewsletter(newsletter), nil
}

type listCampaignsIn struct {
	Newsletter string `json:"newsletter,omitempty" jsonschema:"only this newsletter's campaigns (id or slug)"`
	PageArgs
}

type listCampaignsOut struct {
	page[campaignView]
}

func listCampaigns(ctx context.Context, sess *session, _ store.Credential, in listCampaignsIn) (listCampaignsOut, error) {
	limit, offset, err := in.resolve()
	if err != nil {
		return listCampaignsOut{}, err
	}
	newsletterID := ""
	if strings.TrimSpace(in.Newsletter) != "" {
		newsletter, err := newsletterByRef(ctx, sess.deps.Store, in.Newsletter)
		if err != nil {
			return listCampaignsOut{}, err
		}
		newsletterID = newsletter.ID
	}
	// One row more than asked for, so has_more is a fact rather than a guess.
	campaigns, err := sess.deps.Store.Campaigns(ctx, newsletterID, limit+1, offset)
	if err != nil {
		return listCampaignsOut{}, err
	}
	return listCampaignsOut{paginate(campaignViews(campaigns), limit, offset)}, nil
}

type campaignRefIn struct {
	Campaign string `json:"campaign" jsonschema:"campaign id, or the subject of a draft"`
}

func getCampaign(ctx context.Context, sess *session, _ store.Credential, in campaignRefIn) (campaignView, error) {
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return campaignView{}, err
	}
	return viewCampaign(campaign), nil
}

// CampaignReport is get_campaign_report: the issue's own counters, the same figures by mailbox
// provider, and how many of the most engaged have not had it yet.
type CampaignReport struct {
	Campaign   campaignView   `json:"campaign"`
	Stats      countersView   `json:"stats"`
	LastChance lastChanceView `json:"last_chance"`
	Providers  []providerView `json:"providers"`
	Remaining  int            `json:"remaining"`
	// Stranded is messages that are still waiting or claimed under a campaign that is not sending any
	// more. It should always be 0; anything else is a campaign whose report would otherwise say the
	// issue went out while part of the audience never got it, and nobody would ever claim those
	// messages (§phase 9 #37).
	Stranded int `json:"stranded_messages,omitempty"`
}

func getCampaignReport(ctx context.Context, sess *session, _ store.Credential, in campaignRefIn) (CampaignReport, error) {
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return CampaignReport{}, err
	}
	counters, lastChance, err := sess.deps.Store.CampaignCountersOf(ctx, campaign.ID)
	if err != nil {
		return CampaignReport{}, err
	}
	providers, err := sess.deps.Store.ProviderBreakdown(ctx, campaign.ID)
	if err != nil {
		return CampaignReport{}, err
	}
	remaining := 0
	// Only a send capped to the most engaged has a next step to offer (upstream countRemaining).
	if campaign.Status == model.CampaignSent && campaign.AudienceLimit > 0 {
		active, err := sess.deps.Store.CountActiveContacts(ctx, campaign.NewsletterID)
		if err != nil {
			return CampaignReport{}, err
		}
		if reached := counters.Recipients + lastChance.Recipients; active > reached {
			remaining = active - reached
		}
	}
	rows := make([]providerView, 0, len(providers))
	for _, provider := range providers {
		rows = append(rows, viewProvider(provider))
	}
	// A campaign that is still sending has unfinished messages by design, and one that was queued but
	// never started has all of them; only the finished states should show nothing.
	stranded := 0
	if !campaign.Status.Queueable() && campaign.Status != model.CampaignSending {
		if stranded, err = sess.deps.Store.CountUnfinishedCampaignMessages(ctx, campaign.ID); err != nil {
			return CampaignReport{}, err
		}
	}
	return CampaignReport{
		Campaign: viewCampaign(campaign), Stats: viewCounters(counters),
		LastChance: viewLastChance(lastChance), Providers: rows, Remaining: remaining,
		Stranded: stranded,
	}, nil
}

type getStatsIn struct {
	Newsletter string `json:"newsletter,omitempty" jsonschema:"newsletter id or slug (default: every newsletter)"`
	TZ         string `json:"tz,omitempty" jsonschema:"IANA time zone for the days and hours (default UTC)"`
}

type getStatsOut struct {
	Reports []statsquery.Report `json:"reports"`
}

func getStats(ctx context.Context, sess *session, _ store.Credential, in getStatsIn) (getStatsOut, error) {
	zone := stats.ResolveTimeZone(strings.TrimSpace(in.TZ))
	newsletters, err := newslettersFor(ctx, sess.deps.Store, in.Newsletter)
	if err != nil {
		return getStatsOut{}, err
	}
	reports := make([]statsquery.Report, 0, len(newsletters))
	for _, newsletter := range newsletters {
		report, err := statsquery.Build(ctx, sess.deps.Store, newsletter, zone)
		if err != nil {
			return getStatsOut{}, err
		}
		reports = append(reports, report)
	}
	return getStatsOut{Reports: reports}, nil
}

type listContactsIn struct {
	Newsletter string `json:"newsletter" jsonschema:"newsletter id or slug"`
	Status     string `json:"status,omitempty" jsonschema:"active, pending, unsubscribed, bounced or complained"`
	Search     string `json:"search,omitempty" jsonschema:"only addresses containing this text"`
	PageArgs
}

type listContactsOut struct {
	page[contactView]
}

func listContacts(ctx context.Context, sess *session, _ store.Credential, in listContactsIn) (listContactsOut, error) {
	limit, offset, err := in.resolve()
	if err != nil {
		return listContactsOut{}, err
	}
	newsletter, err := newsletterByRef(ctx, sess.deps.Store, in.Newsletter)
	if err != nil {
		return listContactsOut{}, err
	}
	filter := store.ContactFilter{
		NewsletterID: newsletter.ID, EmailPrefix: strings.TrimSpace(in.Search),
		Limit: limit + 1, Offset: offset,
	}
	if status := strings.TrimSpace(in.Status); status != "" {
		filter.Status = model.ContactStatus(status)
		if !knownContactStatus(filter.Status) {
			return listContactsOut{}, fmt.Errorf("%q is not a contact status", status)
		}
	}
	contacts, err := sess.deps.Store.Contacts(ctx, filter)
	if err != nil {
		return listContactsOut{}, err
	}
	return listContactsOut{paginate(contactViews(contacts), limit, offset)}, nil
}

// CountAudience is count_audience: the number an agent has to have before it decides to send.
//
// It is its own tool rather than a field of the campaign because it is the answer to "how many people
// does this go to", and a caller must be able to get it without sending anything (design §9).
type CountAudience struct {
	Newsletter string `json:"newsletter"`
	Active     int    `json:"active"`
	Pending    int    `json:"pending"`
	// Suppressed is the deployment's suppression list, which is not per newsletter: an address on it
	// is skipped wherever it appears. It is here because it is the subtraction an agent needs.
	Suppressed int `json:"suppressed"`
	// WouldSend is the next step of this campaign: the whole audience, or the part a stepped send has
	// not reached yet.
	WouldSend int `json:"wouldSend"`
}

type countAudienceIn struct {
	Newsletter string `json:"newsletter" jsonschema:"newsletter id or slug"`
	Campaign   string `json:"campaign,omitempty" jsonschema:"a campaign, to also answer how many it would send to"`
}

func countAudience(ctx context.Context, sess *session, _ store.Credential, in countAudienceIn) (CountAudience, error) {
	newsletter, err := newsletterByRef(ctx, sess.deps.Store, in.Newsletter)
	if err != nil {
		return CountAudience{}, err
	}
	active, err := sess.deps.Store.CountActiveContacts(ctx, newsletter.ID)
	if err != nil {
		return CountAudience{}, err
	}
	out := CountAudience{Newsletter: newsletter.Slug, Active: active, WouldSend: active}
	if out.Pending, err = sess.deps.Store.CountContacts(ctx, store.ContactFilter{
		NewsletterID: newsletter.ID, Status: model.ContactPending,
	}); err != nil {
		return CountAudience{}, err
	}
	if out.Suppressed, err = sess.deps.Store.CountSuppressions(ctx); err != nil {
		return CountAudience{}, err
	}
	if strings.TrimSpace(in.Campaign) == "" {
		return out, nil
	}
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return CountAudience{}, err
	}
	limit := campaign.AudienceLimit
	if limit == 0 {
		return out, nil
	}
	out.WouldSend = limit
	if campaign.Status == model.CampaignSent || campaign.Status == model.CampaignSending {
		reached, err := sess.deps.Store.ReachedContacts(ctx, campaign.ID)
		if err != nil {
			return CountAudience{}, err
		}
		out.WouldSend = limit - len(reached)
		if out.WouldSend < 0 {
			out.WouldSend = 0
		}
	}
	return out, nil
}

// CampaignCheck is check_campaign: the same checklist the send runs, so the two can never disagree
// about whether a campaign may go out.
type CampaignCheck struct {
	Campaign string         `json:"campaign"`
	Audience int            `json:"audience"`
	Checks   []checks.Check `json:"checks"`
	Blocked  *checks.Check  `json:"blocked,omitempty"`
	// Sending is the rate and the account's remaining allowance, and Estimated is how long this
	// audience takes at it (D17, D18). An agent asking "may I send this" also wants to know whether
	// the answer is "in a minute" or "tomorrow".
	Sending   mailer.SendingRate `json:"sending"`
	Estimated string             `json:"estimated"`
}

func checkCampaign(ctx context.Context, sess *session, _ store.Credential, in campaignRefIn) (CampaignCheck, error) {
	campaign, err := campaignByRef(ctx, sess.deps.Store, in.Campaign)
	if err != nil {
		return CampaignCheck{}, err
	}
	report, audience, err := preSendChecks(ctx, sess.deps, campaign)
	if err != nil {
		return CampaignCheck{}, err
	}
	rate := sess.deps.Mailer.SendingRate()
	out := CampaignCheck{
		Campaign: campaign.ID, Audience: audience, Checks: report,
		Sending: rate, Estimated: rate.Estimate(audience).Round(time.Second).String(),
	}
	if blocked, bad := checks.Blocked(report); bad {
		out.Blocked = &blocked
	}
	return out, nil
}

type domainRow struct {
	Domain   string         `json:"domain"`
	Verified bool           `json:"verifiedForSend"`
	DKIM     string         `json:"dkimStatus"`
	MailFrom string         `json:"mailFromDomain,omitempty"`
	Status   string         `json:"mailFromStatus,omitempty"`
	Records  []mailerRecord `json:"dnsRecords,omitempty"`
}

type mailerRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type listDomainsOut struct {
	Domains []domainRow `json:"domains"`
}

// listDomains answers "may this deployment send from here": the identities SES knows about, with the
// DNS records still to publish. Against the fake SES it is the configured stand-in list.
func listDomains(ctx context.Context, sess *session, _ store.Credential, _ struct{}) (listDomainsOut, error) {
	identities, err := sess.deps.Identities.Identities(ctx)
	if err != nil {
		return listDomainsOut{}, err
	}
	out := make([]domainRow, 0, len(identities))
	for _, identity := range identities {
		row := domainRow{
			Domain: identity.Domain, Verified: identity.VerifiedForSend, DKIM: identity.DKIMStatus,
			MailFrom: identity.MailFromDomain, Status: identity.MailFromStatus,
		}
		for _, record := range identity.Records {
			row.Records = append(row.Records, mailerRecord{Type: record.Type, Name: record.Name, Value: record.Value})
		}
		out = append(out, row)
	}
	return listDomainsOut{Domains: out}, nil
}

type listSuppressionsIn struct {
	PageArgs
}

type suppressionRow struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

type listSuppressionsOut struct {
	page[suppressionRow]
}

func listSuppressions(ctx context.Context, sess *session, _ store.Credential, in listSuppressionsIn) (listSuppressionsOut, error) {
	limit, offset, err := in.resolve()
	if err != nil {
		return listSuppressionsOut{}, err
	}
	// The page is cut in SQL, with the same one-extra-row rule the other lists use: the list grows with
	// every bounce and complaint, and reading it whole to show twenty rows is work that grows with it
	// (phase 9 #45).
	page, err := sess.deps.Store.SuppressionsPage(ctx, limit+1, offset)
	if err != nil {
		return listSuppressionsOut{}, err
	}
	rows := make([]suppressionRow, 0, len(page))
	for _, suppression := range page {
		rows = append(rows, suppressionRow{
			Email: suppression.Email, Reason: string(suppression.Reason), Detail: suppression.Detail,
		})
	}
	return listSuppressionsOut{paginate(rows, limit, offset)}, nil
}

type getJobIn struct {
	JobID string `json:"job_id" jsonschema:"the job id a send answered with"`
}

// Job is get_job: what became of a queued send.
type Job struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	RunAt     string `json:"runAt"`
	LastError string `json:"lastError,omitempty"`
	CreatedAt string `json:"createdAt"`
}

func getJob(ctx context.Context, sess *session, _ store.Credential, in getJobIn) (Job, error) {
	id := strings.TrimSpace(in.JobID)
	if id == "" {
		return Job{}, errors.New("job_id is required")
	}
	job, err := sess.deps.Jobs.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return Job{
		ID: job.ID, Kind: job.Kind, State: string(job.State), Attempts: job.Attempts,
		RunAt: job.RunAt.UTC().Format(time.RFC3339), LastError: job.LastError,
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339),
	}, nil
}

type listAuditIn struct {
	Action string `json:"action,omitempty" jsonschema:"only this action, e.g. campaign.send"`
	Actor  string `json:"actor,omitempty" jsonschema:"only this actor kind: cli, api or mcp"`
	PageArgs
}

type auditRow struct {
	ID           string `json:"id"`
	ActorName    string `json:"actor"`
	Action       string `json:"action"`
	Result       string `json:"result"`
	ParamsDigest string `json:"paramsDigest,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type listAuditOut struct {
	page[auditRow]
}

func listAudit(ctx context.Context, sess *session, _ store.Credential, in listAuditIn) (listAuditOut, error) {
	limit, offset, err := in.resolve()
	if err != nil {
		return listAuditOut{}, err
	}
	entries, err := sess.deps.Store.AuditLog(ctx, store.AuditFilter{
		Action: strings.TrimSpace(in.Action), Actor: strings.TrimSpace(in.Actor),
		Limit: limit + 1, Offset: offset,
	})
	if err != nil {
		return listAuditOut{}, err
	}
	rows := make([]auditRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, auditRow{
			ID: entry.ID, ActorName: entry.ActorName, Action: entry.Action, Result: entry.Result,
			ParamsDigest: entry.ParamsDigest, CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return listAuditOut{paginate(rows, limit, offset)}, nil
}

// --- shared lookups -------------------------------------------------------

// slice is the tail of rows from offset, for the two lists whose store method returns everything.
func slice[T any](rows []T, offset int) []T {
	return rows[sliceFrom(len(rows), offset):]
}

func views(newsletters []model.Newsletter) []newsletterView {
	out := make([]newsletterView, 0, len(newsletters))
	for _, newsletter := range newsletters {
		out = append(out, viewNewsletter(newsletter))
	}
	return out
}

func campaignViews(campaigns []model.Campaign) []campaignView {
	out := make([]campaignView, 0, len(campaigns))
	for _, campaign := range campaigns {
		out = append(out, viewCampaign(campaign))
	}
	return out
}

func contactViews(contacts []model.Contact) []contactView {
	out := make([]contactView, 0, len(contacts))
	for _, contact := range contacts {
		out = append(out, viewContact(contact))
	}
	return out
}

func sliceFrom(length, offset int) int {
	if offset > length {
		return length
	}
	return offset
}

// newsletterByRef finds a newsletter by id or by slug, the two names an operator uses.
func newsletterByRef(ctx context.Context, db *store.DB, ref string) (model.Newsletter, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Newsletter{}, errors.New("name a newsletter by id or slug")
	}
	if ids.Valid(ref) {
		return db.Newsletter(ctx, ref)
	}
	return db.NewsletterBySlug(ctx, strings.ToLower(ref))
}

func newslettersFor(ctx context.Context, db *store.DB, ref string) ([]model.Newsletter, error) {
	if strings.TrimSpace(ref) == "" {
		return db.Newsletters(ctx)
	}
	newsletter, err := newsletterByRef(ctx, db, ref)
	if err != nil {
		return nil, err
	}
	return []model.Newsletter{newsletter}, nil
}

// campaignByRef finds the campaign the caller meant: an id when one is given, otherwise the draft
// with that subject. It is the CLI's rule, so `get_campaign{subject}` means the same thing here.
func campaignByRef(ctx context.Context, db *store.DB, ref string) (model.Campaign, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Campaign{}, errors.New("name a campaign by id or by subject")
	}
	if ids.Valid(ref) {
		return db.Campaign(ctx, ref)
	}
	campaign, err := db.CampaignBySubject(ctx, ref)
	if errors.Is(err, store.ErrNotFound) {
		return model.Campaign{}, fmt.Errorf("no campaign called %q: %w", ref, store.ErrNotFound)
	}
	return campaign, err
}

// preSendChecks builds the checklist for a campaign: the same inputs the send path uses, so
// check_campaign and send_campaign can never disagree about whether a campaign is sendable.
func preSendChecks(ctx context.Context, deps Deps, campaign model.Campaign) ([]checks.Check, int, error) {
	newsletter, err := deps.Store.Newsletter(ctx, campaign.NewsletterID)
	if err != nil {
		return nil, 0, err
	}
	verified, err := deps.Identities.Identities(ctx)
	if err != nil {
		return nil, 0, err
	}
	audience, err := deps.Store.CountActiveContacts(ctx, newsletter.ID)
	if err != nil {
		return nil, 0, err
	}
	report := checks.Run(checks.Input{
		Campaign: campaign, Newsletter: newsletter,
		Rendered: render.Email(render.EmailInput{
			Format: campaign.Format, Body: campaign.Body, Subject: campaign.Subject,
			PreviewText: campaign.PreviewText, Newsletter: newsletter,
		}),
		SendingOn:      deps.Config.SendingOn,
		Limits:         deps.Config.Limits,
		DomainVerified: rules.VerifiedFromDomain(mailer.VerifiedDomains(verified), newsletter.FromEmail),
		Audience:       audience,
	})
	return report, audience, nil
}

func knownContactStatus(status model.ContactStatus) bool {
	switch status {
	case model.ContactActive, model.ContactPending, model.ContactUnsubscribed,
		model.ContactBounced, model.ContactComplained:
		return true
	default:
		return false
	}
}

// normalizeEmail is the one place an email argument is cleaned, so what a tool stores and what it
// looks up are the same string.
func normalizeEmail(value string) string { return validate.NormalizeEmail(value) }
