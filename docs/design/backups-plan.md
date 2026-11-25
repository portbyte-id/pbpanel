# Site and panel backups: plan

Status: **plan, not started; decisions unratified.** Migration number reserved: `0023`. Scope:
Ubuntu first; EL (Rocky/RHEL) stays in **M9**.

Written after reading `internal/store` (`store.go`, `migrate.go`, all of `migrations/0001`-`0021`),
`internal/secretbox/secretbox.go`, `internal/tls` (`paths.go`, `files.go`, `store.go`),
`internal/sites` (`manager.go`, `files.go`, `reconcile.go`), `internal/nginx/paths.go`,
`internal/install/layout.go`, `internal/cron` (`builtin.go`, `schedule.go`), `internal/jobs/jobs.go`,
`internal/settings/keys.go`, `internal/fm/archive_linux.go` + `fs_linux.go`, `cmd/panel`
(`serve.go`, `m7.go`, `tls.go`, `nginx.go`), `docs/plan.md`, `docs/development-plan.md` §12,
`docs/RUNBOOK.md`, and the `modernc.org/sqlite@v1.62.0` sources in the module cache. Claims marked
**(assumed)** were not checked in code.

## 1. Goal

The operator can take, keep and restore byte-exact backups:

- **Site backup**: one site's web root, its on-disk nginx site file and includes, its certificate
  and key, and its database rows (site, aliases, proxy routes, WAF, PHP pool, cert row). On demand
  or on a schedule, to a local directory and/or an S3-compatible bucket (S3, R2, B2, MinIO).
- **Panel backup**: `panel.db` through SQLite's online backup API plus `data/secret.key`, enough to
  rebuild the panel's state on a **fresh host**.
- Retention (keep N daily / weekly / monthly), encryption of every archive, a disk-space preflight,
  restore of a whole site or of chosen files/folders, and a periodic **test restore** that proves an
  archive opens and every file checks out.
- Runs as jobs (`internal/jobs`, SSE output); schedules through a cron built-in; failures go to
  `notify.Emitter` (sibling plan `docs/design/notifications-plan.md`).
- UI: a **Backups** page (targets, plans, archives, key) and a **Backups** section in the site
  detail page.

What does **not** change: the confedit apply pipeline (a restored site file still passes `nginx -t`
or is rolled back), the "database first" rule (`docs/plan.md` §2: rows are committed, then rendered),
secretbox's format, the cron table (no new columns), the nginx/PHP runtimes (they are rebuilt from the
Store, never backed up), and the scope line in `docs/plan.md` decision 25: **databases are out of
scope**, so no MySQL/Postgres dumps — §4.6 leaves a hook.

**Backups vs the "config as code" export** (sibling plan): the export is human-readable YAML of
*definitions* (sites, WAF, cron), secret-free, diffable, meant to be read and re-applied. A backup is
an encrypted, byte-exact, restorable copy of *state*: web content, private keys, raw operator-edited
site files, sealed secrets. They overlap on the site definition. The backup must **not** use the
export format for its rows (it uses SQLite rows at a recorded schema version, §4.2), so a change to
the export never breaks old archives. The export may later be offered as one optional extra member
of a panel archive, nothing more.

## 2. Current state

| Area | What exists | Path |
|---|---|---|
| Backups | **Nothing.** Only a row in the cross-cutting table ("`panel.db` online backup via SQLite backup API to `/www/panel/data/backups/` with retention", M0 manual / M7 scheduled) and a two-line runbook note. No code, no `backups` dir in the layout | `docs/development-plan.md:850`, `docs/RUNBOOK.md:89-92` |
| SQLite | `modernc.org/sqlite v1.62.0`, WAL, 4-conn pool. The driver conn has `NewBackup(dstURI) (*sqlite.Backup, error)` with `Step/Remaining/PageCount/Finish` (verified in the module cache, `conn.go:1425`, `backup.go`), reachable through `sql.Conn.Raw`. `store.Open` runs forward migrations on open | `internal/store/store.go`, `migrate.go` |
| Secrets | `data/secret.key` (32 B, 0600, dir 0700) feeds secretbox: HKDF → AES-256-GCM, value `v1:<keyid>:<b64>`, aad = `table.column:rowkey` (`tls/store.go:272`). The aad names rows, not hosts, so **panel.db + the same secret.key open on any host**. secretbox's own doc says a db backup leaking without the key must not leak secrets | `internal/secretbox/secretbox.go`, `internal/install/layout.go:96` |
| Sealed columns | `acme_accounts.key_sealed`, `.registration_sealed`, `dns_providers.credentials_sealed`. The challenge page also derives keys from secret.key | `migrations/0012_tls.sql`, `cmd/panel/nginx.go:69` |
| Plaintext secrets in db | Settings marked `Secret` (`waf.turnstile.secret`, ACME EAB HMAC key) are only masked in the UI, stored plain in `settings` **(assumed from `settings.go:72` "masks while typing"; no secretbox call in `internal/settings`)**; `admin` password hash; `sessions` | `internal/settings/keys.go:398,437` |
| Site rows | `sites` (domain PK, kind, root, proxy_target, php_version, rewrite), `site_aliases`, `site_proxy_routes`, `site_waf` (JSON), `php_pools` (domain PK, JSON), `certs` (domain UNIQUE, `provider` names a `dns_providers` row). No foreign keys enforced | `migrations/0007,0008,0012,0013,0017,0019,0020` |
| Web root | Default `/www/services/sites/<domain>`; may be any folder in the `sites.web_roots` allowlist; app workdirs default inside it | `sites/manager.go:239,364`, `internal/webroots` |
| Site files on disk | `/www/panel/nginx/sites/<domain>.conf` and `nginx/includes/<domain>/`, operator-editable raw (`sites/files.go:44`, `ErrDetached`), so the disk is **not** derivable from rows alone | `internal/nginx/paths.go:98-110` |
| Certificates | `/www/panel/ssl/sites/<domain>/{fullchain,privkey}.pem`, key 0600 plaintext on disk | `internal/tls/paths.go`, `files.go` |
| Cron | `cron_jobs` has **no site column**; built-ins are a name → `BuiltinFunc(ctx, io.Writer)` map seeded by `EnsureDefaults` from the package var `defaultJobs`; `Schedule.Next` exists | `internal/cron/builtin.go`, `schedule.go:185`, `cmd/panel/tls.go:123` |
| Jobs | `Manager.Start(ctx, kind, func(ctx, *Writer) error)`, `ReportProgress`, `ProgressReader` | `internal/jobs/jobs.go:201`, `progress.go` |
| Safe extraction | fm's `extractRun` writes beneath a directory descriptor (`openat`, `O_NOFOLLOW`, `O_EXCL`), rejects escapes, devices, links through archive-made symlinks, and lying sizes; a tree walk feeds tar/zip sinks | `internal/fm/archive_linux.go:23-120,486-700` |
| Disk space | `unix.Statfs` already used | `internal/fm/fs_linux.go:353`, `internal/sysinfo/source_linux.go` |
| Dependencies | Only lego, `x/crypto`, `x/net`, `x/sys`, sqlite. No S3, zstd or age library | `go.mod` |
| www account | Fixed UID/GID 1500 on every host, so numeric ownership in a tar is portable | `internal/install/layout.go:37` |

## 3. Decisions (unratified)

| # | Decision | Recommendation |
|---|---|---|
| D1 | Archive engine: restic/kopia-style chunked dedup, or full archives | **Full `tar` + zstd per run, GFS retention.** Dedup means a content-addressed repository, index, locking, prune and repair: a project of its own, and its failure modes (corrupt index) are worse than the problem. Typical panel sites are MB-GB; a daily full fits. Revisit only with measured sizes (§7 R1). Shelling out to a downloaded restic binary is rejected: a third-party root binary, a second key model |
| D2 | Compression library | **`github.com/klauspost/compress/zstd`** (pure Go, no cgo, one module). gzip from stdlib is the fallback if the dependency is refused: ~30% larger, ~3x slower |
| D3 | S3 client | **Vendor a minimal SigV4 client** in `internal/backup/s3` (PUT, multipart create/upload/complete/abort, GET with Range, HEAD, DELETE, ListObjectsV2; path-style and virtual-host). ~600 lines + tests against MinIO. `aws-sdk-go-v2` or `minio-go` would multiply `go.mod` for four verbs. Test matrix: MinIO (CI, Docker), R2/B2/AWS by hand at the host gate |
| D4 | Encryption | **Every archive is encrypted, local ones too** (site archives carry `privkey.pem`; panel archives carry `secret.key`). Own small format from `x/crypto` (already a dep): per-archive random 32 B data key, body in 64 KiB XChaCha20-Poly1305 chunks (STREAM: counter nonce + last-chunk flag, header as aad), data key wrapped by the backup key. No `age` dependency |
| D5 | Backup key and `secret.key` | **The backup key is derived from an operator passphrase (argon2id), never from `secret.key`**, so a fresh host can open an archive with only the passphrase. The derived key is stored sealed (secretbox) for unattended runs; the passphrase itself is never stored. Panel archives **include `secret.key` inside the encryption**, so a restore brings back ACME accounts, DNS credentials and challenge keys intact. No backups run until a passphrase is set. Shown once: "lose this passphrase and every archive is unreadable" |
| D6 | Passphrase change | **Keyring, not re-encryption.** A change adds a new key ID; old IDs stay sealed in `backup_keys` so old archives still open on this host. On a fresh host, an old archive needs the passphrase it was made with; the archive header names its key ID and creation date to make that obvious |
| D7 | Site rows in the archive | **A trimmed copy of `panel.db`** (online backup → delete every row not belonging to the site, drop global/secret tables' rows → `VACUUM`), with its `schema_migrations` version. Restore opens it with the panel's own migrations, bringing old archives forward for free, then copies rows. Archives from a **newer** schema are refused. A test fails when a migration adds a table not classified site-scoped or global in `backup/tables.go` |
| D8 | Schedules | **Per-plan cron expression** in `backup_plans`, evaluated by **one** built-in `backup.scheduler` (job `backups`, `*/15 * * * *`, root) using `cron.Schedule.Next` against `last_run_at`. Per-plan cron rows are impossible without changing built-ins to take arguments. A second built-in `backup.verify` (job `backup-verify`, weekly `50 4 * * 0`) runs test restores |
| D9 | Cron entries "tied to a site" | **None exist structurally** (`cron_jobs` has no site column). Include cron jobs whose `workdir` or `script_path` lies under the site root as `related`; restore offers them, **created disabled**, only if the name is free. Same treatment for apps whose `workdir` is under the root |
| D10 | Restoring a site that exists | **Restore replaces it** after a confirm naming what is lost, taking an automatic pre-restore backup of the current site first. Restore-as-another-domain is out of scope |
| D11 | Panel restore path | **CLI only, daemon stopped**: `pbp restore panel <file|s3-url>` on a host after `pbp install`. Swapping `panel.db` under the running daemon is not safe. Site restores are UI jobs |
| D12 | Single-file restore without downloading the whole archive | **Encrypted sidecar index** (`<archive>.idx`: the manifest) is uploaded beside each archive; browsing reads only it. Extracting files streams the archive once (tar has no random access); for remote targets the archive is downloaded to staging first, behind the disk preflight |
| D13 | Live-site consistency | **No snapshot** (LVM/btrfs out of scope). Files are read once; a file whose size/mtime changed during read is recorded as `changed` in the manifest and the run ends `succeeded with warnings`. `panel.db` is always consistent (backup API) |

## 4. Design

### 4.1 Package layout

`internal/backup` (Linux-only parts behind `_linux.go`, like `internal/fm`):

- `backup.go` - `Service`: plans, runs, retention, one-run-at-a-time mutex, `notify.Emitter`.
- `format/` - container: header, key wrap, STREAM chunks, zstd, tar writer/reader, manifest.
- `keys.go` - argon2id KDF, keyring sealed with secretbox (aad `backup_keys.key:<id>`).
- `target.go` - `Target` interface `{Put(ctx,key,r,size), Get(ctx,key,off) io.ReadCloser, List(ctx,prefix), Delete(ctx,key), Stat(ctx,key), Free(ctx) (int64, bool)}`; `local.go`, `s3/`.
- `site.go`, `panel.go` - the `Part`s (§4.6); `tables.go` - table classification.
- `restore_linux.go` - reuses fm's descriptor-based extractor, refactored to accept a `*tar.Reader`
  and a root fd (an `fm` change, done in step 4, behaviour unchanged for fm).
- `retention.go`, `verify.go`, `preflight_linux.go`.

### 4.2 Archive format (`.pbk`)

```
"PBBK\x00\x01"                      magic + format version
u32 len | header JSON               authenticated as aad of every chunk
  {format:1, kind:"site"|"panel", name, created, panel_version, schema_version,
   host, key_id, kdf:{alg:"argon2id",t,m,p,salt}, wrapped_key, zstd:true}
STREAM chunks: 64 KiB plaintext -> XChaCha20-Poly1305, nonce = prefix||counter||last
  plaintext = zstd(tar)
```

Tar members, in order (manifest first so verification can stream):

| Member | Site | Panel |
|---|---|---|
| `manifest.json` | entries (path, type, mode, uid, gid, size, mtime, sha256, `changed`), related cron/apps, root path, aliases, totals | same |
| `db/site.sqlite` / `db/panel.db` | trimmed copy (D7) | full online backup |
| `secret.key` | - | yes (D5) |
| `nginx/site.conf`, `nginx/includes/*` | on-disk files as they are | - (rendered from rows on boot) |
| `ssl/fullchain.pem`, `ssl/privkey.pem` | if a cert exists | the panel's own `panel.crt/key` |
| `root/...` | web root: files, dirs, symlinks (never followed), hardlinks; no devices/fifos/sockets | - |

Object names: `<prefix>/<kind>/<name>/<UTC yyyymmddThhmmssZ>-<rand4>.pbk` and `.idx`. The sidecar
index is the manifest alone in the same container format.

### 4.3 Data model: `migrations/0023_backups.sql`

```sql
CREATE TABLE backup_keys (          -- D5/D6 keyring
    id          TEXT PRIMARY KEY,   -- short hash of the derived key
    kdf         TEXT NOT NULL,      -- JSON params + salt
    key_sealed  TEXT NOT NULL,      -- secretbox, aad backup_keys.key:<id>
    current     INTEGER NOT NULL DEFAULT 0 CHECK (current IN (0,1)),
    created_at  TEXT NOT NULL
);
CREATE TABLE backup_targets (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT NOT NULL UNIQUE,
    kind           TEXT NOT NULL CHECK (kind IN ('local','s3')),
    config         TEXT NOT NULL,   -- JSON: dir | endpoint, region, bucket, prefix, path_style, access_key_id
    secret_sealed  TEXT NOT NULL DEFAULT '',  -- S3 secret access key, aad backup_targets.secret:<name>
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE TABLE backup_plans (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    scope         TEXT NOT NULL CHECK (scope IN ('panel','site')),
    site_domain   TEXT NOT NULL DEFAULT '',  -- not a FK; sites.Delete asks what to do with the plan
    target_ids    TEXT NOT NULL,             -- JSON array; one archive is written to each
    schedule      TEXT NOT NULL DEFAULT '40 2 * * *',
    keep_daily    INTEGER NOT NULL DEFAULT 7,
    keep_weekly   INTEGER NOT NULL DEFAULT 4,
    keep_monthly  INTEGER NOT NULL DEFAULT 6,
    excludes      TEXT NOT NULL DEFAULT '[]', -- root-relative globs, e.g. "cache/**"
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    last_run_at   TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
CREATE TABLE backup_archives (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    plan_id        INTEGER,            -- NULL for an ad-hoc or pre-restore archive
    target_id      INTEGER NOT NULL,
    scope          TEXT NOT NULL CHECK (scope IN ('panel','site')),
    name           TEXT NOT NULL,      -- domain or "panel"
    object_key     TEXT NOT NULL,
    key_id         TEXT NOT NULL,
    size           INTEGER NOT NULL DEFAULT 0,
    sha256         TEXT NOT NULL DEFAULT '',  -- of the encrypted object
    entries        INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL CHECK (status IN ('running','ok','warnings','failed','pruned','missing')),
    error          TEXT NOT NULL DEFAULT '',
    job_id         TEXT NOT NULL DEFAULT '',
    verified_at    TEXT,
    verify_result  TEXT NOT NULL DEFAULT '' CHECK (verify_result IN ('','ok','failed')),
    verify_error   TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    UNIQUE (target_id, object_key)
);
CREATE INDEX backup_archives_name ON backup_archives (scope, name, created_at);
```

The archive table is a cache of what is on the targets: a **rescan** (`POST .../targets/{id}/rescan`)
lists the target, reads each `.idx` header, and rebuilds rows. That is how a fresh host learns
which archives a bucket holds.

### 4.4 Settings keys (group `Backups`, `internal/settings/keys.go`)

| Key | Type | Default | Notes |
|---|---|---|---|
| `backup.local_dir` | path | `/www/panel/data/backups` | default `local` target and staging (`.staging/`); created 0700. Matches `development-plan.md:850` |
| `backup.min_free_percent` | int 1-50 | `10` | preflight floor on staging and restore filesystems |
| `backup.zstd_level` | int 1-19 | `3` | |
| `backup.s3_part_mib` | int 5-512 | `64` | multipart part size |
| `backup.verify_sample` | int 0-100 | `100` | percent of entries whose sha256 a verify recomputes; 100 = all |

Targets, plans and the key are rows, not settings (they hold sealed secrets and are many).

### 4.5 API routes (`internal/httpapi/handlers_backup.go`; `docs/API.md` updated)

```
GET    /api/backups/key                     status: set?, current id, created
PUT    /api/backups/key                     {passphrase, confirm}            -> new current key
GET    /api/backups/targets | POST          CRUD; secret write-only, never returned
PUT    /api/backups/targets/{id} | DELETE   delete refused while a plan uses it
POST   /api/backups/targets/{id}/test       put/stat/get/delete a probe object
POST   /api/backups/targets/{id}/rescan     job backup.rescan
GET    /api/backups/plans | POST, PUT /{id}, DELETE /{id}
POST   /api/backups/plans/{id}/run          job backup.run -> {job}
GET    /api/backups/archives?scope=&name=   list
GET    /api/backups/archives/{id}/entries?path=   browse via .idx (opened with the stored key)
POST   /api/backups/archives/{id}/verify    job backup.verify
POST   /api/backups/archives/{id}/restore   {mode:"site"|"paths", paths[], dest:"original"|"folder", folder} -> job backup.restore
GET    /api/backups/archives/{id}/download  the encrypted .pbk, streamed
DELETE /api/backups/archives/{id}           removes the object and .idx
GET    /api/sites/{domain}/backups          plans + archives for one site (Site Backups section)
```

Every unsafe route is caught by the existing audit middleware (`httpapi/auditmw.go`); the
route-enumeration audit test covers them. Panel restore has no route (D11).

CLI (`cmd/panel`): `pbp backup panel [--target NAME]` (manual, e.g. before an upgrade) and
`pbp restore panel <path|s3://bucket/key> [--endpoint ... ]` (prompts for passphrase and S3 secret
on the tty, refuses while `panel.service` is active).

### 4.6 Parts and the database hook

```go
type Part interface {
    Name() string                                   // "webroot", "nginx", "tls", "rows", ...
    Estimate(ctx context.Context) (int64, error)    // bytes, for the preflight
    Write(ctx context.Context, tw *format.TarWriter, m *format.Manifest) error
    Restore(ctx context.Context, tr *format.TarReader, opt RestoreOptions) error
}
```

Site parts: `rows`, `nginx`, `tls`, `webroot`. Panel parts: `paneldb`, `secretkey`, `panelcert`.
Databases are out of scope (`docs/plan.md` decision 25); a future DB feature adds a `dbdump` part
(e.g. `mysqldump --single-transaction` into `db/<name>.sql.zst`) without a format change: unknown
members are skipped with a warning by older restorers.

### 4.7 Flows

**Backup run** (`backup.run`): preflight (sum of `Estimate` x1.1 must fit on staging with
`min_free_percent` left) → stream parts into `format` → write to `.staging/` → for each target:
local = rename into place; s3 = multipart upload + `.idx` → row `ok|warnings` → retention prune per
target (GFS: newest per day/ISO week/month, keep the N newest buckets of each; never prune the newest
`ok` archive, never prune while a restore reads it) → `notify` success (Resolved=true on key
`backup:<plan>`) or `backup.failed` (critical). Staging is removed on every exit path. A
direct-stream S3 upload without staging is a later optimisation.

**Site restore** (`backup.restore`, mode `site`): automatic pre-restore archive of the current site
(D10) → download/decrypt to staging if remote, preflight on the web root's filesystem → open
`db/site.sqlite`, migrate it forward (D7), refuse newer → extract `root/` into a new sibling folder
`<root>.restore-<ts>` via fm's extractor (uid/gid/mode kept; www is 1500 everywhere) → in one
sites-manager critical section: replace rows in a transaction, write cert files
(`tls.WriteSiteFiles`), apply `nginx/site.conf` + includes + WAF files through **one confedit
change** (`nginx -t`, reload, rollback) → on success swap folders (rename old to trash under
`/www/panel/trash`, new into place), reconcile the PHP pool (`sites.ReconcilePHP`); on failure,
roll back rows and drop the new folder. Related cron/apps (D9) are created disabled.

**Paths restore**: browse from `.idx`, pick files/dirs, restore into the original path (overwrite
confirmed) or into a chosen folder through fm's policy (deny list applies).

**Panel restore on a fresh host** (CLI): `pbp install` → `pbp restore panel s3://...` (asks for
passphrase) → verify header, key, manifest → write `panel.db` (via `NewRestore` into a new file, then
rename) and `secret.key` 0600 → keep the host-specific settings that would lock the operator out
(`panel.bind`, `panel.domain`) **unless** `--keep-network` is passed; print what was reset →
`systemctl start panel`. Boot reconcile then reports sites as needing their runtime: the operator
installs nginx/PHP versions from the Store **(assumed: reconcile fails soft without nginx; checked in
step 0)**, then runs "Restore all sites from latest" (a Backups page action: one job, each site in
turn, report per site).

**Verify** (`backup.verify`, and the weekly built-in on the newest `ok` archive per plan and
target): download → authenticate every chunk → stream tar, recompute sha256 of `verify_sample`% of
entries against the manifest → open `db/*.sqlite` copy and run `PRAGMA integrity_check` and the
forward migration in a temp dir → for panel archives check `secret.key` opens a sealed value
from the restored db. Nothing touches live files. Result on the row; `backup.verify_failed`
(critical) on failure, resolved on the next pass.

## 5. Steps

Each step ends green on the gate (`go test ./cmd/... ./internal/...` in WSL, CI golangci-lint on
changed code, vitest for UI steps) and is committed on its own.

### Step 0: spike on the Ubuntu sandbox (no product code)

- Online backup via `sql.Conn.Raw` → `NewBackup` while the daemon writes (jobs, audit): page counts,
  time for a 50 MB db, `integrity_check` of the copy, WAL state after.
- Trim recipe for D7 on a copy of the real `panel.db`: which tables are site-scoped, size after `VACUUM`.
- zstd level 3 vs gzip on a real WordPress root (size, MB/s, RSS); argon2id parameters that cost
  ~1 s and ≤64 MiB on the sandbox.
- SigV4 against MinIO in Docker and one real provider the operator has (R2 or B2): path-style vs
  virtual-host, multipart minimums, ListObjectsV2 pagination, `x-amz-content-sha256: UNSIGNED-PAYLOAD`
  acceptance, B2/R2 quirks (checksum headers, no `x-amz-storage-class`).
- Boot with a restored `panel.db` on a host without nginx installed: what reconcile and the UI show.

Done when each item has a recorded answer in this file and D1-D3, D7 are confirmed or amended.

### Step 1: container format and keys

- `internal/backup/format`: header, STREAM writer/reader, zstd, tar wrapper, manifest types.
  `internal/backup/keys.go`: argon2id, keyring, seal/open via secretbox.
- Migration `0023_backups.sql` (whole schema at once) + migration test.
- Tests: round trip; truncated stream, swapped/reordered/dropped chunk, flipped header byte, wrong
  key id, wrong passphrase all fail closed; last-chunk flag enforced; fuzz the reader.
- Done when a 1 GiB synthetic stream round-trips at bounded memory (<64 MiB).

### Step 2: targets

- `local.go` (atomic rename, 0600 files, 0700 dirs, `Free` via Statfs) and `s3/` (SigV4, multipart,
  retries with backoff on 5xx/429, abort on cancel). Target CRUD in `Service`, secrets sealed.
- Tests: SigV4 against the AWS published test vectors; local target unit tests; a real-Docker MinIO
  integration test in the style of the existing real-Docker tests (skipped without Docker).
- Done when `Target` conformance tests pass on both.

### Step 3: panel backup

- `paneldb`, `secretkey`, `panelcert` parts; `backup.run` for `scope=panel`; preflight; `.idx`;
  retention (`retention.go`, table tests over synthetic timelines incl. DST and month ends).
- `pbp backup panel` CLI.
- Tests: backup during concurrent writes yields an `integrity_check`-clean db; secret.key present only
  inside the encrypted body (grep the object for the key bytes); retention keeps exactly the GFS set.

### Step 4: site backup

- `tables.go` classification + the unclassified-table test; `rows` part (trim, D7); `nginx`, `tls`,
  `webroot` parts (walk with `O_NOFOLLOW`, excludes, `changed` detection, related cron/apps).
- Refactor fm's tree walk and `extractRun` into a reusable form (fm tests unchanged and green).
- Tests: archive of a fixture site contains exactly the expected members; symlink pointing outside the
  root stored as a link, never followed; device files skipped with a warning; excludes honoured.

### Step 5: restore and verify

- `backup.restore` (site and paths modes), `backup.verify`, `backup.rescan`; pre-restore archive;
  forward-migration of old `site.sqlite`; newer-schema refusal.
- `pbp restore panel` CLI (daemon-stopped check, network-settings guard).
- Tests: restore into a temp root reproduces every sha256, mode, uid/gid; hostile archive (escape,
  `..`, absolute, link-through) rejected by the shared extractor; a site file that fails `nginx -t`
  rolls back rows and leaves the old root serving (fake confedit in unit tests, real nginx in the
  existing integration harness); verify flags a corrupted object.

### Step 6: scheduling, notifications, HTTP

- Built-ins `backup.scheduler` and `backup.verify` added to `cron.defaultJobs` and registered next to
  `tls.renew` (`cmd/panel/tls.go:123` grows into a merged map); wiring in `cmd/panel/serve.go`.
- `notify.Emitter` dependency (no-op until the sibling lands): `backup.failed` critical,
  `backup.succeeded` info, `backup.verify_failed` critical; key `backup:<plan>` / `backup-verify:<plan>`.
- `handlers_backup.go`, `docs/API.md`; site delete asks whether to keep or delete the site's plan
  (`sites.Delete` gets a hook, archives are always kept).
- Tests: scheduler fires a plan once per due slot, skips while one runs, catches up one missed slot
  after downtime (not all of them); handler tests incl. secrets never echoed; audit-route test green.

### Step 7: UI

- `web/src/pages/BackupsPage.tsx`: key card (set/change, recovery warning), targets (with Test),
  plans (schedule, retention, targets, excludes, Run now), archives table (status, size, verified,
  Verify/Restore/Download/Delete), "Restore all sites from latest".
- Site detail: new section `backups` in `siteSectionList` (`SiteDetailPage.tsx:74`): the site's plan,
  its archives, Restore site, Browse (tree from `entries`, reusing the Files page list components)
  → restore paths.
- Job output through the existing jobs SSE view.
- vitest for each page/section; restore confirm names what is replaced.

## 6. Host gate: `scripts/smoke/11-backups.sh`

On the current Ubuntu sandbox (re-confirm the IP) with MinIO started in Docker by the script:

1. Set passphrase; add `local` and `minio` targets; Test both.
2. Create a PHP site with WordPress-like content (5k files, a symlink, a 200 MB file), a cert
   (upload), WAF on, an edited include, a related cron job.
3. Run the site plan and the panel plan to both targets; `ok`; objects exist; `.idx` exists; the
   plaintext of `privkey.pem` and `secret.key` does not appear in any object.
4. Delete 3 files, edit the site file, change WAF; restore paths (2 files) then whole site: sha256
   of every file matches the manifest, `curl` serves the old content, WAF and include are back.
5. Make `nginx -t` fail via a crafted archive: restore rolls back, the site still serves.
6. Corrupt one byte in a MinIO object: verify fails, `backup.verify_failed` recorded; a fresh run
   passes verify.
7. Fill the disk to below the floor: run refused by the preflight with a clear message.
8. Retention: run 10 times with a fake clock hook (`--now` on the built-in, test-only) and check the kept set.
9. **Fresh host**: a second, newly provisioned Ubuntu VM (or the same VM reinstalled). `pbp install`
   → `pbp restore panel s3://...` with the passphrase → start → log in with the **old** password →
   ACME account and DNS provider open (no `ErrUnknownKey`) → install nginx + PHP from the Store →
   Restore all sites → the site serves over TLS with the old certificate.
10. Reboot the restored host: everything comes back.

Manual, once: one run against a real R2 or B2 bucket the operator provides. EL: M9.

## 7. Risks

- **R1 Size.** Daily full archives of a multi-GB root cost storage and egress. Mitigated by
  excludes, GFS, zstd; measured in step 0. Dedup (D1) is the escape hatch, not v1.
- **R2 Lost passphrase = lost backups.** By design. Mitigated by the one-time warning, a
  "verify passphrase" action, and a printable recovery sheet (key ID + date, never the key).
- **R3 Sealed key in panel.db and secret.key inside the same archive.** Both are inside the
  passphrase encryption; anyone with the passphrase gets everything. Accepted: the passphrase is
  the root of trust for backups, as `secret.key` is for the db.
- **R4 Restore writes as root into operator paths.** Only through fm's descriptor-based extractor,
  into a fresh sibling folder, swapped by rename. Security review of step 5 before merge.
- **R5 Schema drift.** Old site archives rely on migrations staying replayable on a trimmed db; the
  table-classification test and a golden archive from each release in `testdata/` keep it honest.
- **R6 Vendored SigV4.** Provider quirks surface late; MinIO in CI plus one real provider by hand.
- **R7 Inconsistent live copies** (D13): an app writing during the walk. Warned, not prevented.
- **R8 Host-specific state in panel.db** (bind address, panel domain, firewall rules naming the old
  SSH port, panel cert for the old IP): the CLI resets the network-binding settings by default; the
  firewall's 60 s confirm timer protects the rest **(assumed: the firewall reapplies from db at
  boot under that timer; checked in step 0)**.
- **R9 Memory.** Streaming throughout; no member is buffered whole. Asserted by the 1 GiB test.

## 8. Out of scope

- Database dumps (MySQL/Postgres/SQLite apps): no DB feature exists; §4.6 is the hook.
- Dedup/incremental repositories, snapshots (LVM/btrfs/ZFS), block-level backups.
- Backing up the nginx/PHP runtimes, Docker images/volumes, `/www/panel/history`, logs, trash.
- Restore into another domain; partial row restores (e.g. WAF only); cross-panel migration of one
  site between two live panels (works by download + restore, but not designed for).
- Other remote kinds (SFTP, WebDAV, Google Drive); server-side encryption settings of providers.
- Multi-server, multi-user retention policies, object lock/immutability (may come later as an S3
  header on the target).
- EL (Rocky/RHEL) host gate: M9.
