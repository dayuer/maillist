-- Multi-channel sending, step 2a (plan 2026-10-13-multi-channel-step2.md).
--
-- A configured sending channel. The default channel is not a row: it is the SES_* settings, and a
-- deployment with no rows here behaves exactly as before. Secrets are never stored: secret_env is the
-- prefix of the environment variables that hold them. params is a JSON object of the type's
-- non-secret settings (for ses: region, configuration_set, tenant, endpoint).
CREATE TABLE channels (
    name           TEXT PRIMARY KEY,
    type           TEXT NOT NULL,
    secret_env     TEXT NOT NULL,
    params         TEXT NOT NULL DEFAULT '{}',
    max_per_second INTEGER NOT NULL DEFAULT 0,
    max_per_day    INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
