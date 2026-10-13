package store

import (
	"context"
	"strings"

	"nibwire/internal/core/model"
)

// The claims below split the queue by channel (multi-channel design §6). Step 2c moves the other
// channel-specific message queries here as well (plan 2026-10-13-multi-channel-step2.md).

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
