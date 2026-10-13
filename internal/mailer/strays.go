package mailer

import (
	"context"
	"errors"
	"fmt"

	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

// Strays are messages on a channel this engine has no lane for. No lane claims them, so the engine
// claims them on their own and decides what they are:
//
//   - a channel that does not exist at all is a message that can never go out: it fails, with the
//     channel named in its error;
//   - a channel that exists but is not loaded here — disabled, broken (App.ChannelProblems), or
//     enabled after this process started — is the operator's to fix. A campaign message goes back
//     to the queue and its campaign pauses with a reason naming the channel; nothing is counted as
//     failed, and resuming after the fix sends it. A message outside a campaign has no campaign to
//     pause and would be claimed again on every tick, so it fails with the same reason.

// sendStrayBatch claims and disposes of strays until there are none left.
func (e *Engine) sendStrayBatch(ctx context.Context) (BatchResult, error) {
	total := BatchResult{}
	for {
		strays, err := e.store.ClaimMessagesOutside(ctx, e.channelNames(), rules.SendBatchSize)
		switch {
		case err != nil && ctx.Err() != nil:
			total.Retry = true
			return total, nil
		case err != nil:
			return total, err
		case len(strays) == 0:
			return total, nil
		}
		// Every stray leaves the claimable set: failed, or held under a paused campaign. So this
		// loop ends.
		result, err := e.sendStrays(ctx, strays)
		total.add(result)
		if err != nil {
			return total, err
		}
	}
}

// sendStrays disposes of claimed strays (see above). Every write runs on the detached context, as the
// rest of the bookkeeping does.
func (e *Engine) sendStrays(ctx context.Context, strays []model.Message) (BatchResult, error) {
	result := BatchResult{}
	reasons := map[string]string{} // channel → why it cannot send, read once per batch
	exists := map[string]bool{}
	paused := map[string]bool{}
	touched := map[string]bool{}
	for i, message := range strays {
		channel := messageChannel(message)
		if _, read := reasons[channel]; !read {
			reason, found, err := e.channelUnavailable(ctx, channel)
			if err != nil {
				e.releaseAll(ctx, strays[i:])
				return result, err
			}
			reasons[channel], exists[channel] = reason, found
		}
		reason := reasons[channel]
		if exists[channel] && rules.IsCampaignKind(message.Kind) && message.CampaignID != "" {
			if err := e.releaseMessage(ctx, message.ID); err != nil {
				e.releaseAll(ctx, strays[i+1:])
				return result, err
			}
			if !paused[message.CampaignID] {
				paused[message.CampaignID] = true
				if err := e.writeBack(ctx, func(writeCtx context.Context) error {
					return e.pause(writeCtx, message.CampaignID, reason)
				}); err != nil {
					e.releaseAll(ctx, strays[i+1:])
					return result, err
				}
			}
			continue
		}
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			if err := e.store.MarkMessageFailed(writeCtx, message.ID, reason); err != nil {
				return err
			}
			return e.addStrayFailedStats(writeCtx, message)
		}); err != nil {
			e.releaseAll(ctx, strays[i+1:])
			return result, err
		}
		e.log.Warn("message failed: its channel cannot send", "message", message.ID, "reason", reason)
		result.Failed++
		if message.CampaignID != "" {
			touched[message.CampaignID] = true
		}
	}
	// A campaign whose last messages just failed here is finished, the same as one whose last
	// messages failed in a lane.
	for campaignID := range touched {
		if paused[campaignID] {
			continue
		}
		if err := e.writeBack(ctx, func(writeCtx context.Context) error {
			return e.settle(writeCtx, campaignID)
		}); err != nil {
			return result, err
		}
	}
	return result, nil
}

// channelUnavailable says why a channel with no lane cannot send, and whether it exists at all (a
// row in channels; the default channel always has a lane).
func (e *Engine) channelUnavailable(ctx context.Context, channel string) (reason string, exists bool,
	err error) {
	row, err := e.store.Channel(ctx, channel)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("channel %s does not exist", channel), false, nil
	}
	if err != nil {
		return "", false, err
	}
	switch problem := e.cfg.ChannelProblems[channel]; {
	case !row.Enabled:
		return fmt.Sprintf("channel %s is disabled: enable it, then resume the campaign", channel), true, nil
	case problem != nil:
		return fmt.Sprintf("channel %s could not be loaded (%v): fix it, restart, then resume the "+
			"campaign", channel, problem), true, nil
	default:
		return fmt.Sprintf("channel %s is not loaded in this process: restart it, then resume the "+
			"campaign", channel), true, nil
	}
}

// addStrayFailedStats counts a failed stray against its campaign, the way addFailedStats does for a
// message a lane sent.
func (e *Engine) addStrayFailedStats(ctx context.Context, message model.Message) error {
	if !rules.IsCampaignKind(message.Kind) || message.CampaignID == "" {
		return nil
	}
	if message.Kind == model.KindLastChance {
		return e.store.AddLastChanceStats(ctx, message.CampaignID, store.LastChanceCounters{Failed: 1})
	}
	return e.store.AddCampaignStats(ctx, message.CampaignID, store.CampaignCounters{Failed: 1})
}
