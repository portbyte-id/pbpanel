-- Version history of managed config files (development plan step 1.4).
--
-- `confedit` keeps the last versions of every file it has written. The content
-- lives in files under /www/panel/history; this table is the index: which
-- versions exist for a path, when each was recorded and why. `version` counts
-- up per path and is never reused, so a version number shown in the UI keeps
-- meaning the same content after older versions have been trimmed.

CREATE TABLE config_history (
    path       TEXT NOT NULL,
    version    INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    sha256     TEXT NOT NULL,
    size       INTEGER NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (path, version)
);
