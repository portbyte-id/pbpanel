-- Background jobs and their output (development plan step 0.9).
--
-- A job is a Go function run inside the daemon. The row records what it was,
-- how it ended, and how far its output goes; the lines live in their own table
-- so a reloaded page can replay them. `last_seq` is the per-job sequence
-- counter and it keeps counting after old lines are dropped, which is what
-- keeps a client's Last-Event-ID meaningful once the output cap has trimmed the
-- start of the stream.

CREATE TABLE jobs (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed','canceled','interrupted')),
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT,
    exit_code   INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT '',
    last_seq    INTEGER NOT NULL DEFAULT 0,
    dropped     INTEGER NOT NULL DEFAULT 0,
    truncated   INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0,1))
);

CREATE INDEX jobs_created_at ON jobs (created_at);

CREATE TABLE job_output (
    job_id TEXT NOT NULL REFERENCES jobs (id),
    seq    INTEGER NOT NULL,
    line   TEXT NOT NULL,
    PRIMARY KEY (job_id, seq)
);
