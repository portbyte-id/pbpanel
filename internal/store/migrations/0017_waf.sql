-- Per-site firewall settings (WAF): one JSON document per site, the shape of
-- waf.Config. A site without a row has the firewall off.
--
-- `domain` is not a declared foreign key: the store does not enable
-- foreign_keys, so the sites manager deletes a site's row itself in the same
-- transaction that removes the site.

CREATE TABLE site_waf (
    domain     TEXT PRIMARY KEY,
    json       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
