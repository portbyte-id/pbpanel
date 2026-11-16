-- PHP (development plan M2).
--
-- `sites.kind` gains `php`. SQLite cannot change a CHECK in place, so the table
-- is rebuilt; `site_aliases` refers to it by name and is untouched.

CREATE TABLE sites_new (
    domain       TEXT PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('static', 'proxy', 'php')),
    root         TEXT NOT NULL DEFAULT '',
    proxy_target TEXT NOT NULL DEFAULT '',
    php_version  TEXT,
    created_at   TEXT NOT NULL
);

INSERT INTO sites_new (domain, kind, root, proxy_target, php_version, created_at)
SELECT domain, kind, root, proxy_target, php_version, created_at FROM sites;

DROP TABLE sites;

ALTER TABLE sites_new RENAME TO sites;

-- One row per PHP version the panel has ever been asked to pull. `image` is the
-- official reference that was pulled and `digest` what it resolved to; `output`
-- is the tail of a failed pull.
CREATE TABLE php_versions (
    version    TEXT PRIMARY KEY,
    image      TEXT NOT NULL DEFAULT '',
    digest     TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL CHECK (state IN ('absent', 'pulling', 'ready', 'failed')),
    output     TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

-- The php.ini form of one version, as one JSON document.
CREATE TABLE php_settings (
    version TEXT PRIMARY KEY,
    json    TEXT NOT NULL
);

-- One pool per PHP site. `name` is the FPM pool name derived from the domain;
-- it is unique so two domains that map to the same name cannot share a socket.
CREATE TABLE php_pools (
    domain TEXT PRIMARY KEY,
    name   TEXT NOT NULL UNIQUE,
    json   TEXT NOT NULL
);
