-- Audit trail: one row per state-changing action. `at` is RFC 3339 in UTC and
-- `detail` holds JSON text that the audit package has already redacted, so a
-- secret must never reach this table.
--
-- Numbered 0003, not 0002: 0001_init.sql reserves 0002 for the settings
-- registry, which lands in a parallel branch. Migration numbers are never
-- reused, so leaving the gap costs nothing and a collision would.
CREATE TABLE audit_log (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    at     TEXT NOT NULL,
    actor  TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL,
    result TEXT NOT NULL,
    detail TEXT NOT NULL
);

-- GET /api/audit lists newest first.
CREATE INDEX audit_log_at ON audit_log (at DESC);
