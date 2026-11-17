-- File manager (development plan M3).
--
-- `fm_trash` is the recycle bin's index: where each deleted entry came from and
-- who owned it, so a restore puts it back as it was. The payload lives at
-- <panel>/trash/<id>/<name>.
CREATE TABLE fm_trash (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    orig_path  TEXT NOT NULL,
    size       INTEGER NOT NULL DEFAULT 0,
    is_dir     INTEGER NOT NULL DEFAULT 0,
    mode       INTEGER NOT NULL,
    uid        INTEGER NOT NULL,
    gid        INTEGER NOT NULL,
    deleted_at TEXT NOT NULL
);

CREATE INDEX fm_trash_deleted_at ON fm_trash (deleted_at);

-- `fm_uploads` is one chunked upload in progress. The bytes are at
-- <panel>/uploads/<id>.part; `received` is how many of them are committed, and
-- is where a resuming client starts.
CREATE TABLE fm_uploads (
    id         TEXT PRIMARY KEY,
    dir        TEXT NOT NULL,
    name       TEXT NOT NULL,
    size       INTEGER NOT NULL,
    received   INTEGER NOT NULL DEFAULT 0,
    overwrite  INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
