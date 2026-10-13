-- Multi-channel sending, step 2c-1 (plan 2026-10-13-multi-channel-step2.md).
--
-- One routing rule, in the order it is tried: the first rule whose match claims the recipient
-- decides the channel. match is the stored form of core/route.Match ('default' | 'domain:a.b,c.d' |
-- 'mailbox:gmail'); targets is a JSON array ([{"channel":"aws2","weight":80}]), and a rule with a
-- single target carries weight 1. position is kept contiguous from 0, and the one default rule,
-- when there is one, is always last.
CREATE TABLE routes (
    id         TEXT PRIMARY KEY,
    position   INTEGER NOT NULL,
    match      TEXT NOT NULL,
    targets    TEXT NOT NULL,
    created_at TEXT NOT NULL
);
