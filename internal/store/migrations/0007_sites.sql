-- Sites and the global nginx settings (development plan steps 1.6 and 1.8).
--
-- `sites.domain` is the primary key and is stored as punycode, so the web root,
-- the config file names and the log names derived from it are plain ASCII.
-- `site_aliases.alias` is unique on its own; the code also checks it against
-- `sites.domain`, because one name may not be both a primary and an alias.
-- `php_version` stays NULL until PHP arrives in M2.

CREATE TABLE sites (
    domain       TEXT PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('static', 'proxy')),
    root         TEXT NOT NULL DEFAULT '',
    proxy_target TEXT NOT NULL DEFAULT '',
    php_version  TEXT,
    created_at   TEXT NOT NULL
);

CREATE TABLE site_aliases (
    alias    TEXT PRIMARY KEY,
    domain   TEXT NOT NULL REFERENCES sites (domain) ON DELETE CASCADE,
    position INTEGER NOT NULL
);

CREATE INDEX site_aliases_domain ON site_aliases (domain, position);

-- One JSON document per settings group. `http` is the common-settings form.
CREATE TABLE nginx_settings (
    name TEXT PRIMARY KEY,
    json TEXT NOT NULL
);
