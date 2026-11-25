-- Notifications: channels, the event list behind the bell, one row per open
-- condition key, and the delivery log. The schema is in
-- docs/design/notifications-plan.md §4.2. Channel secrets are sealed with
-- secretbox (aad notify_channels:secret:<id>); the sealed value is never read
-- back into an API response.

CREATE TABLE notify_channels (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    type          TEXT NOT NULL CHECK (type IN ('smtp', 'webhook', 'telegram', 'discord')),
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    min_severity  TEXT NOT NULL DEFAULT 'warning' CHECK (min_severity IN ('info', 'warning', 'critical')),
    kinds         TEXT NOT NULL DEFAULT '[]',
    quiet_start   TEXT NOT NULL DEFAULT '',
    quiet_end     TEXT NOT NULL DEFAULT '',
    send_resolved INTEGER NOT NULL DEFAULT 1 CHECK (send_resolved IN (0, 1)),
    config        TEXT NOT NULL DEFAULT '{}',
    secret        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE notify_events (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    at       TEXT NOT NULL,
    kind     TEXT NOT NULL,
    severity TEXT NOT NULL,
    key      TEXT NOT NULL,
    subject  TEXT NOT NULL,
    body     TEXT NOT NULL DEFAULT '',
    source   TEXT NOT NULL,
    link     TEXT NOT NULL DEFAULT '',
    fields   TEXT NOT NULL DEFAULT '{}',
    resolved INTEGER NOT NULL DEFAULT 0,
    read_at  TEXT
);
CREATE INDEX notify_events_at ON notify_events (at DESC);

CREATE TABLE notify_state (
    key           TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    source        TEXT NOT NULL,
    open          INTEGER NOT NULL,
    severity      TEXT NOT NULL,
    opened_at     TEXT NOT NULL,
    last_event_id INTEGER NOT NULL
);

CREATE TABLE notify_deliveries (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id   INTEGER NOT NULL REFERENCES notify_events (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES notify_channels (id) ON DELETE CASCADE,
    state      TEXT NOT NULL CHECK (state IN ('queued', 'sent', 'failed', 'suppressed')),
    reason     TEXT NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 0,
    next_at    TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    sent_at    TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX notify_deliveries_due ON notify_deliveries (state, next_at);
