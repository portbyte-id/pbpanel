-- Docker image management (development plan M8, step 8.1).
--
-- Every image the panel builds for PHP or nginx is one row here, tagged
-- `panel/<kind>:<version>-<build-id>`. The image a version serves is named by
-- `image_pointers.current`, not by a mutable tag. `previous` is kept so one
-- action can put it back; the last two good builds are retained.

CREATE TABLE image_builds (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('php', 'nginx')),
    version     TEXT NOT NULL,
    tag         TEXT NOT NULL,
    image_id    TEXT NOT NULL DEFAULT '',
    extensions  TEXT NOT NULL DEFAULT '[]',
    state       TEXT NOT NULL CHECK (state IN ('building', 'ready', 'failed', 'pruned')),
    output      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX image_builds_by_family ON image_builds (kind, version, created_at);

-- The build a family serves now and the one it served before. Both are build
-- IDs; an empty string means none.
CREATE TABLE image_pointers (
    kind     TEXT NOT NULL CHECK (kind IN ('php', 'nginx')),
    version  TEXT NOT NULL,
    current  TEXT NOT NULL DEFAULT '',
    previous TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (kind, version)
);
