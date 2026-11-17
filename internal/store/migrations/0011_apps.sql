-- Apps (development plan M5): Go binaries, Node.js apps and arbitrary commands
-- run as supervised processes. A row is the operator's definition; the program
-- file under supervisor/conf.d is rendered from it and is never the source of truth.

CREATE TABLE apps (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL UNIQUE,
    type         TEXT NOT NULL CHECK (type IN ('go', 'node', 'command')),
    command      TEXT NOT NULL,
    args         TEXT NOT NULL DEFAULT '[]',
    workdir      TEXT NOT NULL,
    env          TEXT NOT NULL DEFAULT '{}',
    run_user     TEXT NOT NULL DEFAULT 'www',
    node_version TEXT NOT NULL DEFAULT '',
    autostart    INTEGER NOT NULL DEFAULT 1,
    restart      TEXT NOT NULL DEFAULT 'always' CHECK (restart IN ('always', 'on-failure', 'never')),
    port         INTEGER CHECK (port IS NULL OR port BETWEEN 1 AND 65535),
    created_at   TEXT NOT NULL
);

-- Two apps cannot claim one port. NULL ports do not collide.
CREATE UNIQUE INDEX apps_port ON apps (port) WHERE port IS NOT NULL;
