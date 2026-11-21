-- Subpath reverse-proxy routes (one site with PHP and proxy routes).
--
-- A site is no longer one of three mutually exclusive kinds. It may serve files
-- from a folder, run PHP, and proxy one or more subpaths. `sites.kind` stays as
-- the primary serving mode; the routes are the new truth about what is proxied.
--
-- `sites.proxy_target` is deliberately NOT dropped: a daemon rolled back to a
-- build that predates this migration still reads a proxy site's single target
-- from it, exactly as before. This build neither reads it as the truth nor
-- writes it (new rows carry ''), but the column and its data stay.
--
-- `strip_prefix` is 1 by default: the common case is an app mounted under /api
-- that does not know its prefix, so /api/users must arrive as /users. A route
-- that keeps the prefix (the app expects /api/users) sets it to 0.

CREATE TABLE site_proxy_routes (
    domain       TEXT NOT NULL,
    position     INTEGER NOT NULL,
    path         TEXT NOT NULL,
    target       TEXT NOT NULL,
    strip_prefix INTEGER NOT NULL DEFAULT 1 CHECK (strip_prefix IN (0, 1)),
    PRIMARY KEY (domain, path),
    UNIQUE (domain, position)
);

-- The upgrade: every reverse-proxy site's single target becomes one route on "/".
-- An old site had nothing else proxied, so "/" is the behaviour it had. Its
-- prefix was kept (an old proxy_pass carried no URI), hence strip_prefix 0.
INSERT INTO site_proxy_routes (domain, position, path, target, strip_prefix)
SELECT domain, 0, '/', proxy_target, 0
FROM sites
WHERE kind = 'proxy' AND proxy_target <> '';
