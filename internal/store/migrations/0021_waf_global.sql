-- The global firewall (WAF): one JSON document that applies to every site, the
-- shape of waf.Global. There is no row until an operator changes something; the
-- defaults apply until then.

CREATE TABLE waf_global (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    json       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
