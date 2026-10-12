// Package events applies what SNS and the tracking endpoints report to the database. It is the port
// of upstream's lib/ses-events.js: core/sesmap decides what an event means, this decides how to
// write it down.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"nibwire/internal/alerts"
	"nibwire/internal/core/model"
	"nibwire/internal/core/render"
	"nibwire/internal/core/rules"
	"nibwire/internal/core/sesmap"
	"nibwire/internal/store"
)

// recordedTypes are the events worth a row: every send produces a delivery, and storing those would
// fill the database within months (upstream RECORDED_EVENT_TYPES).
var recordedTypes = map[sesmap.EventType]bool{
	sesmap.EventBounce: true, sesmap.EventComplaint: true, sesmap.EventReject: true,
	sesmap.EventDeliveryDelay: true, sesmap.EventRenderingFailure: true,
}

// Handler applies events.
type Handler struct {
	store *store.DB
	log   *slog.Logger
	// alerts tells the operator about a spam complaint, which is the one SES event that is somebody's
	// opinion of the newsletter rather than a fact about the delivery.
	alerts *alerts.Notifier
}

// New returns a Handler over db.
func New(db *store.DB, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{store: db, log: log}
}

// WithAlerts gives the handler somewhere to send its alerts.
func (h *Handler) WithAlerts(notifier *alerts.Notifier) *Handler {
	h.alerts = notifier
	return h
}

// Applied is what one event did.
type Applied struct {
	Handled   bool
	MessageID string
}

// ApplyEvent maps and applies one raw SES event. A malformed or unknown event is not an error: the
// webhook answers 200 and moves on.
func (h *Handler) ApplyEvent(ctx context.Context, raw json.RawMessage, now time.Time) (Applied, error) {
	plan, ok := sesmap.EventPlan(raw, now)
	if !ok {
		return Applied{}, nil
	}
	message, found, err := h.findMessage(ctx, plan)
	if err != nil {
		return Applied{}, err
	}
	if found {
		if err := h.apply(ctx, message, plan); err != nil {
			return Applied{}, err
		}
	}
	if len(plan.Suppress) > 0 {
		if err := h.suppress(ctx, plan); err != nil {
			return Applied{}, err
		}
	}
	if recordedTypes[plan.Type] {
		email := plan.Email
		if email == "" && found {
			email = message.Email
		}
		event := store.Event{Type: string(plan.Type), SESMessageID: plan.SESMessageID, Email: email,
			OccurredAt: plan.OccurredAt, CreatedAt: now, Data: plan.Store}
		if found {
			event.MessageID, event.CampaignID = message.ID, message.CampaignID
		}
		if err := h.store.InsertEvent(ctx, event); err != nil {
			return Applied{}, err
		}
	}
	applied := Applied{Handled: true}
	if found {
		applied.MessageID = message.ID
	}
	return applied, nil
}

// ApplyTracking applies an open or a click from Nibwire's own pixel and links.
func (h *Handler) ApplyTracking(ctx context.Context, messageID string, eventType sesmap.EventType,
	userAgent string, now time.Time) (bool, error) {
	message, err := h.store.Message(ctx, messageID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if eventType == sesmap.EventClick && render.IsLikelyScanner(userAgent, message.SentAt, now) {
		return false, nil
	}
	plan := sesmap.TrackingPlan(eventType, now)
	if err := h.apply(ctx, message, plan); err != nil {
		return false, err
	}
	if plan.SuppressAs == model.SuppressComplaint && len(plan.Suppress) > 0 {
		h.alerts.Send(ctx, alerts.Alert{
			Key: "complaints", Title: "Spam complaint",
			Detail: strings.Join(plan.Suppress, ", ") + " marked an email as spam. They are suppressed.",
		})
	}
	return true, nil
}

// findMessage resolves the message an event names: by SES's own id first, then by the message_id
// tag, which covers an event that arrives before the send was written back.
func (h *Handler) findMessage(ctx context.Context, plan sesmap.Plan) (model.Message, bool, error) {
	if plan.SESMessageID != "" {
		message, err := h.store.MessageBySESID(ctx, plan.SESMessageID)
		if err == nil {
			return message, true, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return model.Message{}, false, err
		}
	}
	if plan.MessageID == "" {
		return model.Message{}, false, nil
	}
	message, err := h.store.Message(ctx, plan.MessageID)
	if errors.Is(err, store.ErrNotFound) {
		return model.Message{}, false, nil
	}
	if err != nil {
		return model.Message{}, false, err
	}
	// A message id and an SES id that disagree mean the event belongs to another message.
	if message.SESMessageID != "" && plan.SESMessageID != "" && message.SESMessageID != plan.SESMessageID {
		return model.Message{}, false, nil
	}
	if message.SESMessageID == "" && plan.SESMessageID != "" {
		if err := h.store.AdoptSESMessageID(ctx, message.ID, plan.SESMessageID); err != nil {
			return model.Message{}, false, err
		}
	}
	return message, true, nil
}

// apply writes one plan's message, campaign and contact updates. Every counter moves only on the
// first transition, so an SNS redelivery changes nothing.
//
// The increments are collected as a set, not a tally, exactly as upstream's campaignInc object is:
// a hard bounce reports its timestamp and its status, and both name the same counter, which must
// still count once.
func (h *Handler) apply(ctx context.Context, message model.Message, plan sesmap.Plan) error {
	campaignInc := map[string]int{}
	contactInc := map[string]int{}

	for _, first := range plan.Firsts {
		wrote, err := h.store.FirstTimestamp(ctx, message.ID, first.Field, plan.OccurredAt)
		if err != nil {
			return err
		}
		if !wrote {
			continue
		}
		if first.Stat != "" {
			campaignInc[first.Stat] = 1
		}
		if first.ContactStat != "" {
			contactInc[first.ContactStat] = 1
		}
	}

	if plan.NewStatus != "" {
		moved, err := h.store.SetMessageStatusFrom(ctx, message.ID, plan.NewStatus, plan.StatusFrom)
		if err != nil {
			return err
		}
		// The stat a status bumps is the status itself (upstream's status.stat).
		if moved {
			campaignInc[string(plan.NewStatus)] = 1
		}
	}

	counters := store.CampaignCounters{
		Delivered: campaignInc["delivered"], Bounced: campaignInc["bounced"],
		Complained: campaignInc["complained"], Opened: campaignInc["opened"],
		Clicked: campaignInc["clicked"], Failed: campaignInc["failed"],
	}
	countersMoved := len(campaignInc) > 0
	if plan.Error != "" {
		if err := h.store.SetMessageError(ctx, message.ID, plan.Error); err != nil {
			return err
		}
	}

	if rules.IsCampaignKind(message.Kind) && message.CampaignID != "" && countersMoved {
		if message.Kind == model.KindLastChance {
			if err := h.store.AddLastChanceStats(ctx, message.CampaignID, store.LastChanceCounters{
				Delivered: counters.Delivered, Bounced: counters.Bounced,
				Complained: counters.Complained, Failed: counters.Failed,
			}); err != nil {
				return err
			}
		} else if err := h.store.AddCampaignStats(ctx, message.CampaignID, counters); err != nil {
			return err
		}
	}

	if message.ContactID != "" && (plan.Engagement != nil || len(contactInc) > 0) {
		if err := h.store.RecordEngagement(ctx, message.ContactID, plan.Engagement, plan.OccurredAt); err != nil {
			return err
		}
		if len(contactInc) > 0 {
			err := h.store.AddContactStats(ctx, message.ContactID, 0, contactInc["opens"], contactInc["clicks"])
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// suppress adds the addresses to the global suppression list and moves their contacts. A complaint
// outranks an earlier bounce or manual entry (store.AddSuppression decides that).
func (h *Handler) suppress(ctx context.Context, plan sesmap.Plan) error {
	for _, email := range plan.Suppress {
		if err := h.store.AddSuppression(ctx, email, plan.SuppressAs, plan.SuppressDetail); err != nil {
			return err
		}
		if plan.ContactStatus == "" || len(plan.ContactStatusFrom) == 0 {
			continue
		}
		if err := h.store.SuppressContacts(ctx, email, plan.ContactStatus, plan.ContactStatusFrom); err != nil {
			return err
		}
	}
	return nil
}
