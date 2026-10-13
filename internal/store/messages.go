package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"nibwire/internal/core/model"
)

// InsertChunkSize bounds one INSERT statement, not a transaction. A step's messages go in with the
// reservation that pays for them, in one transaction, however many statements that takes: writing
// them in separate transactions is a leak with no way to notice it (see ReserveAndInsert).
const InsertChunkSize = 1000

// messageColumns is the SELECT list every message query shares, in scanMessage order.
const messageColumns = `id, kind, campaign_id, newsletter_id, contact_id, email, provider, status,
    provider_message_id, error, claimed_at, sent_at, delivered_at, opened_at, clicked_at, bounced_at,
    complained_at, created_at, channel`

// channelOf is the channel a message is written with: the default channel when none was chosen.
func channelOf(m model.Message) string {
	if m.Channel == "" {
		return model.DefaultChannel
	}
	return m.Channel
}

// markReservedDayTx records, in the same transaction as the reservation, which day a campaign's
// audience was reserved for.
//
// It is what lets a later resume tell "already paid for today" from "not paid for today". Without it
// the only two options are charging twice on the same day or not charging at all on a later one.
func markReservedDayTx(ctx context.Context, tx *sql.Tx, day string, messages []model.Message) error {
	seen := map[string]bool{}
	for _, message := range messages {
		if message.CampaignID == "" || seen[message.CampaignID] {
			continue
		}
		seen[message.CampaignID] = true
		if _, err := tx.ExecContext(ctx,
			"UPDATE campaigns SET reserved_day = ? WHERE id = ?", day, message.CampaignID); err != nil {
			return err
		}
	}
	return nil
}

// ReserveForResume charges today's ledger for the messages a campaign already has, and records that
// today is the day it did, in one transaction.
//
// This is the cross-day resume. The audience was reserved on the day the campaign was queued (D15),
// so today's ledger has never seen these messages; resuming without charging them would let the
// day's total — the global one and the credential's — go over MAX_SENDS_PER_DAY, which is what a
// managed deployment splits an account's quota with. Nothing is written to the messages table: they
// exist. fits=false means today cannot take it, and the whole step is refused rather than partly
// sent (the caller says how much of the day is left).
func (db *DB) ReserveForResume(ctx context.Context, day, credentialID, campaignID string, charge Charge,
	limits Limits) (bool, error) {
	fits := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		fits = false
		ok, err := reserveTx(ctx, tx, day, credentialID, charge, limits)
		if err != nil || !ok {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE campaigns SET reserved_day = ? WHERE id = ?", day, campaignID); err != nil {
			return err
		}
		fits = true
		return nil
	})
	return fits, err
}

// ChargeOfCampaign is what a campaign's unfinished messages would cost today, per channel. It is the
// charge a cross-day resume reserves: the audience was decided when the campaign was queued, and
// this is how much of it still has to go out, and through what.
func (db *DB) ChargeOfCampaign(ctx context.Context, campaignID string) (Charge, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT channel, count(*) FROM messages
        WHERE campaign_id = ? AND kind IN (?, ?) AND status IN (?, ?) GROUP BY channel`,
		campaignID, string(model.KindCampaign), string(model.KindLastChance),
		string(model.MessagePending), string(model.MessageClaimed))
	if err != nil {
		return nil, fmt.Errorf("count unfinished campaign messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := Charge{}
	for rows.Next() {
		var (
			channel string
			n       int
		)
		if err := rows.Scan(&channel, &n); err != nil {
			return nil, fmt.Errorf("count unfinished campaign messages: %w", err)
		}
		out[channel] += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count unfinished campaign messages: %w", err)
	}
	return out, nil
}

// InsertMessages adds messages in chunks, ignoring rows that already exist (one email per contact
// per campaign). It returns how many rows were actually inserted.
func (db *DB) InsertMessages(ctx context.Context, messages []model.Message) (int, error) {
	inserted := 0
	for start := 0; start < len(messages); start += InsertChunkSize {
		end := min(start+InsertChunkSize, len(messages))
		chunk := messages[start:end]
		written, err := db.insertChunk(ctx, chunk)
		inserted += written.Total()
		if err != nil {
			return inserted, fmt.Errorf("insert messages %d-%d: %w", start, end, err)
		}
	}
	return inserted, nil
}

// insertChunk writes one chunk in a transaction of its own and reports what it wrote.
func (db *DB) insertChunk(ctx context.Context, chunk []model.Message) (Charge, error) {
	var written Charge
	err := db.Write(ctx, func(tx *sql.Tx) error {
		count, err := insertMessagesTx(ctx, tx, chunk)
		written = count
		return err
	})
	return written, err
}

// insertMessagesTx is one chunk of InsertMessages inside a caller's transaction. It returns the rows
// that were actually new, per channel: what the day is charged for is what will be sent, and a row
// that already existed is neither.
func insertMessagesTx(ctx context.Context, tx *sql.Tx, chunk []model.Message) (Charge, error) {
	if len(chunk) == 0 {
		return Charge{}, nil
	}
	var builder strings.Builder
	builder.WriteString(`INSERT OR IGNORE INTO messages (` + messageColumns + `) VALUES `)
	args := make([]any, 0, len(chunk)*19)
	for i, m := range chunk {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			m.ID, string(m.Kind), m.CampaignID, m.NewsletterID, m.ContactID, m.Email,
			m.Provider, string(m.Status), m.ProviderMessageID, m.Error,
			formatTimePtr(m.ClaimedAt), formatTimePtr(m.SentAt), formatTimePtr(m.DeliveredAt),
			formatTimePtr(m.OpenedAt), formatTimePtr(m.ClickedAt), formatTimePtr(m.BouncedAt),
			formatTimePtr(m.ComplainedAt), formatTime(m.CreatedAt), channelOf(m))
	}
	// RETURNING is what says which rows these were. A count alone cannot: a step mixes channels, and
	// paying for a row that already existed would spend an allowance on mail that is not sent.
	rows, err := tx.QueryContext(ctx, builder.String()+" RETURNING channel", args...)
	if err != nil {
		return Charge{}, err
	}
	defer func() { _ = rows.Close() }()
	written := Charge{}
	for rows.Next() {
		var channel string
		if err := rows.Scan(&channel); err != nil {
			return Charge{}, err
		}
		written[channel]++
	}
	if err := rows.Err(); err != nil {
		return Charge{}, err
	}
	return written, nil
}

// ReserveAndInsert reserves the day's budget and writes the first chunk of messages in one
// transaction (D15).
//
// Reserving and writing apart is a leak: a process that reserved and then died before inserting has
// spent the day's allowance on mail that does not exist, and nothing gives it back. Together, either
// both happen or neither does.
//
// The reservation is made for the whole step — that is the check — and then corrected down to what
// was actually new, because a row that already existed is not charged for. That correction is also
// what makes a retry safe: a job that runs again after inserting and before moving the campaign on
// reserves nothing the second time.
//
// fits=false means the budget is spent: nothing is written and nothing is reserved.
//
// Every message it was given is written before it returns, so there is no "the rest" for a caller to
// write, and the day is charged for exactly what was new by the time it returns. What a *crash* can
// leave behind is the whole step reserved with only part of it written: over-reserved, which is the
// direction a budget may be wrong in.
func (db *DB) ReserveAndInsert(ctx context.Context, day, credentialID string, limits Limits,
	messages []model.Message) (fits bool, inserted int, err error) {
	if len(messages) == 0 {
		return true, 0, nil
	}
	// The charge is what the messages are: one reservation per channel they go through. Deriving it
	// from them rather than taking it as an argument is what keeps a caller from paying for fewer
	// messages than it writes.
	charge := ChargeOf(messages)
	first := messages[:min(InsertChunkSize, len(messages))]
	var paid Charge
	err = db.Write(ctx, func(tx *sql.Tx) error {
		fits, inserted, paid = false, 0, Charge{}
		ok, err := reserveTx(ctx, tx, day, credentialID, charge, limits)
		if err != nil || !ok {
			return err
		}
		// The whole step's reservation, the day it belongs to, and the first chunk of messages, in one
		// transaction. The reservation is what makes the day's ceiling true, so it must not be able to
		// go missing; the messages follow it in chunks, because one transaction for all of them holds
		// the single writer for as long as the audience is long — a million messages is about sixteen
		// seconds of unsubscribe records, opens, clicks and SNS callbacks queued behind it, and a CLI
		// write that gives up after ten (design §5.2).
		//
		// A process that dies partway through therefore leaves the day charged for more than exists,
		// never for less: a reservation is a promise to send, and the failure a spend can be in is
		// spending too much of it. Everything below corrects downwards when it can.
		written, err := insertMessagesTx(ctx, tx, first)
		if err != nil {
			return err
		}
		paid, inserted = written, written.Total()
		if err := markReservedDayTx(ctx, tx, day, first); err != nil {
			return err
		}
		fits = true
		return nil
	})
	if err != nil || !fits {
		return fits, inserted, err
	}
	if over := paid.beyond(charge); len(over) > 0 {
		return fits, inserted, fmt.Errorf("charged for fewer messages than were written on %s",
			strings.Join(over, ", "))
	}

	// The rest, one transaction per chunk.
	for start := len(first); start < len(messages); start += InsertChunkSize {
		chunk := messages[start:min(start+InsertChunkSize, len(messages))]
		written, err := db.insertChunk(ctx, chunk)
		paid = paid.plus(written)
		inserted += written.Total()
		if err != nil {
			// The messages after this point do not exist, so the day must not stay charged for them.
			// Best effort: the ledger can be read again, a promised send cannot be un-promised by
			// guessing.
			if refundErr := db.Refund(ctx, day, credentialID, charge.minus(paid)); refundErr != nil {
				return fits, inserted, errors.Join(err, refundErr)
			}
			return fits, inserted, err
		}
	}
	// Rows that already existed were not charged for: the reservation was for the whole step, and this
	// corrects it down to what was actually new, channel by channel (a job retried after inserting
	// must not be charged twice).
	if err := db.Refund(ctx, day, credentialID, charge.minus(paid)); err != nil {
		return fits, inserted, err
	}
	return fits, inserted, nil
}

// claimableCampaign restricts a claim to messages whose campaign is still sending. A message of a
// paused or finished campaign stays pending until the campaign is resumed, so a paused campaign
// does not spin: without this the batch would claim and release the same rows forever.
const claimableCampaign = `(kind NOT IN (?, ?) OR campaign_id IN (SELECT id FROM campaigns WHERE status = ?))`

// claimableArgs are the placeholders of claimableCampaign.
func claimableArgs() []any {
	return []any{string(model.KindCampaign), string(model.KindLastChance), string(model.CampaignSending)}
}

// ClaimMessages atomically moves up to n pending messages to claimed and returns them. The claim is
// what makes sending at most once (D14): SES is only called after it commits. The order within a
// batch is the database's, not the caller's.
func (db *DB) ClaimMessages(ctx context.Context, n int) ([]model.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	args := append([]any{string(model.MessagePending)}, claimableArgs()...)
	args = append(args, n)
	return db.claim(ctx, "SELECT id FROM messages WHERE status = ? AND "+claimableCampaign+
		" ORDER BY created_at, id LIMIT ?", args...)
}

// ClaimMessagesByID claims exactly the messages named, skipping any that are not pending or whose
// campaign is not sending.
func (db *DB) ClaimMessagesByID(ctx context.Context, ids []string) ([]model.Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := append([]any{string(model.MessagePending)}, claimableArgs()...)
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := "SELECT id FROM messages WHERE status = ? AND " + claimableCampaign + " AND id IN (" + //nolint:gosec // G202: placeholders, not values
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
		&status, &m.ProviderMessageID, &m.Error, &claimedAt, &sentAt, &delivered, &openedAt, &clickedAt,
		&bouncedAt, &complained, &createdAt, &m.Channel)
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

// MarkMessageSent and the channel-scoped lookups live in messages_channel.go with the claims: an id
// a provider handed out is only unique within the channel that handed it out.

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

// CountUnfinishedCampaignMessages is how many of a campaign's messages have not reached a final
// state: waiting, or claimed by a sender that has not finished with them.
//
// Claimed counts. A campaign is only finished when nothing is in flight, because a message another
// process released after this one settled would be pending on a campaign that is no longer sending —
// nobody claims it, and the report says the issue went out (the phase 1-3 audit's C2).
func (db *DB) CountUnfinishedCampaignMessages(ctx context.Context, campaignID string) (int, error) {
	var unfinished int
	err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM messages
        WHERE campaign_id = ? AND kind IN (?, ?) AND status IN (?, ?)`,
		campaignID, string(model.KindCampaign), string(model.KindLastChance),
		string(model.MessagePending), string(model.MessageClaimed)).Scan(&unfinished)
	return unfinished, err
}

// InsertSignupMessage writes one welcome or confirmation message if the newsletter is still under
// its cap, and reports whether it did.
//
// The count and the insert are one statement, so two signups arriving together cannot both read a
// count below the cap and both write: a check-then-insert lets a burst through exactly when the cap
// matters most (the phase 1-3 audit's ST-I6).
func (db *DB) InsertSignupMessage(ctx context.Context, message model.Message, newsletterID string,
	kinds []model.MessageKind, since time.Time, cap int) (bool, error) {
	inserted := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		inserted = false
		args := []any{
			message.ID, string(message.Kind), message.CampaignID, message.NewsletterID, message.ContactID,
			message.Email, message.Provider, string(message.Status), message.ProviderMessageID, message.Error,
			formatTimePtr(message.ClaimedAt), formatTimePtr(message.SentAt), formatTimePtr(message.DeliveredAt),
			formatTimePtr(message.OpenedAt), formatTimePtr(message.ClickedAt), formatTimePtr(message.BouncedAt),
			formatTimePtr(message.ComplainedAt), formatTime(message.CreatedAt), channelOf(message),
			newsletterID, formatTime(since),
		}
		for _, kind := range kinds {
			args = append(args, string(kind))
		}
		args = append(args, cap)

		// The SELECT provides the row only when the count allows it, and the count is part of the
		// same statement: it sees every row committed before this transaction, and no other.
		result, err := tx.ExecContext(ctx, insertSignupQuery(len(kinds)), args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		inserted = affected > 0
		return nil
	})
	return inserted, err
}

// insertSignupQuery is the statement InsertSignupMessage runs, with one placeholder per kind.
//
//nolint:gosec // G202: the only interpolated text is a run of "?" placeholders, one per kind.
func insertSignupQuery(kinds int) string {
	return `INSERT OR IGNORE INTO messages (` + messageColumns + `)
        SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
        WHERE (SELECT count(*) FROM messages
               WHERE newsletter_id = ? AND created_at >= ? AND kind IN (` +
		strings.TrimSuffix(strings.Repeat("?,", kinds), ",") + `)) < ?`
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

// timestampColumns maps the plan's field names onto columns. The set is closed on purpose: a field
// name never reaches SQL as text, so a malformed plan cannot become an injection.
var timestampColumns = map[string]string{
	"openedAt": "opened_at", "clickedAt": "clicked_at", "deliveredAt": "delivered_at",
	"bouncedAt": "bounced_at", "complainedAt": "complained_at",
}

// FirstTimestamp sets one message timestamp only if it is still empty, and reports whether it wrote.
// That WHERE clause is what makes a repeated SNS delivery harmless.
func (db *DB) FirstTimestamp(ctx context.Context, id, field string, at time.Time) (bool, error) {
	column, ok := timestampColumns[field]
	if !ok {
		return false, fmt.Errorf("unknown message timestamp %q", field)
	}
	wrote := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		wrote = false
		result, err := tx.ExecContext(ctx,
			"UPDATE messages SET "+column+" = ? WHERE id = ? AND "+column+" IS NULL",
			formatTime(at), id)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		wrote = affected > 0
		return nil
	})
	return wrote, err
}

// SetMessageStatusFrom moves a message to to only from one of the from statuses, and reports whether
// it moved. That is how a late Delivery can't undo a Bounce.
func (db *DB) SetMessageStatusFrom(ctx context.Context, id string, to model.MessageStatus, from []model.MessageStatus) (bool, error) {
	moved := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		moved = false
		placeholders := make([]string, len(from))
		args := []any{string(to), id}
		for i, status := range from {
			placeholders[i] = "?"
			args = append(args, string(status))
		}
		// The only interpolated text is a run of "?" placeholders; every value is bound.
		query := "UPDATE messages SET status = ? WHERE id = ? AND status IN (" + //nolint:gosec // G202: placeholders, not values
			strings.Join(placeholders, ",") + ")"
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		moved = affected > 0
		return nil
	})
	return moved, err
}

// SetMessageError records why a message failed, truncated to upstream's 500 characters.
func (db *DB) SetMessageError(ctx context.Context, id, message string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE messages SET error = ? WHERE id = ?",
			truncate(message, 500), id)
		return err
	})
}

// messageByProviderIDWhere and the lookups that name a channel are in messages_channel.go, with the
// claims: an id a provider handed out is only unique within the channel that handed it out.

// HasMessageOfKind reports whether a contact already has a message of this kind.
func (db *DB) HasMessageOfKind(ctx context.Context, contactID string, kind model.MessageKind) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		"SELECT count(*) FROM messages WHERE contact_id = ? AND kind = ?",
		contactID, string(kind)).Scan(&count)
	return count > 0, err
}

// HasMessageOfKindSince is HasMessageOfKind bounded to a window.
func (db *DB) HasMessageOfKindSince(ctx context.Context, contactID string,
	kind model.MessageKind, since time.Time) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		"SELECT count(*) FROM messages WHERE contact_id = ? AND kind = ? AND created_at >= ?",
		contactID, string(kind), formatTime(since)).Scan(&count)
	return count > 0, err
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
