# Server Panel — Development Plan

Date: 2026-11-16. Source: [plan.md](plan.md) (decisions, layout, architecture, stages).
Status: not started. This document turns the ten stages of `plan.md` §4 into milestones with
ordered steps, deliverables, tests, and exit gates. Where this file and `plan.md` disagree,
`plan.md` wins and this file is corrected.

References like **D7** point to decision 7 in `plan.md` §1; **§3.5** points to a section there.

---

## 1. How the work is organised

### 1.1 Milestones

| Milestone | Stage in plan.md | Theme | Size | Depends on |
|-----------|------------------|-------|------|------------|
| M0 | 0 Foundation | Daemon, auth, settings, jobs, install, UI shell | XL | — |
| M1 | 1 Nginx and sites | Docker client, nginx container, `confedit`, static + proxy sites | L | M0 |
| M2 | 2 PHP config | PHP containers, php.ini, pools, PHP sites | M | M1 |
| M3 | 3 File manager | Browse/edit/upload/archive/trash with guardrails | L | M0 |
| M4 | 4 Firewall | iptables chains, rules, confirm-or-rollback | M | M0 |
| M5 | 5 Apps | supervisord, nvm, app CRUD, link to proxy site | M | M1 |
| M6 | 6 TLS | Upload, ACME HTTP-01 / DNS-01, renewal, panel cert | M | M1 |
| M7 | 7 Dashboard, logs, cron | Metrics, service control, log tail/search, cron | M | M0 (M1–M5 for content) |
| M8 | 8 Docker management | PHP image builds, version install/remove, nginx update | L | M2 |
| M9 | 9 EL9 pass | Rocky 9 in `osdep`, SELinux, firewalld | M | M0–M8 |

Sizes are relative (S < M < L < XL), not calendar estimates. M0 is the largest because every later
milestone sits on its pipelines.

### 1.2 Order and parallelism

```
M0 ──┬── M1 ──┬── M2 ── M8 ──┐
     │        ├── M5 ────────┤
     │        └── M6 ────────┤
     ├── M3 ─────────────────┼── M7 ── M9
     └── M4 ─────────────────┘
```

The default order is the stage order (M0 → M9). After M1 lands, M3 and M4 have no dependency on
M1/M2 and can run in parallel with them if more than one person works on the repo. M7 goes after
M5 so the dashboard shows real services, apps, and cron instead of placeholders. M9 is last by
decision (D29).

### 1.3 Definition of done (every milestone)

A milestone is done when all of these hold, and not before:

1. `go test ./...` and `go vet ./...` green; frontend `vitest`, `tsc --noEmit`, `eslint` green.
2. `scripts/smoke/<stage>.sh` passes on a **fresh** Ubuntu VM (snapshot reverted before the run),
   installed from the release binary built by CI, not from a dev tree.
3. Every earlier smoke script still passes on the same VM (regression).
4. New settings appear in the settings registry with default, validation and restart flag.
5. Every state-changing route writes an audit entry.
6. The milestone section in this document is updated: steps ticked, deviations recorded.

Two milestones add a **security review** to the gate: M0 (auth and sessions) and M3 (file manager).
Findings rated High or above block the milestone (risk 3 in `plan.md` §5).

### 1.4 Conventions that apply from the first commit

- **No shell strings.** `exec.Command` with argument vectors only. A lint check (`forbidigo` or a
  grep in CI) rejects `sh -c` and `bash -c` outside an explicit allowlist.
- **Database first, then render.** Every feature that writes to disk has a `Render(ctx)` that is
  idempotent and is called at boot.
- **All config writes go through `confedit`** once it exists (M1). Before M1, nothing writes
  managed config.
- **Distro logic only in `osdep`.** Other packages call interfaces; CI greps for `apt`, `dnf`,
  `ufw`, `firewalld`, `semanage` outside `internal/osdep`.
- **Unix-only behaviour is tested in `*_unix_test.go`.** File modes and ownership are not
  meaningful on Windows, and a test that "passes" there proves nothing. CI runs the suite on Linux.
- **Errors reach the UI.** Validator output (`nginx -t`, `php-fpm -t`, `iptables-restore`) is
  returned verbatim in the API error body and shown in the UI. No silent fallbacks.
- **Commits** are small, one concern each, with a conventional prefix (`feat(sites): …`).

### 1.5 Test environments

| Environment | Used for | Notes |
|-------------|----------|-------|
| Developer machine | Unit tests, frontend dev server | Windows/macOS fine for most packages; Linux-only packages behind build tags. |
| CI (Linux runner) | Full Go + frontend suite, release build | Produces the static binary used by smoke runs. |
| Ubuntu VM (24.04 LTS) | Smoke scripts M0–M8 | Snapshot "clean install" taken once; reverted before every gate run. |
| Rocky 9 VM | M9 only | SELinux enforcing, firewalld active at start. |

Smoke scripts run from the operator's machine against the VM over SSH plus the panel's HTTPS API.
They use their own HTTP client and an independent oracle (e.g. `curl` against the site, `iptables
-S` on the host, `docker inspect`) instead of trusting the panel's own status endpoints.

---

## 2. M0 — Foundation

**Goal.** A static binary that installs itself on a fresh Ubuntu VM, serves the SPA over its own
HTTPS, lets the single admin log in, edit every setting from the UI, see job output live, and
recover from the CLI.

**Covers.** D1–D7, D26, D27, §3.1, §3.2, filesystem layout §2 (directories only).

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 0.1 | Repo skeleton | Go module, `cmd/panel`, `internal/` tree from §3.1 as empty packages, `web/` Vite + React + TS + Tailwind + shadcn/ui, Makefile, CI workflow (Go + web + static build with `CGO_ENABLED=0`), lint config incl. the no-shell-string and osdep greps. | CI green on an empty build. |
| 0.2 | CLI frame | Subcommands `serve`, `install`, `upgrade`, `info`, `reset-password`, `set`, `firewall restore\|disable` (last two stubbed until M4). Root check for everything except `--version`. | Table test of argument parsing; non-root refusal. |
| 0.3 | `store` | SQLite via `modernc.org/sqlite`, WAL mode, busy timeout, forward-only numbered migrations embedded with `go:embed`, `schema_migrations` table, one connection pool, transaction helper. | Migrate up on empty DB; re-run is a no-op; failing migration leaves version unchanged. |
| 0.4 | `settings` registry | Typed keys (`int`, `string`, `bool`, `duration`, `port`, `path`, `enum`), default, validator, `RestartRequired` flag, description for the UI. Read path: DB value or default. Write path: validate → store → notify subscribers. First keys: `panel.port`, `panel.bind`, `session.idle_timeout`, `login.max_attempts`, `login.lockout`. | Every key has a valid default; invalid values rejected; subscribers notified once. |
| 0.5 | `audit` | `audit_log` table (time, actor, action, target, result, detail JSON), writer used by middleware, file mirror to `/www/panel/logs/audit.log`. | Entry written for success and failure; detail never contains secrets (redaction test). |
| 0.6 | `auth` | argon2id hash/verify with tuned parameters stored with the hash; single `admin` row; session table (random 256-bit ID, hashed at rest, created, last seen, IP, UA); idle timeout; revoke-all on password change; CSRF token bound to session (double-submit header `X-CSRF-Token`); failed-login throttle per IP and global, temporary lockout. | Hash round-trip; idle expiry; revoke-all; lockout after N; CSRF missing/wrong → 403; timing of unknown user ≈ known user. |
| 0.7 | `httpapi` core | Router (`chi` or stdlib mux), middleware chain: request ID, recover, security headers (CSP, HSTS, frame-deny, no-sniff), session, CSRF on unsafe methods, audit, JSON error envelope `{error, detail}`. Self-signed TLS cert generated at install into `/www/panel/ssl/panel.{crt,key}`; listener on `panel.bind:panel.port`. Routes: `POST /api/login`, `POST /api/logout`, `GET /api/me`, `POST /api/password`, `GET/PUT /api/settings`, `GET /api/audit`. | `httptest` suite for every route incl. unauthenticated access → 401 and CSRF → 403. |
| 0.8 | Port change without lock-out | Changing `panel.port` starts the new listener first, returns the new URL, then closes the old one after a grace period. If the new bind fails the setting is reverted and the error returned. | Integration test on two free ports; bind failure keeps the old port. |
| 0.9 | `jobs` + SSE | `jobs` table (id, kind, state, started, finished, exit, error), output lines persisted (capped per job), in-memory fan-out, `GET /api/jobs/{id}/events` as SSE with `Last-Event-ID` resume, job cancellation via context. Jobs survive a page reload; on daemon restart running jobs are marked `interrupted`. | Resume from a mid-stream ID; cancel; restart marks interrupted. |
| 0.10 | `osdep` Ubuntu | Interface: `DetectDistro`, `InstallPackages`, `EnsureUser(www, uid, gid)`, `EnsureDocker`, `MaskFirewallServices` (unused until M4), `SELinux` (no-op). Ubuntu implementation using `apt-get` with `DEBIAN_FRONTEND=noninteractive`. | Fake command runner records argv; Ubuntu impl emits expected argv. |
| 0.11 | `panel install` / `upgrade` | Install: root check → distro detect → create `/www` tree with modes from §2 → `www` user with fixed UID/GID → install Docker Engine from the official repo → generate `secret.key` (0600) → init DB → prompt or generate admin password and print it once → self-signed cert → write and enable `panel.service` (§3.2 hardening) → start → print URL. Idempotent: re-running repairs, never overwrites the DB or password. Upgrade: copy new binary next to the old, run migrations dry check, atomic rename, restart, roll back binary if health check fails within 30 s. | Install steps unit-tested via fake runner; idempotency test on a temp root. Real run is the smoke. |
| 0.12 | Recovery CLI | `info` (port, bind, URL, version, DB path), `reset-password` (prints a new one, revokes sessions), `set <key> <value>` through the same registry validators. Works while the daemon runs (SQLite WAL) and when it is stopped. | CLI tests against a temp DB. |
| 0.13 | UI shell | Login page, app layout with sidebar sections for every future milestone (disabled until built), TanStack Router + Query, API client with CSRF header and 401 → login redirect, Settings page generated from the registry (grouped, restart-required badge), Audit page, Jobs drawer that streams SSE, password change. SPA embedded with `go:embed`, served with correct cache headers. | Vitest for API client and settings form rendering; one Playwright journey (login → change setting → logout). |
| 0.14 | Smoke script | `scripts/smoke/0-foundation.sh` and a small shared library (`scripts/smoke/lib.sh`: SSH runner, API login with cookie jar + CSRF, assertions, cleanup). | — |
| 0.15 | Security review | Review of 0.6–0.8 and headers: session fixation, CSRF coverage of every unsafe route, throttle bypass via `X-Forwarded-For` (must not be trusted), cookie flags, timing, error message enumeration, cert/key file modes. Findings fixed or recorded. | Regression test for each fixed finding. |

### Exit gate (smoke 0)

Fresh VM → copy binary → `panel install` → log in over HTTPS → change `panel.port` in the UI →
panel answers on the new port and not the old one → `panel reset-password` → old session is
rejected, new password logs in → `panel info` shows the new port → reboot → panel is back.

### Risks specific to M0

- Docker install on Ubuntu pulls from Docker's repository; pin the repo setup steps and fail
  loudly if the GPG key or repo is unreachable instead of falling back to the distro `docker.io`.
- `MemoryDenyWriteExecute` in the unit can break pure-Go code only in unusual cases; verify the
  daemon starts with the full hardening list on the first VM run, not later.

---

## 3. M1 — Nginx and sites

**Goal.** nginx runs in Docker under panel control. Static and reverse-proxy sites are created
from a form, then owned by the operator as raw text, with every write validated and rolled back on
failure.

**Covers.** D8, D9, D12, D13, D22, D23, D28 (nginx part), §3.3, risk 1, risk 4.

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 1.1 | Spike: `www` in the nginx image | Decide between a two-line derived image (`FROM nginx:<ver>` + `useradd -u <uid> www`) and a mounted passwd/group entry. Record the decision and why in this document. Also confirm: host networking works, `nginx -t` runs via `docker exec` (or a one-shot container) against the mounted config, `nginx -s reload` via exec. | Spike notes; throwaway script kept in `scripts/spikes/`. |
| 1.2 | `docker` client | Thin client over `/var/run/docker.sock`: ping, image inspect/pull (streaming progress into a job), container create/start/stop/remove/inspect, exec with captured output and exit code, logs. No Docker SDK dependency. Explicit `unix` dial (no TCP fallback). | Tests against a fake socket server with recorded responses; one Linux-only integration test behind a build tag. |
| 1.3 | Container unit model | `ContainerSpec` (name, image, role, mounts with identical host/container paths, host network, user, entrypoint, args, labels `panel.role=...`, restart policy `unless-stopped`). `Ensure(spec)` is idempotent: create if missing, recreate if the spec hash label differs, start if stopped. Mount rule: never mount over a directory the image needs (mount individual files or panel-only subdirectories). | Spec hash stability; Ensure decisions table-tested with the fake client. |
| 1.4 | `confedit` pipeline | `Apply(ctx, change)`: for each file snapshot → atomic write (temp + fsync + rename, same mode/owner) → validator → reload → health check (container running, process alive) → on any failure restore all snapshots, reload again, return validator output. History: last 10 versions per file under `/www/panel/history/` with metadata in DB; restore is just another `Apply`. A per-service mutex serialises applies. | Failure injection at each stage restores every file byte-for-byte; history trimmed to 10; concurrent applies serialised. |
| 1.5 | nginx renderer | Templates: `nginx.conf` (generated once; includes `panel-http.conf` and `sites/*.conf`; does not set any directive the form owns), `panel-http.conf` (worker processes, client_max_body_size, gzip, timeouts), site file, include files `listen.conf`, `acme.conf` (placeholder until M6), `proxy.conf`. Logs to `/www/panel/logs/sites/<domain>.{access,error}.log`. | Golden-file tests per template; rendered config passes `nginx -t` in the integration test. |
| 1.6 | `sites` model | `sites` table (domain primary, aliases, kind, root, proxy target, php version null until M2, created). Domain validation (IDN to punycode, no wildcards until DNS-01 in M6), uniqueness across primary and aliases. Create: DB row → web root `/www/services/sites/<domain>` owned by `www` with a placeholder `index.html` → render site file + includes → `confedit.Apply`. Delete: confirm → remove config through `confedit` → optionally move web root to trash (M3) — until M3, keep the web root and say so. | Create/delete with fake confedit; duplicate domain rejected. |
| 1.7 | Include ownership and detached detection | Panel actions (edit domains, proxy target) rewrite only their include file. A scanner checks the raw site file for each expected `include` line (literal match on the line, no parsing per risk 4); a missing one marks the feature `detached` and the UI disables that control with an explanation. "Regenerate site file" is a confirmed action that rewrites the site file from the template through `confedit`. | Remove include → detached; regenerate → attached; raw edits elsewhere untouched. |
| 1.8 | Global nginx config | Common-settings form → `panel-http.conf`. Raw editor for `nginx.conf`. Duplicate-directive errors from `nginx -t` shown as-is. | Bad raw edit rolled back with nginx still serving. |
| 1.9 | Boot reconciliation | On daemon start: ensure nginx container (`docker.Ensure`), re-render panel-owned include files from DB (never the raw site files), reload once. | Delete the container by hand → daemon restart restores it. |
| 1.10 | API | `GET/POST /api/sites`, `GET/PUT/DELETE /api/sites/{domain}`, `GET/PUT /api/sites/{domain}/file`, `POST /api/sites/{domain}/regenerate`, `GET/PUT /api/nginx/settings`, `GET/PUT /api/nginx/conf`, `GET /api/config-history/{file}`, `POST /api/config-history/{file}/{version}/restore`, `GET /api/services/nginx`. | `httptest` coverage; validator output present in 422 responses. |
| 1.11 | UI | Sites list, create dialog (kind, domains, root or proxy target), site detail with tabs: Overview (feature controls with detached state), Config (CodeMirror 6 with nginx highlighting, validate-on-save, diff against current, history with restore), Logs placeholder. Nginx page: settings form + raw editor + history. | Vitest for forms and detached rendering; Playwright: create static site, break config, see rollback error. |
| 1.12 | Smoke | `scripts/smoke/1-nginx-sites.sh`. | — |

### Exit gate (smoke 1)

Create a static site and a reverse-proxy site (proxy to a throwaway `python3 -m http.server`);
curl both with `Host:` headers and match content. Save a broken site file → API returns the
`nginx -t` error, file on disk equals the previous version, both sites still serve. Remove an
include line → feature shows detached. Restart daemon and reboot VM → both sites serve.

---

## 4. M2 — PHP config

**Goal.** PHP sites work on the official `php:<ver>-fpm` images, one container per version and
one pool per site, with php.ini and pool settings editable through form and raw editor.

**Covers.** D15, D16 (official images only), D28 (PHP part), §3.4, risk 5, risk 6.

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 2.1 | Version catalogue | Supported versions as a setting (initially the versions with official `-fpm` images still in security support). `php_versions` table: version, image ref + digest, state (`absent`, `pulling`, `ready`, `failed`). Pull as a job. | State transitions; failed pull leaves state `failed` with output. |
| 2.2 | PHP container | One container per version: host network, user `www` (same approach as 1.1), mounts `/www/panel/php/<ver>/` (php.ini, conf.d/zz-panel.ini, pool.d/), `/www/panel/run`, `/www/services/sites`, logs. The image's own `php-fpm.d` is not hidden; the panel adds its pool directory with an `include`. Default `www` pool from the image disabled so no TCP listener exists. Reload via `USR2`. Validator: `php-fpm -t` in the container. | Container spec golden test; integration: no TCP 9000 listener after start. |
| 2.3 | php.ini | Copy the image's `php.ini-production` once to `/www/panel/php/<ver>/php.ini` (then raw). Form (memory_limit, upload_max_filesize, post_max_size, max_execution_time, date.timezone) writes `conf.d/zz-panel.ini`. Both through `confedit` with `php-fpm -t`. | Bad ini directive rolled back. |
| 2.4 | Pools | One pool file per PHP site: socket `/www/panel/run/php-<ver>-<site>.sock` owned `www:www` 0660, `pm` mode and child limits from the pool form, `chdir` to web root, per-site error log. Pool name derived from the domain with a safe mapping (dots/dashes → underscores, length capped, collision check). | Name mapping table test; collision rejected. |
| 2.5 | PHP site kind | Create-site form gains `php` with version picker. Renders `php.conf` include (`fastcgi_pass unix:<socket>`; `try_files`; index `index.php`). Placeholder `index.php` echoing `phpinfo()` only when the operator ticks a box (off by default). | Golden tests. |
| 2.6 | Version switch | One `confedit.Apply` with three files: remove old pool, add new pool, rewrite `php.conf`. Order of reloads: new FPM first, then nginx, then old FPM. Any failure restores all three. | Failure in the new FPM leaves the site on the old version and serving. |
| 2.7 | Container lifecycle | Container for a version starts when the first site uses it and stays up; removing the last site leaves it running (stopping is explicit in M8). Boot reconciliation extends 1.9. | — |
| 2.8 | API + UI | `GET /api/php`, `POST /api/php/{ver}/pull`, `GET/PUT /api/php/{ver}/settings`, `GET/PUT /api/php/{ver}/ini`, `GET/PUT /api/sites/{domain}/pool`, `PUT /api/sites/{domain}/php-version`. PHP page per version (form, raw ini, history); site detail gains a PHP tab. | Route tests; vitest for forms. |
| 2.9 | Smoke | `scripts/smoke/2-php.sh`. | — |

### Exit gate (smoke 2)

Create a PHP site on version A → `phpinfo()` served through nginx → change `memory_limit` in the
form → new value visible in `phpinfo()` → switch to version B → `PHP_VERSION` changes → write an
invalid directive in the raw ini → rejected, previous file restored, site still serves.

---

## 5. M3 — File manager

**Goal.** A complete file manager that can reach the whole filesystem as root, with guardrails
that stop the obvious disasters and a recycle bin.

**Covers.** D20, D21, §3.7, risk 3 (second security review).

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 3.1 | Path policy | Single `fm.Policy` used by every operation: cleans and absolutises paths, rejects NUL and non-UTF-8, applies the hard-deny list (§3.7) to the target itself for destructive and recursive operations, refuses writes under `/proc`, `/sys`, `/dev`. Recursive destructive operations require a `confirm` field equal to the base name. | Exhaustive table test of allowed/denied cases incl. `/etc/`, `/etc/..`, `//etc`, symlink to `/etc`. |
| 3.2 | Descriptor-based operations | Operations open the parent directory and act relative to its descriptor (`openat`/`unlinkat`/`renameat`/`fchmodat` with `O_NOFOLLOW` where applicable), so a symlink swapped in between check and use cannot redirect a write. Recursive walks never follow symlinks. | Unix tests that race a symlink swap against delete/chmod. |
| 3.3 | Listing and metadata | List with sort, size, mode, owner, mtime, symlink target, hidden toggle; pagination for large directories; stat; disk usage per mount. | — |
| 3.4 | Basic operations | Create file/folder, rename, move, copy (recursive, progress as a job), chmod/chown (single and recursive), delete → trash. | Per-op tests incl. cross-device move. |
| 3.5 | Trash | Move to `/www/panel/trash/<id>/` with original path, owner, mode in DB. Cross-filesystem: copy then remove, as a job. Restore (refuses if the original path now exists, offers rename). Size limit and retention days are settings; a daily job purges. "Delete permanently" is a separate confirmed action. | Restore round-trip preserves mode/owner; purge honours retention. |
| 3.6 | Text editor | Open refuses binary (NUL sniff) and files above `fm.editor.max_bytes`. Save is atomic with an optimistic-concurrency check (mtime + size + hash from open); conflict → 409 with both versions. CodeMirror language from extension. Files under `/www/panel/nginx` and `/www/panel/php` are redirected to the config editor so they go through `confedit`. | Conflict detection; binary refusal. |
| 3.7 | Chunked upload | Init (target, size, name) → chunks with offset + checksum → complete (assemble, verify, atomic move into target). Resume reads the server's received offset. In-progress data in `/www/panel/uploads`, swept after a settable age. Per-chunk size cap; overall size limited only by free disk. | Interrupted upload resumes; corrupted chunk rejected; sweep vs in-flight complete race test. |
| 3.8 | Download | Single file with `Range` support; folder as streamed `tar.gz` or `zip` (choice), no temp file. | Range correctness; archive contents match. |
| 3.9 | Archives | Create zip/tar.gz from a selection (job). Extract (job): every entry resolved against the target, rejecting absolute paths, `..`, symlinks or hardlinks pointing outside, device files; size and entry-count limits against zip bombs. | Hostile archive corpus (zip-slip, symlink escape, bomb) all rejected. |
| 3.10 | API + UI | `/api/fm/*` routes. UI: list view with breadcrumbs, toolbar, context menu, multi-select, an upload queue held in app-level state so it survives navigation, typed-confirmation dialog, trash view. | Playwright: upload, edit, delete, restore. |
| 3.11 | Security review | Path policy, descriptor operations, archive extraction, upload assembly, download of arbitrary files (by design root can read all — confirm session/CSRF coverage only). Findings fixed or recorded. | Regression test per finding. |
| 3.12 | Smoke | `scripts/smoke/3-file-manager.sh`. | — |

### Exit gate (smoke 3)

2 GB upload with the connection killed mid-way and resumed → `sha256sum` matches. Delete of
`/etc` refused; recursive delete without the typed name refused. Delete a folder → it is in trash
→ restore → contents, modes and owners identical. A hostile archive is rejected and writes nothing
outside the target.

---

## 6. M4 — Firewall

**Goal.** iptables-based default-deny inbound firewall with panel-owned chains, IPv4 and IPv6,
that cannot lock the operator out.

**Covers.** D10, D11, §3.5.

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 4.1 | Rule model | `fw_rules` (port or range, protocol tcp/udp/both, optional source CIDR v4/v6, comment, enabled), `fw_blocklist` (IP or CIDR). Built-in rules generated, not stored: established/related, loopback, ICMP/ICMPv6 essentials (incl. neighbour discovery), SSH port, panel port. | Validation table: ranges, CIDR families, overlaps allowed. |
| 4.2 | SSH port detection | Read `sshd -T` output (argv, no shell) for every `port` line; fall back to parsing `sshd_config` and `sshd_config.d/*`; fall back to 22. Recorded on each apply. | Fixtures for multi-port and include files. |
| 4.3 | Renderer | Produces two `iptables-restore` inputs (v4, v6) touching only `PANEL-INPUT` and `PANEL-BLOCK` in the `filter` table: flush own chains, rules, final `DROP`, and the single jump from `INPUT` if not present. Applied with `--noflush`. Never touches `DOCKER*` or other chains, never changes the `INPUT` policy. | Golden files; a test asserts no line mentions a chain outside the owned set. |
| 4.4 | Apply with confirm timer | Apply: save current owned-chain state (`iptables-save` filtered) → restore new → start 60 s timer → UI shows countdown and "Keep". Keep → DB marks the ruleset confirmed. Timeout or daemon restart before confirm → restore previous state. Only one pending apply at a time. | Timer fires; confirm cancels; restart with pending apply rolls back. |
| 4.5 | First enable | Firewall is off after install (stages 0–3 rule). Enabling: show what will happen, then `osdep.MaskFirewallServices` (stop + mask ufw/firewalld), then first apply with the timer. Disable: unhook and flush own chains, leave ufw/firewalld masked with a note. | — |
| 4.6 | `panel-firewall.service` | Oneshot unit `Before=network-pre.target` running `panel firewall restore` from the DB; does not need the daemon. `panel firewall disable` is the recovery path from a console. | CLI tests with a fake runner. |
| 4.7 | Docker coexistence | Verify Docker's chains survive applies and restore; document the published-port bypass in the UI (warning banner if any non-panel container publishes ports). | Smoke covers it. |
| 4.8 | API + UI | `/api/firewall` (state, rules, blocklist, pending apply), apply, confirm. UI: rules table, built-ins shown read-only, blocklist, apply bar with countdown. | Vitest for countdown and pending state. |
| 4.9 | Smoke | `scripts/smoke/4-firewall.sh`. | — |

### Exit gate (smoke 4)

Enable firewall → a test port with a listener is closed from outside → add rule → open → apply a
rule set and do not confirm → after 60 s the previous state returns → confirm a set → reboot →
rules present before the daemon starts → `iptables -S` still shows Docker's chains → SSH and panel
reachable throughout. Repeat port checks over IPv6 if the VM has it.

---

## 7. M5 — Apps

**Goal.** Run Go binaries and Node.js apps as supervised processes that survive panel restarts,
with Node versions installed from the panel.

**Covers.** D17, D18, D19, §3.6, risk 2.

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 5.1 | Spike: supervisord RPC | Confirm the Go supervisord's XML-RPC on a Unix socket supports start/stop/restart, process info, reloadConfig/update, and log tail. Record the subset in this document. Fallback for any missing call: its `ctl` subcommand via argv. | Spike notes. |
| 5.2 | supervisord install | Pinned release downloaded with checksum verification into `/www/panel/bin`, `supervisord.conf` (unix socket 0700 root, include `conf.d/*.conf`), `panel-supervisord.service` independent of `panel.service`. Part of `panel install` from M5 on; `install` re-run adds it to older installs. | Checksum mismatch aborts. |
| 5.3 | `runtimes` (nvm) | nvm checked out to `/www/panel/nvm` at a pinned tag; install/uninstall Node versions as jobs (nvm runs through `bash` with argv, the one allowlisted shell use, version string validated against a strict pattern). List installed versions with resolved absolute `node` path. | Version string validation; job output captured. |
| 5.4 | App model | `apps` (name, type go/node/command, command, args, working dir default `/www/services/apps/<name>`, env, user default `www`, node version, autostart, restart policy, port optional). Program file rendered to `conf.d/<app>.conf` with logs under `/www/panel/logs/apps/<name>/`. Changes go through `confedit` with a validator that parses the generated file and calls `reloadConfig`. | Golden program files; env escaping. |
| 5.5 | Control | Start/stop/restart, status with uptime and PID, log tail via SSE. | Fake RPC server tests. |
| 5.6 | Link to proxy site | "Create reverse-proxy site" on an app with a port pre-fills the M1 create form with `http://127.0.0.1:<port>`. | — |
| 5.7 | API + UI | `/api/apps`, `/api/runtimes`. Runtimes page; Apps list and detail (config, logs, controls). | Vitest; Playwright create app. |
| 5.8 | Smoke | `scripts/smoke/5-apps.sh`. | — |

### Exit gate (smoke 5)

Install a Node LTS version → create a Node app and a Go binary app → both respond → `kill -9` each
PID → both restart → `systemctl restart panel` → apps keep their PIDs → create a proxy site for one
→ reachable by domain.

---

## 8. M6 — TLS

**Goal.** Certificates for sites by upload, HTTP-01 and DNS-01 (Cloudflare), with automatic
renewal; replaceable panel certificate.

**Covers.** D4 (panel cert replacement), D14, §3.8.

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 6.1 | Secret box | AES-GCM with `secret.key` for DNS provider credentials and the ACME account key; key rotation not in scope but the ciphertext carries a key ID. | Round-trip; tamper detection. |
| 6.2 | Certificate store | `certs` (site, source upload/http01/dns01, domains, not-before/after, issuer, last renewal result). Files in `/www/panel/ssl/sites/<domain>/` with key 0600. Upload validates chain, key match, and domain coverage. | Mismatched key rejected; expired cert warns. |
| 6.3 | `tls.conf` + `acme.conf` | `tls.conf`: listen 443 ssl (+ http2), cert paths, optional HTTP → HTTPS redirect that keeps `/.well-known/acme-challenge/` on HTTP. `acme.conf` serves the webroot under `/www/panel/ssl/acme-webroot`. Both via `confedit`, both detachable. | Golden tests; redirect excludes ACME path. |
| 6.4 | HTTP-01 | lego with a webroot provider. Pre-flight: for each domain, fetch a random token over HTTP from the server's public address before calling the CA, so DNS/port problems surface before rate limits. Account per CA directory; staging directory selectable as a setting. | Fake ACME server (pebble) in a Linux integration test. |
| 6.5 | DNS-01 | Provider interface; Cloudflare implementation with scoped API token. Wildcard domains allowed only for DNS-01 sites. | Pebble + a fake DNS provider. |
| 6.6 | Renewal | Daily job: renew certificates within 30 days of expiry, apply through `confedit`, record result; failures appear on the dashboard (M7) and in the audit log. Manual "renew now". | Clock-injected test of the 30-day window. |
| 6.7 | Panel certificate | Replace by upload or by issuing for a panel hostname (HTTP-01 needs port 80; when nginx owns 80, use the nginx webroot path). Hot-reload the listener cert without restart. | Listener picks up new cert. |
| 6.8 | API + UI | `/api/sites/{domain}/tls`, `/api/tls/providers`, `/api/panel/cert`. Site TLS tab; DNS provider settings; certificate list with expiry. | Vitest. |
| 6.9 | Smoke | `scripts/smoke/6-tls.sh` against the Let's Encrypt **staging** endpoint, needs a VM with a public IP and a test domain on Cloudflare. | — |

### Exit gate (smoke 6)

Issue a certificate by HTTP-01 and a wildcard by DNS-01 against LE staging → `openssl s_client`
shows the staging issuer for both → force renewal → new serial → upload a certificate → served →
replace the panel certificate → panel serves it without a restart.

---

## 9. M7 — Dashboard, logs, cron

**Goal.** One screen that answers "is the server healthy?", logs that can be tailed and searched,
and cron jobs whose output is recorded.

**Covers.** D24, §3.1 (`sysinfo`, `cron`).

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 7.1 | `sysinfo` | CPU, load, memory, swap, disk per mount, network rates, uptime from `/proc`; sampled on an interval into a ring buffer (no time-series DB); SSE stream. | Parsers against `/proc` fixtures. |
| 7.2 | Service status | Status of `panel`, `panel-supervisord`, `panel-firewall`, Docker, the nginx and PHP containers, apps; start/stop/restart controls for each (systemd via `systemctl` argv, containers via the Docker client). | Fake runners. |
| 7.3 | Dashboard | Metrics tiles and sparklines, service grid with controls, alerts: certificate renewal failures, detached features, firewall pending/disabled, failed jobs. | Vitest. |
| 7.4 | Log viewer | Registry of log sources (panel, audit, nginx global, each site, each app, each cron job, Docker container logs). Tail via SSE with follow; search with a literal or regex (RE2) over the last N MB, streamed; rotated `.gz` files readable. | Search over a gz fixture. |
| 7.5 | Log rotation | Panel-owned rotation for files under `/www/panel/logs` (size + count settings), with nginx/FPM reopen signals. | Rotation keeps N files. |
| 7.6 | `cron` | In-process scheduler (standard 5-field expressions + `@daily` etc.), jobs run as a chosen user with argv or a script file (no shell strings; a "shell" job writes a script file and runs it with `/bin/sh <file>`, explicitly allowlisted), timeout, overlap policy, output to `/www/panel/logs/cron/<job>/` and to the jobs table. Missed runs during downtime are not replayed. | Clock-injected scheduling tests; overlap policy. |
| 7.7 | API + UI | `/api/sysinfo`, `/api/services`, `/api/logs`, `/api/cron`. | Vitest; Playwright stop/start a service. |
| 7.8 | Smoke | `scripts/smoke/7-dashboard-logs-cron.sh`. | — |

### Exit gate (smoke 7)

Stop the nginx container from the host → dashboard shows it stopped → restart from the UI →
sites serve. Create a cron job `* * * * *` writing a timestamp → within two minutes its output is
in the run history and in its log file. Search a site access log for a request made by the script.

---

## 10. M8 — Docker management

**Goal.** PHP images built locally with chosen extensions, PHP versions installed and removed, and
nginx image updates with rollback — all without taking a live site down on failure.

**Covers.** D16 (full), D8 (version management).

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 8.1 | Tag contract | Images tagged `panel/php:<ver>-<build-id>` and `panel/nginx:<ver>-<build-id>`; the "current" pointer lives in the DB, not in a mutable tag. Last two good builds retained. | — |
| 8.2 | Build context | Dockerfile generated from the official base + `docker-php-ext-install`/`pecl` for selected extensions + `www` user; build context written to an absolute path; build via the Engine API with streamed output into a job; non-zero build result fails the job (do not trust the stream alone). | Generated Dockerfile golden tests; build-failure propagation test. |
| 8.3 | Extension catalogue | Curated list per PHP version with build dependencies (apt packages in the image); custom pecl names allowed with validation. | — |
| 8.4 | Candidate → promote | Start a candidate container from the new image on a temporary pool set, run `php-fpm -t` and a `php -m` check for every selected extension; if green, swap: start new container, reload nginx if sockets moved, stop old. Any failure leaves the old container serving and the candidate removed. | Failure at each step leaves the old container serving. |
| 8.5 | Version install/remove | Install = build or pull + candidate check. Remove refused while any site uses the version. | — |
| 8.6 | nginx update | Same candidate/promote flow for nginx with `nginx -t` against the live config; rollback to the previous image with one action. | — |
| 8.7 | API + UI | PHP versions page (installed, extensions, build history, rebuild, remove), nginx image page. | Vitest. |
| 8.8 | Smoke | `scripts/smoke/8-docker-mgmt.sh`. | — |

### Exit gate (smoke 8)

Build a PHP image with `intl` and `redis` → swap under a live site → `phpinfo()` lists both →
request a build with a bogus extension → build fails → site still served by the old container,
same container ID. Update nginx image → roll back → sites serve throughout (curl loop during the
swap shows no failed requests beyond a set tolerance).

---

## 11. M9 — EL9 pass

**Goal.** Every smoke script passes on Rocky 9 with SELinux enforcing. Changes are confined to
`osdep` and documented exceptions.

**Covers.** D29, risk 7.

### Steps

| # | Step | Deliverable | Tests |
|---|------|-------------|-------|
| 9.1 | Baseline run | Run smoke 0–8 on a fresh Rocky 9 VM unchanged; record every failure in this document before fixing anything. | — |
| 9.2 | `osdep` EL9 | dnf, Docker CE repo for EL, `useradd` differences, firewalld stop+mask, `iptables`/`ip6tables` nft backend availability, `sshd -T` path. | Fake-runner argv tests for EL9. |
| 9.3 | SELinux | Labels for bind mounts (`container_file_t` on panel-owned directories that containers read; use a relabel at install via `semanage fcontext` + `restorecon` rather than `:z` on shared paths like `/www/services/sites`, which would relabel the operator's tree), the socket directory, log directories written by containers; booleans if required. Read-only mounts keep their `ro` flag. | Smoke with `getenforce` = Enforcing and zero new AVC denials (`ausearch -m avc -ts recent`). |
| 9.4 | Crypto policy and TLS | Check panel TLS and ACME clients under the default EL9 crypto policy. | — |
| 9.5 | Fix and re-run | Fix each recorded failure; rerun all smoke scripts on both Ubuntu and Rocky 9. | — |
| 9.6 | Docs | Install guide for both distros; known limits (published-port bypass, shared `www`, host networking). | — |

### Exit gate (smoke 9)

`scripts/smoke/*.sh` all pass on Rocky 9 with SELinux enforcing and no unexplained AVC denials,
and still pass on Ubuntu.

---

## 12. Cross-cutting tracks

These run alongside the milestones rather than as stages.

| Track | When | Content |
|-------|------|---------|
| Settings registry growth | Every milestone | New keys added with default, validator, restart flag; Settings page picks them up automatically. |
| Audit coverage | Every milestone | A test enumerates all registered unsafe routes and asserts each writes an audit entry. |
| Docs | M0 start, updated each milestone | `docs/install.md`, `docs/recovery.md` (CLI, firewall disable from console), `docs/architecture.md`, one page per feature. |
| Release | From M0 | CI builds `panel-linux-amd64` (and arm64 if wanted) with version stamping; checksum file; `panel upgrade` consumes it. |
| Backups of panel state | M0 (manual), M7 (scheduled) | `panel.db` online backup via SQLite backup API to `/www/panel/data/backups/` with retention. |

---

## 13. Risk register (development view)

Builds on `plan.md` §5 with the milestone where each risk is retired.

| # | Risk | Retire in | How |
|---|------|-----------|-----|
| 1 | `www` user missing in official images | M1 step 1.1 | Spike decides derived image vs mounted passwd; reused by M2. |
| 2 | Go supervisord RPC subset | M5 step 5.1 | Spike lists supported calls; `ctl` fallback. |
| 3 | Root behind one password on a public port | M0 step 0.15, M3 step 3.11 | Two security reviews; throttle and session tests in CI. |
| 4 | Raw site files the panel cannot interpret | M1 step 1.7 | Include-line check only; detached state; explicit regenerate. |
| 5 | PHP on host network reaches loopback services | Accepted | Documented in UI and docs. |
| 6 | Shared `www` user across sites | Accepted | Documented. |
| 7 | EL9 issues surface late | M9 | `osdep` boundary enforced by CI grep from M0. |
| 8 | Bind-mounting over an image directory hides the image's own config | M1 step 1.3 | Mount rule in `ContainerSpec`; integration test checks image defaults still load. |
| 9 | Signals lost when running commands via `docker exec` | M1 step 1.2, M8 | Reloads use signals to the container's main process (`docker kill -s`), not exec'd shells. |
| 10 | Firewall lock-out | M4 | Built-in SSH/panel rules, 60 s rollback, oneshot restore, `panel firewall disable` from console. |
| 11 | Build success reported from the stream while the build failed | M8 step 8.2 | Check the final build result, not only the progress stream. |
| 12 | ACME rate limits during development | M6 | Staging endpoint by default; HTTP pre-flight before every order. |

---

## 14. First week, concretely

1. Step 0.1: repo skeleton, CI, lint rules — CI green on an empty build.
2. Steps 0.3 and 0.4: `store` and `settings` with tests.
3. Step 0.6: `auth` package with the full test list.
4. Step 0.7: `httpapi` core with login/me/logout and settings routes.
5. Prepare the Ubuntu VM and its clean snapshot; write `scripts/smoke/lib.sh`.

At the end of the week the panel should run locally with `panel serve --dev` (dev-only flag: temp
data dir, no install) and a browser should be able to log in and change a setting.
