-- 阶段 2 的初始 schema。列名是 Go model 的 snake_case；时间列是定宽 UTC TEXT（见计划 §0.2）。
-- 表本身不加 IF NOT EXISTS：schema_migrations 保证每个文件只跑一次，重复执行要响。

CREATE TABLE newsletters (
    id                    TEXT PRIMARY KEY,
    slug                  TEXT NOT NULL UNIQUE,
    name                  TEXT NOT NULL,
    image_url             TEXT NOT NULL DEFAULT '',
    from_name             TEXT NOT NULL DEFAULT '',
    from_email            TEXT NOT NULL,
    reply_to              TEXT NOT NULL DEFAULT '',
    postal_address        TEXT NOT NULL DEFAULT '',
    archive_enabled       INTEGER NOT NULL DEFAULT 0,
    description           TEXT NOT NULL DEFAULT '',
    social_links          TEXT NOT NULL DEFAULT '[]',
    author_bio            TEXT NOT NULL DEFAULT '',
    author_image_url      TEXT NOT NULL DEFAULT '',
    require_confirmation  INTEGER NOT NULL DEFAULT 0,
    cleanup_enabled       INTEGER NOT NULL DEFAULT 0,
    cleanup_issues        INTEGER NOT NULL DEFAULT 0,
    cleanup_days          INTEGER NOT NULL DEFAULT 0,
    welcome_enabled       INTEGER NOT NULL DEFAULT 0,
    welcome_subject       TEXT NOT NULL DEFAULT '',
    welcome_preview_text  TEXT NOT NULL DEFAULT '',
    welcome_markdown      TEXT NOT NULL DEFAULT '',
    confirmation_subject      TEXT NOT NULL DEFAULT '',
    confirmation_preview_text TEXT NOT NULL DEFAULT '',
    confirmation_markdown     TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

CREATE TABLE contacts (
    id                       TEXT PRIMARY KEY,
    newsletter_id            TEXT NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    email                    TEXT NOT NULL,
    status                   TEXT NOT NULL DEFAULT 'active',
    source                   TEXT NOT NULL DEFAULT '',
    signup_ip                TEXT NOT NULL DEFAULT '',
    subscribed_at            TEXT,
    confirmed_at             TEXT,
    unsubscribed_at          TEXT,
    last_sent_at             TEXT,
    last_open_at             TEXT,
    last_click_at            TEXT,
    last_engaged_at          TEXT,
    last_chance_at           TEXT,
    ignored_issues           INTEGER NOT NULL DEFAULT 0,
    sent                     INTEGER NOT NULL DEFAULT 0,
    opens                    INTEGER NOT NULL DEFAULT 0,
    clicks                   INTEGER NOT NULL DEFAULT 0,
    imported_emails_received INTEGER NOT NULL DEFAULT 0,
    imported_open_rate       REAL,
    imported_click_rate      REAL,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL
);
-- 一个 newsletter 里一个地址一行；导入与订阅表单都靠它幂等。
CREATE UNIQUE INDEX contacts_newsletter_email ON contacts(newsletter_id, email);
-- 前缀搜索与跨 newsletter 的 suppression 比对。
CREATE INDEX contacts_email ON contacts(email COLLATE NOCASE);
-- 受众查询的排序键（store/audience.go 的 ORDER BY 与 core/audience.Less 同序）。
CREATE INDEX contacts_audience ON contacts(
    newsletter_id, status, last_click_at DESC, last_open_at DESC,
    imported_click_rate DESC, imported_open_rate DESC, created_at, id);

CREATE TABLE campaigns (
    id           TEXT PRIMARY KEY,
    newsletter_id TEXT NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    subject      TEXT NOT NULL DEFAULT '',
    preview_text TEXT NOT NULL DEFAULT '',
    format       TEXT NOT NULL DEFAULT 'markdown',   -- markdown | html
    body         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'draft',
    audience_limit INTEGER NOT NULL DEFAULT 0,       -- 0 = 所有人
    source       TEXT NOT NULL DEFAULT 'cli',
    scheduled_at TEXT,
    started_at   TEXT,
    sent_at      TEXT,
    test_sent_at TEXT,
    archive_slug TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    -- 上游 Campaign schema 的默认值是 true：可空性用列默认表达，Go 零值 false 会被显式覆盖。
    archive_listed INTEGER NOT NULL DEFAULT 1,
    archive_pinned INTEGER NOT NULL DEFAULT 0,
    web_views    INTEGER NOT NULL DEFAULT 0,
    stats_recipients   INTEGER NOT NULL DEFAULT 0,
    stats_sent         INTEGER NOT NULL DEFAULT 0,
    stats_delivered    INTEGER NOT NULL DEFAULT 0,
    stats_bounced      INTEGER NOT NULL DEFAULT 0,
    stats_complained   INTEGER NOT NULL DEFAULT 0,
    stats_opened       INTEGER NOT NULL DEFAULT 0,
    stats_clicked      INTEGER NOT NULL DEFAULT 0,
    stats_unsubscribed INTEGER NOT NULL DEFAULT 0,
    stats_failed       INTEGER NOT NULL DEFAULT 0,
    last_chance_recipients INTEGER NOT NULL DEFAULT 0,
    last_chance_sent       INTEGER NOT NULL DEFAULT 0,
    last_chance_delivered  INTEGER NOT NULL DEFAULT 0,
    last_chance_bounced    INTEGER NOT NULL DEFAULT 0,
    last_chance_complained INTEGER NOT NULL DEFAULT 0,
    last_chance_failed     INTEGER NOT NULL DEFAULT 0,
    last_chance_dormant    INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX campaigns_newsletter_created ON campaigns(newsletter_id, created_at DESC);
CREATE INDEX campaigns_status ON campaigns(status);
-- 归档 slug 要么为空，要么唯一。
CREATE UNIQUE INDEX campaigns_archive_slug ON campaigns(archive_slug) WHERE archive_slug <> '';

CREATE TABLE messages (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    campaign_id   TEXT NOT NULL DEFAULT '',
    newsletter_id TEXT NOT NULL,
    contact_id    TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL,
    provider      TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'pending',
    ses_message_id TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT '',
    claimed_at    TEXT,
    sent_at       TEXT,
    delivered_at  TEXT,
    opened_at     TEXT,
    clicked_at    TEXT,
    bounced_at    TEXT,
    complained_at TEXT,
    created_at    TEXT NOT NULL
);
-- 上游 Message 的两个部分唯一索引：一个联系人一个 campaign 一封 issue，
-- 外加一封 last-chance。测试信（kind='test'）不参与。
CREATE UNIQUE INDEX messages_campaign_contact
    ON messages(campaign_id, contact_id) WHERE kind = 'campaign';
CREATE UNIQUE INDEX messages_campaign_contact_last_chance
    ON messages(campaign_id, contact_id, kind) WHERE kind = 'last-chance';
CREATE INDEX messages_claimable ON messages(status, kind, id);
CREATE INDEX messages_campaign_kind ON messages(campaign_id, kind);
CREATE UNIQUE INDEX messages_ses_message_id ON messages(ses_message_id) WHERE ses_message_id <> '';

CREATE TABLE suppressions (
    email      TEXT PRIMARY KEY COLLATE NOCASE,
    reason     TEXT NOT NULL,          -- bounce | complaint | manual
    detail     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE events (
    id            TEXT PRIMARY KEY,
    type          TEXT NOT NULL,
    ses_message_id TEXT NOT NULL DEFAULT '',
    message_id    TEXT NOT NULL DEFAULT '',
    campaign_id   TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL DEFAULT '',
    occurred_at   TEXT NOT NULL,
    data          TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);
CREATE INDEX events_campaign_type ON events(campaign_id, type);

CREATE TABLE credentials (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,        -- api | mcp
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    scopes       TEXT NOT NULL DEFAULT '',   -- JSON array
    daily_budget INTEGER NOT NULL DEFAULT 0, -- 0 = 无凭据级上限
    last_used_at TEXT,
    revoked_at   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE audit_log (
    id            TEXT PRIMARY KEY,
    actor_kind    TEXT NOT NULL,       -- cli | mcp | api | system
    actor_name    TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL,
    params_digest TEXT NOT NULL DEFAULT '',
    result        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL
);
CREATE INDEX audit_log_created ON audit_log(created_at DESC);

CREATE TABLE jobs (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    payload    TEXT NOT NULL DEFAULT '{}',
    run_at     TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'pending',   -- pending | running | done | dead
    attempts   INTEGER NOT NULL DEFAULT 0,
    locked_at  TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX jobs_ready ON jobs(state, run_at);

-- D15：日预算预留。credential_id 为空串表示 CLI/系统，不设凭据级上限。
CREATE TABLE send_ledger (
    day           TEXT NOT NULL,
    credential_id TEXT NOT NULL DEFAULT '',
    reserved      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, credential_id)
);

CREATE TABLE alerts (
    key          TEXT PRIMARY KEY,
    title        TEXT NOT NULL DEFAULT '',
    last_sent_at TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
