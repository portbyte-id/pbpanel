-- DNS-01 renewal (development plan M6, step 6.6): the certificate row records
-- which stored DNS provider issued it, so the renewal job can order again with
-- the same provider. Empty for upload and HTTP-01 certificates.
ALTER TABLE certs ADD COLUMN provider TEXT NOT NULL DEFAULT '';
