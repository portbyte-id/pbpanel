-- Admin account and sessions (development plan 0.6).
--
-- There is exactly one admin row, enforced by the CHECK on id rather than by
-- callers: decision 3 in plan.md rules out roles and a second account, and a
-- constraint is the only place that stays true.

CREATE TABLE admin (
	id            INTEGER PRIMARY KEY CHECK (id = 1),
	username      TEXT    NOT NULL,
	password_hash TEXT    NOT NULL,
	updated_at    TEXT    NOT NULL
);

-- Session IDs are 256 random bits handed to the client; only their SHA-256 is
-- stored, so a copy of this table cannot be replayed as a cookie. The primary
-- key is the hash, which is what a lookup has, and what makes a duplicate
-- impossible.
CREATE TABLE sessions (
	id_hash      BLOB    PRIMARY KEY,
	created_at   TEXT    NOT NULL,
	last_seen_at TEXT    NOT NULL,
	ip           TEXT    NOT NULL,
	user_agent   TEXT    NOT NULL,
	csrf_token   TEXT    NOT NULL,
	revoked_at   TEXT
);

-- Expiry is a range scan over the idle window, so it is indexed on the column
-- the predicate uses.
CREATE INDEX sessions_last_seen_at ON sessions (last_seen_at);
