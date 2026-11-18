-- Built-in cron jobs: a job whose kind is 'builtin' runs a Go function the panel
-- registers under `builtin`, not a command the operator wrote. The first one is
-- the certificate renewal (`tls.renew`), which used to be a separate loop.
--
-- SQLite cannot change a CHECK in place, so cron_jobs is rebuilt with 'builtin'
-- allowed and the new column added. Rows keep their ids; cron_runs refers to a
-- job by id and is untouched. The id sequence is carried over so an id a job
-- once had is never handed to another.

CREATE TABLE cron_jobs_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    schedule        TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('argv', 'script', 'shell', 'builtin')),
    run_user        TEXT NOT NULL,
    argv            TEXT NOT NULL DEFAULT '[]',
    script_path     TEXT NOT NULL DEFAULT '',
    script_body     TEXT NOT NULL DEFAULT '',
    workdir         TEXT NOT NULL DEFAULT '',
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds > 0),
    overlap         TEXT NOT NULL CHECK (overlap IN ('skip', 'queue', 'allow')),
    retention       INTEGER NOT NULL CHECK (retention > 0),
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    builtin         TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

INSERT INTO cron_jobs_new
    (id, name, schedule, kind, run_user, argv, script_path, script_body, workdir,
     timeout_seconds, overlap, retention, enabled, created_at, updated_at)
SELECT id, name, schedule, kind, run_user, argv, script_path, script_body, workdir,
       timeout_seconds, overlap, retention, enabled, created_at, updated_at
FROM cron_jobs;

INSERT INTO sqlite_sequence (name, seq)
SELECT 'cron_jobs_new', seq FROM sqlite_sequence
WHERE name = 'cron_jobs' AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'cron_jobs_new');

UPDATE sqlite_sequence
SET seq = (SELECT seq FROM sqlite_sequence WHERE name = 'cron_jobs')
WHERE name = 'cron_jobs_new'
  AND (SELECT seq FROM sqlite_sequence WHERE name = 'cron_jobs') > seq;

DROP TABLE cron_jobs;

ALTER TABLE cron_jobs_new RENAME TO cron_jobs;
