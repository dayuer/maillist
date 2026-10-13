-- Multi-channel sending, step 2c-2 (plan 2026-10-13-multi-channel-step2.md, "2c-2").
--
-- The day's reservation is per channel now: a campaign that splits between two channels reserves
-- against each of them separately, and one channel running out of its own budget no longer blocks
-- the mail going through another. SQLite cannot change a primary key, so the table is rebuilt; rows
-- written before there was more than one channel went through the default channel, and that is what
-- they are recorded as.
--
-- Rebuilding send_ledger changes the primary key, so an older release's ON CONFLICT(day,
-- credential_id) fails against this database. Upgrade the way 0005 is upgraded: stop serve, back the
-- database up, then change the binary. Rolling back means restoring that backup.
CREATE TABLE send_ledger_new (
    day           TEXT NOT NULL,
    credential_id TEXT NOT NULL DEFAULT '',
    channel       TEXT NOT NULL DEFAULT 'default',
    reserved      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, credential_id, channel)
);
INSERT INTO send_ledger_new (day, credential_id, channel, reserved)
    SELECT day, credential_id, 'default', reserved FROM send_ledger;
DROP TABLE send_ledger;
ALTER TABLE send_ledger_new RENAME TO send_ledger;

-- The two claim queries both fix the status and walk the waiting messages in an order this index is
-- in — the oldest of one channel, and the strays channel by channel — so neither sorts what it
-- reads. messages_claimable(status, kind, id) served neither, and a claim sorted the whole waiting
-- set every time; at 200,000 waiting messages that held claiming to about 180/s. The EXPLAIN test in
-- messages_channel_test.go pins both plans.
CREATE INDEX messages_claim_channel ON messages(status, channel, created_at, id);
