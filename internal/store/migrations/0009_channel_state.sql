-- Multi-channel sending, step 2d (plan 2026-10-13-multi-channel-step2.md, "2d（第一段）").
--
-- A channel that failed with ErrorPause or ErrorQuota is down: down_reason says why, down_since
-- when it first went down, and down_until when it comes back on its own (NULL: when an operator
-- runs `channel enable`). A channel is down while down_reason is not empty and down_until is NULL
-- or still ahead. The default channel is not a row of channels (it is the SES_* settings), so its
-- state is the one row of default_channel_state, with the same three columns.
--
-- channel_fallbacks is each channel's fallback chain, in order: where its unsent mail is
-- reassigned when it goes down. Either side may be 'default'. Additive only; it does not depend on
-- 0008, and applies in either order with it.
ALTER TABLE channels ADD COLUMN down_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN down_since TEXT;
ALTER TABLE channels ADD COLUMN down_until TEXT;

CREATE TABLE default_channel_state (
    only        INTEGER PRIMARY KEY CHECK (only = 1),
    down_reason TEXT NOT NULL DEFAULT '',
    down_since  TEXT,
    down_until  TEXT
);
INSERT INTO default_channel_state (only) VALUES (1);

CREATE TABLE channel_fallbacks (
    channel  TEXT NOT NULL,
    position INTEGER NOT NULL,
    fallback TEXT NOT NULL,
    PRIMARY KEY (channel, position)
);
