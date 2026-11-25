-- metrics.db: sampled history. Timestamps are unix seconds aligned to the tier
-- step. Every tier of a family has the same columns, so a rollup is one
-- INSERT ... SELECT with aggregates.

CREATE TABLE series_keys (
    id   INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,            -- mount | service | site
    name TEXT NOT NULL,
    UNIQUE (kind, name)
);

CREATE TABLE host_10s (
    ts            INTEGER PRIMARY KEY,
    cpu           REAL,
    cpu_max       REAL,
    load1         REAL,
    load1_max     REAL,
    load5         REAL,
    load15        REAL,
    mem_used      INTEGER,
    mem_avail     INTEGER,
    mem_avail_min INTEGER,
    swap_used     INTEGER,
    net_rx        REAL,
    net_tx        REAL
);
CREATE TABLE host_1m (
    ts            INTEGER PRIMARY KEY,
    cpu           REAL,
    cpu_max       REAL,
    load1         REAL,
    load1_max     REAL,
    load5         REAL,
    load15        REAL,
    mem_used      INTEGER,
    mem_avail     INTEGER,
    mem_avail_min INTEGER,
    swap_used     INTEGER,
    net_rx        REAL,
    net_tx        REAL
);
CREATE TABLE host_5m (
    ts            INTEGER PRIMARY KEY,
    cpu           REAL,
    cpu_max       REAL,
    load1         REAL,
    load1_max     REAL,
    load5         REAL,
    load15        REAL,
    mem_used      INTEGER,
    mem_avail     INTEGER,
    mem_avail_min INTEGER,
    swap_used     INTEGER,
    net_rx        REAL,
    net_tx        REAL
);

-- rd_bps / wr_bps are NULL when the device could not be mapped to diskstats.
CREATE TABLE disk_10s (
    ts           INTEGER NOT NULL,
    key          INTEGER NOT NULL,
    used_pct     REAL,
    used_pct_max REAL,
    used         INTEGER,
    rd_bps       REAL,
    wr_bps       REAL,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;
CREATE TABLE disk_1m (
    ts           INTEGER NOT NULL,
    key          INTEGER NOT NULL,
    used_pct     REAL,
    used_pct_max REAL,
    used         INTEGER,
    rd_bps       REAL,
    wr_bps       REAL,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;
CREATE TABLE disk_5m (
    ts           INTEGER NOT NULL,
    key          INTEGER NOT NULL,
    used_pct     REAL,
    used_pct_max REAL,
    used         INTEGER,
    rd_bps       REAL,
    wr_bps       REAL,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;

-- state: running=1 starting=2 stopping=3 stopped=4 failed=5 missing=6 unknown=7.
-- In a rollup it is the worst state seen and up_frac the share of samples that
-- were running.
CREATE TABLE svc_30s (
    ts      INTEGER NOT NULL,
    key     INTEGER NOT NULL,
    state   INTEGER NOT NULL,
    up_frac REAL NOT NULL,
    cpu     REAL,
    cpu_max REAL,
    rss     INTEGER,
    rss_max INTEGER,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;
CREATE TABLE svc_1m (
    ts      INTEGER NOT NULL,
    key     INTEGER NOT NULL,
    state   INTEGER NOT NULL,
    up_frac REAL NOT NULL,
    cpu     REAL,
    cpu_max REAL,
    rss     INTEGER,
    rss_max INTEGER,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;
CREATE TABLE svc_5m (
    ts      INTEGER NOT NULL,
    key     INTEGER NOT NULL,
    state   INTEGER NOT NULL,
    up_frac REAL NOT NULL,
    cpu     REAL,
    cpu_max REAL,
    rss     INTEGER,
    rss_max INTEGER,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;

-- rt_hist / urt_hist: 20 uvarint bin counts (internal/hist).
CREATE TABLE site_1m (
    ts       INTEGER NOT NULL,
    key      INTEGER NOT NULL,
    req      INTEGER NOT NULL,
    s2       INTEGER NOT NULL,
    s3       INTEGER NOT NULL,
    s4       INTEGER NOT NULL,
    s5       INTEGER NOT NULL,
    bytes    INTEGER NOT NULL,
    rt_hist  BLOB,
    urt_hist BLOB,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;
CREATE TABLE site_15m (
    ts       INTEGER NOT NULL,
    key      INTEGER NOT NULL,
    req      INTEGER NOT NULL,
    s2       INTEGER NOT NULL,
    s3       INTEGER NOT NULL,
    s4       INTEGER NOT NULL,
    s5       INTEGER NOT NULL,
    bytes    INTEGER NOT NULL,
    rt_hist  BLOB,
    urt_hist BLOB,
    PRIMARY KEY (ts, key)
) WITHOUT ROWID;

CREATE TABLE meta (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);
