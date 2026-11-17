-- TLS (development plan M6, step 6.2).
--
-- `certs` holds one row per site domain: a site has one active certificate, and
-- issuing or uploading another replaces it. The PEM files live on disk under
-- <root>/panel/ssl/sites/<domain>/; the row is the index and the renewal log.
-- `domain` is deliberately not a foreign key to `sites`: the panel's own
-- certificate (step 6.7) is stored here too and belongs to no site. The code
-- deletes a site's certificate when it deletes the site.
--
-- Every secret is stored sealed by internal/secretbox (`v1:<keyid>:<base64>`);
-- no column in this file ever holds a plaintext key or token.

CREATE TABLE certs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    domain              TEXT NOT NULL UNIQUE,
    source              TEXT NOT NULL CHECK (source IN ('upload', 'http01', 'dns01')),
    names               TEXT NOT NULL DEFAULT '[]',
    not_before          TEXT NOT NULL,
    not_after           TEXT NOT NULL,
    issuer              TEXT NOT NULL DEFAULT '',
    serial              TEXT NOT NULL DEFAULT '',
    last_renewal_at     TEXT,
    last_renewal_result TEXT NOT NULL DEFAULT '' CHECK (last_renewal_result IN ('', 'ok', 'failed')),
    last_renewal_error  TEXT NOT NULL DEFAULT '',
    redirect_http       INTEGER NOT NULL DEFAULT 0 CHECK (redirect_http IN (0, 1)),
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

-- The renewal job scans by expiry.
CREATE INDEX certs_not_after ON certs (not_after);

-- One ACME account per CA directory: an account registered with the staging CA
-- means nothing to the production one.
CREATE TABLE acme_accounts (
    directory_url       TEXT PRIMARY KEY,
    email               TEXT NOT NULL DEFAULT '',
    key_sealed          TEXT NOT NULL,
    registration_sealed TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL
);

-- DNS-01 provider credentials, named by the operator. `kind` selects the
-- implementation (step 6.5); it is not constrained here so a new provider needs
-- no migration.
CREATE TABLE dns_providers (
    name               TEXT PRIMARY KEY,
    kind               TEXT NOT NULL,
    credentials_sealed TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
