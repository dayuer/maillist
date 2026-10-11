package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"mailcheap/internal/core/model"
)

// InsertChunkSize keeps a bulk insert short: one transaction per 1000 rows, never one big one
// (design §5.2).
const InsertChunkSize = 1000

// messageColumns is the SELECT list every message query shares, in scanMessage order.
const messageColumns = `id, kind, campaign_id, newsletter_id, contact_id, email, provider, status,
    ses_message_id, error, claimed_at, sent_at, delivered_at, opened_at, clicked_at, bounced_at,
    complained_at, created_at`

// InsertMessages adds messages in chunks, ignoring rows that already exist (one email per contact
// per campaign). It returns how many rows were actually inserted.
func (db *DB) InsertMessages(ctx context.Context, messages []model.Message) (int, error) {
	inserted := 0
	for start := 0; start < len(messages); start += InsertChunkSize {
		end := min(start+InsertChunkSize, len(messages))
		chunk := messages[start:end]
		err := db.Write(ctx, func(tx *sql.Tx) error {
			var builder strings.Builder
			builder.WriteString(`INSERT OR IGNORE INTO messages (` + messageColumns + `) VALUES `)
			args := make([]any, 0, len(chunk)*18)
			for i, m := range chunk {
				if i > 0 {
					builder.WriteString(",")
				}
				builder.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
				args = append(args,
					m.ID, string(m.Kind), m.CampaignID, m.NewsletterID, m.ContactID, m.Email,
					m.Provider, string(m.Status), m.SESMessageID, m.Error,
					formatTimePtr(m.ClaimedAt), formatTimePtr(m.SentAt), formatTimePtr(m.DeliveredAt),
					formatTimePtr(m.OpenedAt), formatTimePtr(m.ClickedAt), formatTimePtr(m.BouncedAt),
					formatTimePtr(m.ComplainedAt), formatTime(m.CreatedAt))
			}
			result, err := tx.ExecContext(ctx, builder.String(), args...)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			inserted += int(affected)
			return nil
		})
		if err != nil {
			return inserted, fmt.Errorf("insert messages %d-%d: %w", start, end, err)
		}
	}
	return inserted, nil
}

// ClaimMessages atomically moves up to n pending messages to claimed and returns them. The claim is
// what makes sending at most once (D14): SES is only called after it commits. The order within a
// batch is the database's, not the caller's.
func (db *DB) ClaimMessages(ctx context.Context, n int) ([]model.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	return db.claim(ctx, "SELECT id FROM messages WHERE status = ? ORDER BY created_at, id LIMIT ?",
		string(model.MessagePending), n)
}

// ClaimMessagesByID claims exactly the messages named, skipping any that are not pending.
func (db *DB) ClaimMessagesByID(ctx context.Context, ids []string) ([]model.Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{string(model.MessagePending)}
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := "SELECT id FROM messages WHERE status = ? AND id IN (" + //nolint:gosec // G202: placeholders, not values
		strings.Join(placeholders, ",") + ")"
	return db.claim(ctx, query, args...)
}

// claim runs the selection and the status change in one transaction, so two senders can never take
// the same row.
func (db *DB) claim(ctx context.Context, selection string, args ...any) ([]model.Message, error) {
	var claimed []model.Message
	now := formatTime(time.Now())
	err := db.Write(ctx, func(tx *sql.Tx) error {
		claimed = claimed[:0]
		// selection and messageColumns are this package's own constants; every value is bound.
		query := `UPDATE messages SET status = ?, claimed_at = ?` + //nolint:gosec // G202: package constants, not values
			` WHERE id IN (` + selection + `) RETURNING ` + messageColumns
		rows, err := tx.QueryContext(ctx, query,
			append([]any{string(model.MessageClaimed), now}, args...)...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			message, err := scanMessage(rows)
			if err != nil {
				return err
			}
			claimed = append(claimed, message)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func scanMessage(row interface{ Scan(...any) error }) (model.Message, error) {
	var (
		m          model.Message
		kind       string
		status     string
		claimedAt  sql.NullString
		sentAt     sql.NullString
		delivered  sql.NullString
		openedAt   sql.NullString
		clickedAt  sql.NullString
		bouncedAt  sql.NullString
		complained sql.NullString
		createdAt  string
	)
	err := row.Scan(&m.ID, &kind, &m.CampaignID, &m.NewsletterID, &m.ContactID, &m.Email, &m.Provider,
		&status, &m.SESMessageID, &m.Error, &claimedAt, &sentAt, &delivered, &openedAt, &clickedAt,
		&bouncedAt, &complained, &createdAt)
	if err != nil {
		return model.Message{}, err
	}
	m.Kind = model.MessageKind(kind)
	m.Status = model.MessageStatus(status)
	for _, field := range []struct {
		source sql.NullString
		target **time.Time
	}{
		{claimedAt, &m.ClaimedAt}, {sentAt, &m.SentAt}, {delivered, &m.DeliveredAt},
		{openedAt, &m.OpenedAt}, {clickedAt, &m.ClickedAt}, {bouncedAt, &m.BouncedAt},
		{complained, &m.ComplainedAt},
	} {
		parsed, err := parseTimePtr(field.source)
		if err != nil {
			return model.Message{}, err
		}
		*field.target = parsed
	}
	if m.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Message{}, err
	}
	return m, nil
}

// Message loads one by id.
func (db *DB) Message(ctx context.Context, id string) (model.Message, error) {
	m, err := scanMessage(db.sql.QueryRowContext(ctx,
		"SELECT "+messageColumns+" FROM messages WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Message{}, fmt.Errorf("message %s: %w", id, ErrNotFound)
	}
	return m, err
}

// MarkMessageSent writes back a message SES accepted.
func (db *DB) MarkMessageSent(ctx context.Context, id, sesMessageID string, sentAt time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ?, ses_message_id = ?, sent_at = ?, error = '' WHERE id = ?",
			string(model.MessageSent), sesMessageID, formatTime(sentAt), id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "message", id)
	})
}

// MarkMessageFailed records a send that will not be retried.
func (db *DB) MarkMessageFailed(ctx context.Context, id, message string) error {
	return db.setMessageStatus(ctx, id, model.MessageFailed, truncate(message, 500))
}

// MarkMessageSuppressed records a message whose contact unsubscribed, bounced or complained after
// the audience was chosen.
func (db *DB) MarkMessageSuppressed(ctx context.Context, id string) error {
	return db.setMessageStatus(ctx, id, model.MessageSuppressed, "")
}

func (db *DB) setMessageStatus(ctx context.Context, id string, status model.MessageStatus, message string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ?, error = ? WHERE id = ?", string(status), message, id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "message", id)
	})
}

// ReleaseMessage puts a claimed message back in the queue after a retryable failure.
func (db *DB) ReleaseMessage(ctx context.Context, id string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ?, claimed_at = NULL WHERE id = ? AND status = ?",
			string(model.MessagePending), id, string(model.MessageClaimed))
		return err
	})
}

// RecoverClaimed turns rows left claimed by a crash into unknown: they may or may not have gone out,
// and are never sent again (D14). It runs once at startup.
func (db *DB) RecoverClaimed(ctx context.Context) (int, error) {
	recovered := 0
	err := db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE messages SET status = ? WHERE status = ?",
			string(model.MessageUnknown), string(model.MessageClaimed))
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		recovered = int(affected)
		return nil
	})
	return recovered, err
}

// CountCampaignMessages is the recipients/last-chance split of a campaign.
func (db *DB) CountCampaignMessages(ctx context.Context, campaignID string) (recipients, lastChance int, err error) {
	err = db.sql.QueryRowContext(ctx, `SELECT
            coalesce(sum(kind = ?), 0),
            coalesce(sum(kind = ?), 0)
        FROM messages WHERE campaign_id = ?`,
		string(model.KindCampaign), string(model.KindLastChance), campaignID).Scan(&recipients, &lastChance)
	return recipients, lastChance, err
}

// CountPendingCampaignMessages is how many of a campaign's messages are still waiting to go out.
func (db *DB) CountPendingCampaignMessages(ctx context.Context, campaignID string) (int, error) {
	var pending int
	err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM messages
        WHERE campaign_id = ? AND kind IN (?, ?) AND status = ?`,
		campaignID, string(model.KindCampaign), string(model.KindLastChance),
		string(model.MessagePending)).Scan(&pending)
	return pending, err
}

// ReachedContacts is who this campaign already has a message for, so a stepped send skips them.
func (db *DB) ReachedContacts(ctx context.Context, campaignID string) (map[string]model.MessageKind, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT contact_id, kind FROM messages WHERE campaign_id = ?", campaignID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	reached := map[string]model.MessageKind{}
	for rows.Next() {
		var contactID, kind string
		if err := rows.Scan(&contactID, &kind); err != nil {
			return nil, err
		}
		reached[contactID] = model.MessageKind(kind)
	}
	return reached, rows.Err()
}

// CountContactMessages is how many messages of one kind a contact already has; it is what caps the
// confirmation emails at rules.MaxConfirmationsPerContact.
func (db *DB) CountContactMessages(ctx context.Context, contactID string, kind model.MessageKind) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		"SELECT count(*) FROM messages WHERE contact_id = ? AND kind = ?",
		contactID, string(kind)).Scan(&count)
	return count, err
}

// MessageContext is everything the engine looks up around one claimed message.
type MessageContext struct {
	// Campaign is nil for welcome and confirmation emails.
	Campaign   *model.Campaign
	Newsletter model.Newsletter
	Contact    model.Contact
}

// LoadMessageContext fetches the newsletter, contact and (for a campaign message) the campaign.
func (db *DB) LoadMessageContext(ctx context.Context, m model.Message) (MessageContext, error) {
	var loaded MessageContext
	newsletter, err := db.Newsletter(ctx, m.NewsletterID)
	if err != nil {
		return loaded, err
	}
	loaded.Newsletter = newsletter
	if m.CampaignID != "" {
		campaign, err := db.Campaign(ctx, m.CampaignID)
		if err != nil {
			return loaded, err
		}
		loaded.Campaign = &campaign
	}
	if m.ContactID != "" {
		contact, err := db.Contact(ctx, m.ContactID)
		if err != nil {
			return loaded, err
		}
		loaded.Contact = contact
	}
	return loaded, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
