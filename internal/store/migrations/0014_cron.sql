-- Cron (development plan M7 step 7.6): operator-defined scheduled jobs and the
-- history of their runs. A job runs as a chosen account with an argv, an existing
-- script file, or a panel-written /bin/sh script; the row is the source of truth
-- and the shell script file under the panel data directory is rendered from it.

CREATE TABLE cron_jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    schedule        TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('argv', 'script', 'shell')),
    run_user        TEXT NOT NULL,
    argv            TEXT NOT NULL DEFAULT '[]',
    script_path     TEXT NOT NULL DEFAULT '',
    script_body     TEXT NOT NULL DEFAULT '',
    workdir         TEXT NOT NULL DEFAULT '',
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds > 0),
    overlap         TEXT NOT NULL CHECK (overlap IN ('skip', 'queue', 'allow')),
    retention       INTEGER NOT NULL CHECK (retention > 0),
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

-- job_id is not a declared foreign key: the store does not enable foreign_keys,
-- so cron deletes a job's runs itself in the same transaction.
CREATE TABLE cron_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      INTEGER NOT NULL,
    source      TEXT NOT NULL CHECK (source IN ('schedule', 'manual')),
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    exit_code   INTEGER NOT NULL DEFAULT 0,
    status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'timeout', 'canceled', 'interrupted', 'skipped')),
    log_path    TEXT NOT NULL DEFAULT '',
    output_tail TEXT NOT NULL DEFAULT '',
    truncated   INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    error       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX cron_runs_job ON cron_runs (job_id, id);
