-- The running serve process stamps this every few seconds.
--
-- It is how a CLI command knows a server is already sending. Sending is meant to converge on one
-- process (design §5.2): two senders means a doubled rate limit against SES, a token bucket per
-- process, and messages claimed out from under each other.
CREATE TABLE serve_heartbeat (
    name    TEXT PRIMARY KEY,
    seen_at TEXT NOT NULL
);
