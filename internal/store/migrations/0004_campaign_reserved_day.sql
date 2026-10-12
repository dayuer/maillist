-- The day a campaign's audience was reserved against send_ledger (design D15, §11 invariant 4).
--
-- It is written in the same transaction as the reservation, and it is what keeps a resume honest.
-- A resume does not re-choose the audience or re-reserve it: same day, the messages are already paid
-- for and nothing is charged again. A later day, today's ledger has never seen them, so the resume
-- reserves what is left of the campaign before it sends anything — otherwise a campaign queued
-- yesterday and resumed today would spend today's allowance without today counting it, and
-- MAX_SENDS_PER_DAY is what a managed deployment splits an account's quota with.
ALTER TABLE campaigns ADD COLUMN reserved_day TEXT NOT NULL DEFAULT '';
