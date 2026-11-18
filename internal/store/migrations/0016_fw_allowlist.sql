-- Firewall allow list: addresses accepted on every port, ahead of the blocklist.

CREATE TABLE fw_allowlist (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    addr    TEXT NOT NULL UNIQUE,
    comment TEXT NOT NULL DEFAULT ''
);
