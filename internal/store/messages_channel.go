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

// The channel-specific message queries live together here: the claims, which split the queue by
// channel (multi-channel design §6), and the lookups and writes that name a channel, because the id
// a provider hands out is only unique within the channel that handed it out (2c-2).

// ClaimChannelMessages is ClaimMessages for one channel's messages. Each channel's lane claims its
// own, so a channel that is throttled or slow never holds another channel's mail.
func (db *DB) ClaimChannelMessages(ctx context.Context, channel string, n int) ([]model.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	args := append([]any{string(model.MessagePending)}, claimableArgs()...)
	args = append(args, channel, n)
	return db.claim(ctx, "SELECT id FROM messages WHERE status = ? AND "+claimableCampaign+
		" AND channel = ? ORDER BY created_at, id LIMIT ?", args...)
}

// ClaimMessagesOutside claims up to n pending messages whose channel is none of channels: mail
// recorded on a channel this process has not loaded (disabled, broken, or not there at all). No
// lane would ever claim it, so the engine claims it here to fail it or pause its campaign.
func (db *DB) ClaimMessagesOutside(ctx context.Context, channels []string, n int) ([]model.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	args := append([]any{string(model.MessagePending)}, claimableArgs()...)
	selection := "SELECT id FROM messages WHERE status = ? AND " + claimableCampaign
	if len(channels) > 0 {
		placeholders := make([]string, len(channels))
		for i, channel := range channels {
			placeholders[i] = "?"
			args = append(args, channel)
		}
		selection += " AND channel NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, n)
	// channel, then created_at, id: the order the claim index is in. Ordering by age alone would
	// make SQLite gather every waiting message and sort it, because "not one of these channels" is
	// not a range it can walk; this way the walk stays a walk. Nothing depends on which of the
	// strays comes first — they are all about to be failed, or to pause their campaign.
	return db.claim(ctx, selection+" ORDER BY channel, created_at, id LIMIT ?", args...)
}

// messageByProviderIDWhere is MessageByProviderID's condition. Its last term, a non-empty
// provider_message_id, repeats the partial index's own condition, which is what lets SQLite use it.
const messageByProviderIDWhere = "channel = ? AND provider_message_id = ? AND provider_message_id <> ''"

// MessageByProviderID loads the message a channel's own id names, which is how most events arrive.
//
// The id is only unique within its channel: two providers can hand out the same one, so the lookup
// names the channel. Requiring a non-empty provider_message_id as well is what lets SQLite answer it
// from the partial (channel, provider_message_id) index.
func (db *DB) MessageByProviderID(ctx context.Context, channel, providerMessageID string) (model.Message, error) {
	message, err := scanMessage(db.sql.QueryRowContext(ctx,
		"SELECT "+messageColumns+" FROM messages WHERE "+messageByProviderIDWhere, channel, providerMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Message{}, fmt.Errorf("message with provider id %s on channel %s: %w", providerMessageID, channel,
			ErrNotFound)
	}
	return message, err
}

// AdoptProviderMessageID writes the channel's id onto a message that got an event before the send
// was written back, so later events find it by id.
//
// The condition on provider_message_id is also the channel rule: once a message carries an id, the
// channel that id came from owns it, and nothing may move it to another one.
func (db *DB) AdoptProviderMessageID(ctx context.Context, id, providerMessageID string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE messages SET provider_message_id = ? WHERE id = ? AND provider_message_id = ''",
			providerMessageID, id)
		return err
	})
}

// MarkMessageSent writes back a message the channel accepted.
func (db *DB) MarkMessageSent(ctx context.Context, id, providerMessageID string, sentAt time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE messages SET status = ?, provider_message_id = ?, sent_at = ?, error = '' WHERE id = ?",
			string(model.MessageSent), providerMessageID, formatTime(sentAt), id)
		if err != nil {
			return err
		}
		return requireOneRow(result, "message", id)
	})
}
