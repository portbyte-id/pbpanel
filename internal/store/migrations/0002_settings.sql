-- Operator-changed settings only. A key with no row here is at its registry
-- default, so the defaults live in one place (internal/settings) and not in a
-- second copy that could drift.
CREATE TABLE settings (
	name       TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
