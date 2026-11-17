-- Firewall (development plan M4).
--
-- `fw_rules` and `fw_blocklist` are the operator's rules; built-in rules are
-- generated and never stored. `fw_state` holds the last confirmed ruleset as
-- rendered iptables-restore text, which `panel firewall restore` replays at boot
-- without the daemon. `fw_pending` is at most one apply awaiting confirmation,
-- with the owned-chain state to return to; a row surviving a restart is rolled back.

CREATE TABLE fw_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    port_start INTEGER NOT NULL CHECK (port_start BETWEEN 1 AND 65535),
    port_end   INTEGER NOT NULL CHECK (port_end BETWEEN 1 AND 65535 AND port_end >= port_start),
    protocol   TEXT NOT NULL CHECK (protocol IN ('tcp', 'udp', 'both')),
    source     TEXT NOT NULL DEFAULT '',
    comment    TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE fw_blocklist (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    addr    TEXT NOT NULL UNIQUE,
    comment TEXT NOT NULL DEFAULT ''
);

CREATE TABLE fw_state (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    enabled      INTEGER NOT NULL DEFAULT 0,
    confirmed_v4 TEXT NOT NULL DEFAULT '',
    confirmed_v6 TEXT NOT NULL DEFAULT '',
    confirmed_at TEXT NOT NULL DEFAULT ''
);

INSERT INTO fw_state (id) VALUES (1);

CREATE TABLE fw_pending (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    token      TEXT NOT NULL,
    prev_v4    TEXT NOT NULL,
    prev_v6    TEXT NOT NULL,
    new_v4     TEXT NOT NULL,
    new_v6     TEXT NOT NULL,
    started_at TEXT NOT NULL,
    deadline   TEXT NOT NULL
);
