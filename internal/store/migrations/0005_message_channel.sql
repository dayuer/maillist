-- Multi-channel sending, step 1 (design 2026-10-12-multi-channel-sending-design.md §7).
--
-- Every message now says which channel carried it. Messages written before this migration went
-- through the only channel there was, the SES account the SES_* settings describe, which is named
-- "default". The id that channel answered with is no longer SES's by definition, so the column is
-- renamed on messages and on events, which records the same value.
--
-- The id is unique within a channel, not across them: two providers may hand out the same string.
ALTER TABLE messages ADD COLUMN channel TEXT NOT NULL DEFAULT 'default';
ALTER TABLE messages RENAME COLUMN ses_message_id TO provider_message_id;
ALTER TABLE events RENAME COLUMN ses_message_id TO provider_message_id;
DROP INDEX messages_ses_message_id;
CREATE UNIQUE INDEX messages_channel_provider_message_id
    ON messages(channel, provider_message_id) WHERE provider_message_id <> '';
