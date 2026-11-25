-- Threshold alerts over the metrics history (docs/design/metrics-history-plan.md
-- section 4.2). The samples live in metrics.db; the rules and their state live
-- here because they are configuration and must survive a history reset.
--
-- kind: disk_pct | mem_pct | swap_pct | load_per_core | cpu_pct | service_down |
--       cert_days | site_5xx_pct
-- target: a mount, service name or domain, or '*' for each one seen.
-- A rule fires when its value stays past threshold for for_seconds and clears
-- when it stays past clear_threshold for clear_for_seconds.

CREATE TABLE metric_alert_rules (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    kind              TEXT NOT NULL,
    target            TEXT NOT NULL DEFAULT '*',
    threshold         REAL NOT NULL,
    clear_threshold   REAL NOT NULL,
    for_seconds       INTEGER NOT NULL DEFAULT 300 CHECK (for_seconds >= 0),
    clear_for_seconds INTEGER NOT NULL DEFAULT 300 CHECK (clear_for_seconds >= 0),
    severity          TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    enabled           INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    builtin           INTEGER NOT NULL DEFAULT 0 CHECK (builtin IN (0, 1)),
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

-- One row per (rule, target) that is not in the quiet 'ok' state, so a restart
-- neither re-fires a condition that is still open nor forgets one.
CREATE TABLE metric_alert_state (
    rule_id INTEGER NOT NULL REFERENCES metric_alert_rules (id) ON DELETE CASCADE,
    target  TEXT NOT NULL,
    state   TEXT NOT NULL CHECK (state IN ('ok', 'pending', 'firing', 'clearing')),
    since   INTEGER NOT NULL,
    value   REAL,
    PRIMARY KEY (rule_id, target)
);

INSERT INTO metric_alert_rules
    (kind, target, threshold, clear_threshold, for_seconds, clear_for_seconds, severity, enabled, builtin, created_at, updated_at)
VALUES
    ('disk_pct',      '*',  85,  80,  300,  300, 'warning',  1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('disk_pct',      '*',  95,  90,  300,  300, 'critical', 1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('mem_pct',       '*',  90,  80,  600,  600, 'warning',  1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('swap_pct',      '*',  50,  30,  600,  600, 'warning',  1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('load_per_core', '*',   2, 1.5,  900,  900, 'warning',  1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- service_down: the value is the percent of the minute the service was not running.
    ('service_down',  '*', 100,   0,  300,   60, 'warning',  1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- cert_days: days left; these two count down, so they fire at or below the threshold.
    ('cert_days',     '*',  14,  21,  300,  300, 'warning',  1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('cert_days',     '*',   3,   7,  300,  300, 'critical', 1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- Off by default: a site with a handful of requests can show 100% 5xx for a minute.
    ('site_5xx_pct',  '*',  10,   5,  300,  300, 'warning',  0, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
