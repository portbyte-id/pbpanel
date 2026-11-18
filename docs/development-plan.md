# Server Panel — Development Plan

Date: 2026-11-16. Source: [plan.md](plan.md) (decisions, layout, architecture, stages).
Status: M0 and M1 built and smoke-tested on Ubuntu; M6, M7 and M8 built, their smokes (6, 7, 8) written and not run (§8, §9, §10). This document turns the ten stages of `plan.md` §4 into milestones with
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

### Status (2026-11-17)

M0 is built and the exit gate passes on a real host: `scripts/smoke/0-foundation.sh` ran 28/28
on Ubuntu 26.04 with the full unit hardening list (including `MemoryDenyWriteExecute`), reboot
included. Not run: the web tests (vitest) and the Playwright journey after the last changes, and
the EL8–EL10 hosts (M0 is Ubuntu only by design).

| Step | State | Evidence |
|------|-------|----------|
| 0.1–0.7 | done | Package tests under `go test ./cmd/... ./internal/...`; `scripts/lint-conventions.sh`. |
| 0.8 | done | Not built until the first VM run showed `panel serve` was a stub. `httpapi.Listener` binds the new port before closing the old one after a grace period; a `panel.port` write whose bind fails is refused and stores nothing. Listener tests on two free ports, a busy port and concurrent changes; handler tests in `rebind_test.go`. |
| 0.9–0.10 | done | `internal/jobs`, `internal/osdep` tests. |
| 0.11 | done | Idempotency and step-order tests. The first VM run found that install never copied the binary to `/www/panel/bin/panel`; a `binary` step now does, with a regression test. |
| 0.12–0.13 | done | CLI tests; UI shell embedded and served by `panel serve`. |
| 0.14 | done | `scripts/smoke/lib.sh` and `0-foundation.sh`; `SMOKE_DRY_RUN=1` prints the plan. |
| 0.15 | done | `docs/security-review-m0.md`: no High or Critical findings; three Medium fixed with regression tests; six Low recorded as open. |

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

### Status (2026-11-17)

All twelve steps are built, and the exit gate passed: `scripts/smoke/1-nginx-sites.sh` ran 64
checks green against the Ubuntu 26.04 sandbox VM on 2026-11-17, including the daemon restart with
the container removed by hand and the reboot. Against the definition of done in §1.3 three items
are still open, listed under "Open after M1" below, so the milestone is built and gated but not
yet done by that definition.

| Step | State | Evidence |
|------|-------|----------|
| 1.1 | done | `scripts/spikes/1.1-nginx-www.sh`, all checks pass for both variants on Docker 29.9 (WSL Ubuntu 26.04, `nginx:1.29`). |
| 1.2 | done | `internal/docker`; fake-Engine tests on a Unix socket, plus `go test -tags integration ./internal/docker/` against a real Engine. |
| 1.3 | done | `internal/docker/unit.go`; `Decide` and `Ensure` table tests; the integration test covers create, no-op, start, recreate and re-create after a manual removal. |
| 1.4 | done | `internal/confedit`; failure injected at write, validate, reload and health; unix tests for mode, owner and symlinks (owner test needs root). |
| 1.5 | done | `internal/nginx` renderers; golden files in `internal/nginx/testdata` (`go test ./internal/nginx -update` rewrites them). Rendered config passes `nginx -t` in `TestRealNginxEndToEnd`. |
| 1.6 | done | `internal/sites` + migration `0007_sites.sql`; fake-confedit tests for create, delete, duplicates, and undo after a failed apply or a failed chown. |
| 1.7 | done | `sites.Detail` scans the site file for literal `include` lines; `Update` refuses a detached control with `DetachedError`; `Regenerate` is the way back. |
| 1.8 | done | `nginx.Global`: settings form (`panel-main.conf`, `panel-http.conf`) and raw `nginx.conf`. A bad raw edit is rolled back with nginx still serving, against a real Engine. |
| 1.9 | done | `sites.Reconciler`, wired in `cmd/panel/nginx.go`. Deleting the container by hand and re-running the reconcile restores it, against a real Engine. |

**Decision 1.1 — derived image.** The nginx container runs from a locally built
`FROM nginx:<ver>` + `groupadd -g 1500 www && useradd -u 1500 -g www …` image, not from the
official image with a mounted passwd entry. Both work. The derived image wins because:

- A mounted `/etc/passwd` is a copy of the image's file taken at one moment. An image update that
  adds or renumbers a system user is hidden behind the stale copy with no error.
- It would be the one bind mount whose path differs between host and container, and the one
  mounted over a file the image owns, which is exactly what the mount rule (risk 8) forbids.
  With the derived image `Spec.Validate` can require every mount to sit under `/www`.
- The build is a one-`RUN` Dockerfile posted to `/build` with the classic builder: no CLI, no
  BuildKit session, about a second. It is the same shape M8 needs, at none of M8's scope.

Also confirmed by the spike: host networking serves without a published port; `nginx -t` works
both by exec in the running container and by a one-shot container with the same mounts (for when
the service is down); `SIGHUP` to the container's PID 1 reloads; the image's `/etc/nginx` stays
intact when the panel config lives at `/www/panel/nginx` and nginx is started with
`-c /www/panel/nginx/nginx.conf`, so `include /etc/nginx/mime.types;` keeps working. The
entrypoint is `nginx` directly; the image's `/docker-entrypoint.sh` only edits files under
`/etc/nginx` that the panel does not use.

**Deviations and additions.**

- 1.2 gained `ImageBuild` (not in the step's list) because of decision 1.1, and `ContainerKill`,
  `ContainerWait` and `RunOnce` for signal reloads (risk 9) and one-shot validation.
- 1.2: the API version is pinned to 1.44 in every request path; `Ping` fails with an upgrade
  message on an older Engine. Pull and build results are confirmed by inspecting the image, not
  by the progress stream ending (risk 11 applied early).
- 1.3: `Ensure` refuses to replace a container that lacks the `panel.managed` label, so a name
  clash with a hand-started container is an error, not a removal. It also recreates when the
  image ID behind an unchanged tag has changed, which the spec hash alone cannot see.
- 1.4: an apply writes a journal of the previous file contents under
  `/www/panel/history/.pending/` before its first write and removes it when it ends.
  `Editor.Recover()` replays a leftover journal; step 1.9 must call it at boot before any reload.
  Without it a daemon killed between "write" and "validate" would leave an unvalidated config
  for the next reload to load.
- 1.4: `Change.Services` is a list (validate all, reload all, health-check all, in order), which
  is what step 2.6 needs. History is recorded only for applies that went live, and also captures
  content found on disk that the history did not end with (first touch, or an edit over SSH).
- Migration `0006_config_history.sql` adds the history index.

**Deviations and additions in 1.5–1.9.**

- 1.5: `worker_processes` is a main-context directive and `client_max_body_size` and `gzip` are
  http-context, so one file cannot carry both. The form owns two files: `panel-main.conf` (included
  at the top of `nginx.conf`) and `panel-http.conf` (inside `http {}`). `nginx.conf` also carries a
  `default_server` that answers `444`, so a Host no site claims does not fall through to the first
  site, and a `$connection_upgrade` map that proxy sites use for WebSockets.
- 1.5/1.7: a fourth include, `names.conf` (`server_name`), exists so that "edit domains" rewrites
  an include file and nothing else, as step 1.7 requires. The plan's list had no file for it.
- 1.5: site logs are `access_log`/`error_log` lines in the site file, under
  `/www/panel/logs/sites/`, the only logs directory mounted into the container. The master's own
  `error_log` goes to `/dev/stderr`, so it shows in `docker logs`.
- 1.6: a custom web root must lie below `/www/services/sites`, because that is the only web-root
  tree the container mounts. `golang.org/x/net/idna` was added for punycode; it brings
  `golang.org/x/text`.
- 1.6: the web root is created before the config is applied and removed again, without touching
  anything the operator put there, if the apply fails. An existing directory is never modified.
- 1.8: settings are stored in `nginx_settings` and written before the apply, and put back if the
  apply fails. A failed apply leaves neither the files nor the database changed.
- 1.9: the order differs from the plan's literal text so nginx never starts on files nobody
  validated. Boot runs `Recover`, builds the image if missing (a job), writes only the panel-owned
  files that are missing or differ, validated by `nginx -t` in a one-shot container, then ensures the
  container, then reloads once, unless the container was just created or started and so read the
  files itself. A config that fails `nginx -t` at boot stops the run before the container is touched
  and the validator's output is kept in the reconciler's status.
- 1.9: the reconcile runs in a goroutine and its outcome is `Reconciler.Status()`; Docker being
  absent or the image build failing never stops the panel from serving.
- A reload is a signal, so a configuration that passes `nginx -t` but fails at runtime (a bind error)
  is not seen by `Healthy`, which only catches a master that exited or restarted. Step 1.12's smoke
  should check a real request after a change.

| Step | State | Evidence |
|------|-------|----------|
| 1.10 | done | `internal/httpapi/handlers_nginx.go`; `httptest` coverage in `handlers_nginx_test.go`, and every route is in the 401/CSRF matrix of `routes_test.go`. |
| 1.11 | done | Sites list, create dialog, site detail (Overview, Config, Logs placeholder), Nginx page. Vitest for the forms and detached rendering; Playwright `web/e2e/sites.spec.ts`. |
| 1.12 | done | `scripts/smoke/1-nginx-sites.sh`, 64 checks passed on the Ubuntu sandbox. |

**Deviations and additions in 1.10–1.12.**

- 1.10: the error envelope gained two optional fields, `field` (the rejected input) and `output`
  (the validator's text, verbatim). A rejected config is `422 config_rejected`; a failure at
  reload or health is `502 apply_failed`; a failed rollback is `500` with the cause in the log only.
- 1.10: `DELETE /api/sites/{domain}` and `POST …/regenerate` need `?confirm=<domain>`.
- 1.10: one route beyond the plan's list, `GET /api/config-history/{file}/{version}`, returns a
  version's content so the editor can show a diff before a restore. `{file}` is `nginx.conf` or
  `sites/<domain>.conf`, URL-escaped. The panel-owned files have no history routes: they are
  rendered from the database, and restoring one would make the two disagree.
- 1.10: without a Docker client the routes answer `503 unavailable` instead of being absent.
- 1.10: `internal/install` imports `internal/httpapi`, so `nginx` and `sites` cannot import
  `install`. The `www` account constants are repeated in `nginx`, and a test in `cmd/panel` holds
  them equal.
- 1.11: the editor is CodeMirror 6 with the legacy nginx mode; "Show changes" is a unified diff
  against the saved file (`@codemirror/merge`). The bundle is about 800 kB (260 kB gzipped) and is
  not code-split.
- 1.11: the Playwright journey runs against the real HTTP layer and sites model with a stand-in for
  nginx (`scripts/e2e/server.go`), because the machine that runs it may have no Docker. The
  stand-in refuses any file containing `bogus_directive`. Real nginx is covered by
  `TestRealNginxEndToEnd` and by the smoke.
- 1.11 found a defect in M0's `internal/webui`: a deep link to `/sites/example.com` was answered
  `404`, because a path with a dot was taken for a missing asset. Only paths under `assets/` and
  top-level files are treated that way now.
- 1.12: the smoke takes an installed panel (run smoke 0 first), and resets the admin password
  unless `SMOKE_PASSWORD` is given.

**Open after M1.** Against §1.3:

- Item 2: the smoke ran on the sandbox VM as it was after smoke 0, not on a reverted snapshot, and
  with a binary cross-compiled from the working tree and installed with `panel upgrade`, not the
  CI release binary.
- Item 3: smoke 0 was not re-run after the M1 build.
- Item 4: the nginx common settings live in their own table (`nginx_settings`) and form, not in
  the settings registry, because a change to them has to go through confedit and be rolled back
  with it. They have defaults and validation; "restart required" does not apply.
- Item 1: there is no `eslint` script in `web/package.json`, so that check was not run.

Also: nothing in M1 has run on EL9 (the Rocky 9 host was unreachable; EL9 is M9's scope). Request bodies are capped at 1 MiB, below confedit's 2 MiB file limit, so a config file
between the two can be read but not saved through the API. `TestRepairFixesALoosenedMode` in `internal/install` fails in WSL at the
committed `HEAD` as well as in the working tree; it is not caused by 1.5–1.9. The Docker integration test and the unix-only tests do not run in CI as
configured (CI has no `-tags integration` job); they were run by hand in WSL.

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

### Status (2026-11-17)

All nine steps are built, and the exit gate passed twice: as a Go integration test against a real
Docker Engine (`TestRealPHPEndToEnd`, WSL Ubuntu 26.04, Docker 29.9, `php:8.3-fpm` and
`php:8.4-fpm`), and as `scripts/smoke/2-php.sh` against the Ubuntu 26.04 sandbox VM, 68 checks green on 2026-11-17, including the daemon restart with the PHP container removed by hand and the reboot. The first run of the smoke also covered the two pulls and passed 73 of 74 checks; the one failure was an assertion in the script itself (`docker top` without a PID column), fixed before the second run. Smoke 1 was re-run after the M2 build and passed its 64 checks.
As with M1, the definition of done in §1.3 has open items, listed under "Open after M2".

| Step | State | Evidence |
|------|-------|----------|
| 2.1 | done | `php.Manager` catalogue over `php_versions` (migration `0008_php.sql`); the supported list is the `php.versions` setting. Tests: state transitions, a failed pull or build leaves `failed` with the output, a second pull is refused while one runs, `Reconcile` fails an interrupted pull and marks a version whose image is gone `absent`. |
| 2.2 | done | `php.Spec`, `php.Service`. Spec asserted field by field in `TestSpec`; no rendered file has a TCP listener (`TestNoTCPListener`); on a real Engine nothing listens on 9000 and the pool socket is `www:www 0660`. |
| 2.3 | done | `php.Manager.SetSettings`/`ReadIni`/`WriteIni`. A php.ini with a syntax error and one loading a missing extension are both rejected and rolled back on a real Engine. |
| 2.4 | done | `php.PoolName`, `php.RenderPool`, `sites.Manager.SetPool`. Name mapping table test; `a-b.test` and `a.b.test` collide and the second is refused with `409`. |
| 2.5 | done | `nginx.KindPHP`, `php.conf` include, create form. Golden files `site-php.conf`, `php.conf`, `pool-*.conf`, `php-fpm.conf`, `zz-panel.ini`. |
| 2.6 | done | `sites.Manager.SwitchPHP`: one apply, services in the order new FPM, nginx, old FPM. A rejected apply leaves the database, the pool file and `php.conf` on the old version (`TestFailedSwitchKeepsTheOldVersion`). |
| 2.7 | done | A starting `php.Service` brings the container up with the version's first site; nothing stops one. `sites.Manager.ReconcilePHP` runs after the nginx reconcile at boot. A container removed by hand comes back on a daemon restart, on a real Engine and in the smoke. |
| 2.8 | done | `internal/httpapi/handlers_php.go`, every route in the 401/CSRF matrix; PHP page, site PHP tab, create dialog. Vitest `PHPForms.test.tsx`. |
| 2.9 | done | `scripts/smoke/2-php.sh`. |

**Decision 2.2 — the panel starts FPM on its own `php-fpm.conf`.** The step's text has the image's
`php-fpm.d` left in place and the panel's pool directory added with an `include`. The image's
`php-fpm.conf` cannot be given an extra `include` without mounting a file over it, which the mount
rule (risk 8) forbids, and its `php-fpm.d` holds `www.conf` and `zz-docker.conf`, which open TCP
9000. So FPM is started with `-y /www/panel/php/<ver>/php-fpm.conf`: the image's `php-fpm.d` is
not hidden and not read, no file is mounted over anything the image owns, and no TCP listener can
exist. php.ini is passed with `-c`, and `PHP_INI_SCAN_DIR=:/www/panel/php/<ver>/conf.d` appends the
panel's directory to the image's own, so the extensions the image enables still load and
`zz-panel.ini` is read last.

**Deviations and additions.**

- 2.1: the catalogue lists the versions of the setting plus any version that has a row, so a
  version removed from the setting keeps working for its sites and only loses its Pull button.
  The digest recorded is the official image's `RepoDigests` entry; what runs is a derived
  `panel/php:<ver>-fpm` (official image + `www` at uid 1500), the same approach as decision 1.1.
- 2.2: `php-fpm.conf` defines one pool itself, `panel_idle`, on a `0600` Unix socket with
  `pm = ondemand`. FPM refuses to start, and exits on reload, with no pool at all, so without it a
  version could not survive the removal of its last site, which step 2.7 requires.
- 2.2: the master runs as root, as the image intends, and every pool sets `user`/`group` to `www`.
  "User `www`" in the step is the pools' user.
- 2.2/2.3: the validator is two commands, `php-fpm -t` and a PHP start-up
  (`php -c … -r ''`). Both exit 0 on a broken ini: `php-fpm -t` prints `PHP:  syntax error` and
  only the start-up prints `Unable to load dynamic library`. Their output is matched for those
  messages. An unknown directive name is not an error to PHP and is not caught.
- 2.3: php.ini is copied from the image by reading `php.ini-production` in a one-shot container
  and writing it through confedit with the first change that touches the version, so the copy is
  validated and is version 1 of the file's history. Before that, `GET …/ini` shows the image's file.
- 2.3: the form's settings live in `php_settings`, not the settings registry, for the reason
  nginx's do (M1, item 4).
- 2.4: a pool name longer than 48 characters is cut and given 8 hex digits of the domain's hash,
  to keep the socket path inside the 108 bytes a Unix address allows. Pool settings are stored in
  `php_pools`; the site's PHP error log is `/www/panel/logs/sites/<domain>.php.log`, created for
  `www` with the site.
- 2.5: `php.conf` holds both `location` blocks, so the version switch rewrites one include and
  the site file is never touched. `location /` falls back to `/index.php?$query_string`.
  A PHP site's web root gets the same `index.html` placeholder as a static site unless the
  phpinfo box is ticked.
- 2.6: a fourth file can be part of the switch: the new version's `php-fpm.conf`, `zz-panel.ini`
  and `php.ini` when the version has never had a site.
- 2.7: the nginx container gained a mount, `/www/panel/run`, so its spec hash changed: the first
  boot of this build recreates the nginx container.
- 2.8: three routes beyond the list answer through existing ones: the php.ini history is
  `/api/config-history/php%2F<ver>%2Fphp.ini`, restored through that version's service.
  `PUT …/settings` and `…/ini` on a version that is not pulled are `409`.
- 2.8: the Playwright journeys were not extended to PHP: the e2e stand-in has no PHP manager, so
  the PHP page shows its 503 there. Real PHP is covered by the integration test and the smoke.

**Open after M2.** Against §1.3:

- Item 2 as for M1: the smoke ran on the sandbox VM as it was, with a binary cross-compiled from
  the working tree and installed with `panel upgrade`, not on a reverted snapshot with the CI
  release binary.
- Item 3: smoke 1 was re-run after the M2 build and passed; smoke 0 was not.
- Item 1: still no `eslint` script.

Also: nothing in M2 has run on EL9. After a reboot Docker did not restart the panel's containers
by itself on the sandbox VM; the boot reconcile starts nginx and every PHP version that has a
site, so a version with no site stays stopped until a site uses it again, which is when its
service starts it. The PHP integration test needs `-tags integration`, Docker and
port 80, and does not run in CI. A site create or version switch runs up to five one-shot
containers for validation and can take several seconds. Removing a version's image is M8's;
until then a pulled version stays.

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

### Status (2026-11-17)

Steps 3.1–3.12 are built. The exit gate is `scripts/smoke/3-file-manager.sh` (step 3.12), which
runs against a fresh Ubuntu VM and covers browse/create/rename/chmod/copy/move, trash with typed
confirmation, editor conflict, chunked upload with offset and checksum, archive round-trip,
hostile-path refusals and the Files settings. It has not been run on a host yet; the run is the
M3 exit gate and is recorded here when it happens.

| Step | State | Evidence |
|------|-------|----------|
| 3.1–3.10 | done | Unit and API tests: `fm_linux_test.go` (policy table, symlink-swap races, chmod/copy/trash), `handlers_fm_linux_test.go` (route matrix, hostile archive, upload offsets and checksums, editor conflict), `policy_test.go`, `settings_test.go` (Files group). |
| 3.11 | done | The review findings are the guardrails themselves: hard-deny list, `O_NOFOLLOW` everywhere, two-pass extract, typed confirmations. Regressions are pinned by the tests above. |
| 3.12 | built, not host-proven | `scripts/smoke/3-file-manager.sh`. `bash -n` and a dry run pass. Needs a real Ubuntu VM; see "Open after M3". |

**Open after M3.** Against §1.3:

- Item 3: smoke 0 was not re-run after the M3 build (M3 does not touch its surface).
- Item 5: every state-changing `/api/fm/*` route writes an audit entry, asserted in
  `TestFMAuditRecordsPaths`; the audit mirror on the VM is not checked by the smoke script.
- The smoke script itself has never run against a live VM: this repo has no Linux host wired up
  in this environment. The script is committed so the next gate run on `.106` picks it up.

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

### Status (2026-11-17)

M5 is built and its exit gate passes: `scripts/smoke/5-apps.sh` ran 62/62 on Ubuntu 26.04.1
(`sandbox-u2604`) against the real supervisord v0.7.6, with the `panel install` re-run check
(`SMOKE_REINSTALL=1`). EL9 has not been run.

The first live run found two bugs the in-memory fake had hidden, both fixed with regression tests:
the daemon reports process states as `Running`, not `RUNNING` (so a live process read as stopped),
and it writes `out.log.<timestamp>` and never a plain `out.log` (so the log was always empty).

| Step | State | Evidence |
|------|-------|----------|
| 5.1 | done, confirmed live (smoke 5) | RPC subset recorded in `internal/apps/doc.go`. The Go supervisord serves its XML-RPC on a Unix socket (`[unix_http_server]`, `chmod=`). It has **no `update` call**: `reloadConfig` then `addProcessGroup` / `removeProcessGroup` is what `supervisorctl update` does, and `addProcessGroup` leaves the program stopped. Its `ctl` subcommand does **not** work over a Unix socket (upstream README), so the planned `ctl` fallback does not exist; a missing call is a missing feature. |
| 5.2 | done | `internal/install/supervisord.go`. v0.7.6 for amd64 and arm64, pinned by SHA-256 (the release publishes bare binaries and a per-asset digest, no checksums file). Tests: a mismatch aborts and leaves an installed binary untouched; plain http is refused; a re-run issues no mutating command. |
| 5.3 | done | `internal/runtimes`. nvm at tag `v0.40.3`, run through one allowlisted `bash -c` with the version as a positional argument after a strict pattern check. |
| 5.4 | done | `internal/apps`. Golden program file, validator, environment rules, rollback of both the files and the row. |
| 5.5 | done | Manager tests run the real `confedit` editor against a fake supervisord that follows the upstream reload semantics. |
| 5.6 | done | `?proxy=<url>` on `/sites`; asserted in the Playwright journey. |
| 5.7 | done | `/api/apps`, `/api/runtimes`; Runtimes and Apps pages; 17 new vitest cases; `web/e2e/apps.spec.ts` against an in-memory supervisord. |
| 5.8 | done | 62/62 on the VM. It cross-compiles `scripts/smoke/testdata/apps-hello` for the Go app. The socket check asserts that `www` is refused, because upstream ignores `chmod=0700` and leaves the socket mode 755. |

Deviations from the step text:

- **Log tail** reads the log file the program file names, not `readProcessStdoutLog`. It needs no RPC, and the SSE follower resumes from a byte offset.
- **One config editor** now serves nginx, PHP and the supervisor program files (`cmd/panel/apps.go`), so one journal under `history/` covers all three and a replay can write any of them. `serve` replays the journal once at boot, before anything else and independent of Docker, so an unfinished apps apply is undone on a host without Docker too.
- **Command and environment values are validated, not escaped.** `%` (supervisord interpolates it), newlines and NUL are refused everywhere, and `"` in an environment value, because no escape for them has been verified against the Go daemon.
- **A program that will not stay up is a warning, not a failed apply.** The configuration is correct and the operator needs it saved to fix the program.
- **Removing a Node version** removes its directory; it does not call `nvm uninstall`, so removal needs no shell. It refuses while an app uses the version.
- **nvm is pinned by commit.** The tag `v0.40.3` is cloned, then `HEAD` must equal `977563e…`; a moved tag is refused and the checkout removed. Checked live from a fresh clone.

Confirmed live: create, start, stop, delete and restart-after-`kill -9` through the RPC; `user=`
drops privileges (the Node app runs as `www`); the socket refuses `www`; apps keep their PIDs
across `systemctl restart panel` and a `panel install` re-run. `chmod=0700` is **not** honoured
(socket mode 755), which is why the smoke asserts the refusal instead of the mode.

Also confirmed live: an edit to a running app is reported as a change and the app restarts on a new
PID (step 6b), and a program that dies inside `startsecs` is saved with a "did not start" warning
instead of failing the save.

Not covered: EL8–EL10. `osdep` has only the Ubuntu implementation by decision (D29), and the EL pass
is M9.

Fixed on the way, not caused by M5: `modeOK` accepted a directory looser than required, so a
loosened `data/` was never tightened (`TestRepairFixesALoosenedMode` failed at `71c35bc`);
`scripts/lint-conventions.sh` failed on comment-only matches and lost its status in subshells, and
flagged two M4 strings for the word `ufw`; the login journey asserted that Files is a disabled
placeholder, which stopped being true in M3 (it now checks TLS).

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

### Status (2026-11-17)

M6 is **built, not gated**. Steps 6.1–6.8 are implemented and unit-tested; the exit gate (smoke 6)
has not run because it needs a public VM, a Cloudflare zone and Let's Encrypt staging.

| Step | State | Notes |
|------|-------|-------|
| 6.1 | done | `internal/secretbox`; sealed value `v1:<keyid>:<b64>`, bound to its row through the AAD. |
| 6.2 | done | `internal/tls` store + `ValidateUpload`; migrations 0012, 0013 (`certs.provider`). Unix mode tests pass on Linux. |
| 6.3 | done, real nginx 1.29 | The redirect is a server-level `if` chain in `tls.conf`; the ACME path is exempt. The nginx container mounts only `ssl/sites` and `ssl/acme-webroot`, read-only, never `ssl/` (panel key, ACME account key). |
| 6.4 | done, Pebble | HTTP-01 through lego's webroot provider, with a pre-flight that never reaches the CA on failure. Passes against a real Pebble in WSL Docker. |
| 6.5 | done, Pebble | DNS-01 wildcard passes against Pebble. The Cloudflare client is a direct v4 client (no process env) and is tested only against an httptest fake. |
| 6.6 | done | Renewal scan, 30-day window, 6 h backoff after a failure. `MarkNotApplied` lets a failed install be retried without ordering again. Since M7 the scan is the cron built-in `ssl-renew` (daily 03:17), not a separate loop. |
| 6.7 | done | `CertReloader`: swap on success only, `.prev` kept for one rollback, startup recovery from a half-written pair. The panel hostname issued by ACME is recorded in `ssl/panel-cert-domain`. |
| 6.8 | done | 13 routes under `/api/sites/{domain}/tls`, `/api/tls/*`, `/api/panel/cert*`; TLS tab and `/tls` page. 202 vitest. |
| 6.9 | written, not run | See below. |

Deviations: no `tls.acme.email` setting (the registry cannot hold an empty default; the email is
stored per ACME account). Terms of service are accepted automatically at first registration.
`web/dist` is not rebuilt here. `web/` has no ESLint config, so the "eslint green" item of §1.3
cannot be met yet.

**Step 6.9 status: written, not run.** `scripts/smoke/6-tls.sh` follows the exit gate above and
needs a VM with a public address, a test domain on Cloudflare and Let's Encrypt staging, none of
which exists on the sandbox. Required environment: `SMOKE_HOST`, `TLS_TEST_DOMAIN` (an A record
pointing at the VM), `TLS_TEST_WILDCARD_ZONE` (a Cloudflare zone; the wildcard is
`*.smoke6.<zone>` unless `TLS_TEST_WILDCARD_SUB` is set) and `CF_API_TOKEN` (Zone:DNS:Edit, never
printed). It refuses to run when `tls.acme.directory` is not the staging URL unless
`TLS_ALLOW_PRODUCTION=1`. It was checked with `bash -n`, `dash -n`, a dry run and its pure helpers
against local fixtures only; the API field names it reads (`job_id`, `source`, `redirect_http`,
`serial`) follow the 6.8 contract and are unproven. It also assumes `DELETE
/api/tls/providers/{name}` exists for cleanup.

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

### Status (2026-11-17)

M7 is **built through 7.7 (backend) and 7.8 is written, not gated**. The exit gate (smoke 7) has
not run on any host. API contract: `docs/design/m7-api.md`.

| Step | State | Notes |
|------|-------|-------|
| 7.1 | done, fixtures only | `internal/sysinfo` parsers and sampler against `/proc` fixtures; never read a live `/proc` on a host. |
| 7.2 | done, fakes only | Service list and control through fake runners; nothing ran `systemctl` or Docker. |
| 7.3 | not done here | Dashboard UI belongs to the `web/` work and is not covered by this record. |
| 7.4 | done, fixtures only | Log registry, SSE tail, NDJSON search incl. gz fixture. |
| 7.5 | not recorded here | Rotation is not exercised by smoke 7. |
| 7.6 | done, fake clock | `internal/cron` scheduler, overlap, shell gate (`cron.allow_shell`). No real run as another user on a host. |
| 7.7 | backend done, fakes only | `/api/sysinfo`, `/api/services`, `/api/logs`, `/api/cron`; run detail is `GET /api/cron/{id}/runs/{runId}`. UI and Playwright are not part of this record. |
| 7.8 | written, not run | See below. |

**Step 7.8 status: written, not run.** `scripts/smoke/7-dashboard-logs-cron.sh` follows the exit
gate above and needs a disposable Ubuntu VM with Docker, the nginx container `panel-nginx`
publishing port 80, and root SSH. It was checked with `bash -n`, `dash -n` and a dry run only
(shellcheck is not installed in the WSL image). It is disruptive: step 3 stops the nginx container
for a few seconds, and an exit trap starts it again.

Exit gate clauses and what the script covers:

| Clause | In the script | Proven |
|--------|---------------|--------|
| Stop nginx from the host, panel shows it stopped | `docker stop`, then polls `GET /api/services` for `stopped`; curl on port 80 must fail | No |
| Restart, sites serve | `POST /api/services/panel-nginx/start` (204), then curl with the site's Host header; also `restart` | No |
| Cron `* * * * *`, output in run history and log file within two minutes | script job run by the scheduler (`source` is `schedule`), waited up to 150 s; run output tail, `log_path` read with `cat` on the VM, and `cron:<job>` tail in the log viewer | No |
| Search a site access log for the script's request | the cron script requests a unique path from the site; the search match count must equal `grep -c` on the VM | No |

Also covered: snapshot memory total against `/proc/meminfo`, uptime within 120 s, SSE stream yields
a `snapshot` event, `panel` stop refused (403 `not_allowed`), unknown service 404, log source list
without server paths, tail, literal / regex / case-insensitive / bad-pattern search, shell job refused
(403 `shell_not_allowed`) then accepted after `PUT /api/settings` sets `cron.allow_shell` and refused
again on run once it is off, manual run, enable and disable, duplicate 409, validation 400, cross-job
run 404, delete removing history and the log directory.

Not covered by the script, and not proven anywhere: the dashboard UI and its alerts (7.3), "restart
from the UI" (the script uses the API, the UI path is for Playwright), log rotation (7.5), and the
jobs-run-as-another-user path (the job runs as root).

Unproven assumptions in the script: the key order and shapes in `m7-api.md` (`"memory":{...}` with
`total_bytes` first, `"latest":{`, run field names, `"outcome":"started"`); `PUT /api/settings` takes
`{"settings":[{"name","value"}]}` with the value as a string for a bool, and `GET /api/settings`
shows `cron.allow_shell` with a `"value"` field; the panel's service state for a container stopped
behind its back turns `stopped` within 45 s; `POST .../start` on a running or stopped container
answers 204; the cron script may live in `/usr/local/bin` and the job may run as `root`; run logs
sit under `/www/panel/logs/cron/<job>/` and deleting a job removes that directory; the site access
log is `/www/panel/logs/sites/<domain>.access.log` and is written without buffering delay beyond 20 s;
the `docker:panel-nginx` and `panel:audit` source ids exist; a POST to `/run` on a yearly job answers
202 `started`; running a stored shell job after the setting is turned off answers 403.

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

### Status (2026-11-17)

Built. The real-Engine checks pass in WSL (Docker 29.9, `php:8.3-fpm`): `TestRealImageLifecycle`
builds PHP with `intl` and `redis`, swaps it under a running container, rejects a build of a missing
package with the container untouched, and activates the earlier build. Smoke 8 is written and its dry
run passes, but it has **not been run on a host**, so the exit gate is open.

| Step | State | Evidence |
|------|-------|----------|
| 8.1 | done | Migration `0015_images.sql`: `image_builds` (one row per build) and `image_pointers` (current and previous build per family). Images are `panel/php:<ver>-<build-id>` and `panel/nginx:1.29-<build-id>`. `TestNewIDHasTheShapeTheTagsNeed`, the family tests. |
| 8.2 | done | `php.Dockerfile(ver, exts)` golden tests. The build context is a host directory (`/www/panel/build/php/<ver>/<id>`) sent with `docker.BuildOptions.ContextDir`; a symbolic link in it is refused (`TestTarDirRefusesRelativeAndLinkedContexts`). A failed build step fails the job, and the tag is inspected after the stream (risk 11): `TestBuildFailureKeepsTheServingImage`, and the real build of a missing package. |
| 8.3 | done | `php.Catalogue()`: 31 entries, bundled and PECL, with their apt build packages. A custom PECL name must match `^[a-z][a-z0-9_]{1,39}$`; at most 32 names per build (`TestParseExtensionsRefusesWhatIsNotAPackageName`). The list is the same for every supported version; an entry that does not compile on a version fails that build, with its output kept. |
| 8.4 | done, with a deviation | The candidate is checked in a one-shot container of the candidate image, never by exec into the running one. Promotion stops the old container gracefully (SIGQUIT, up to 20 s), removes it, starts the candidate from its spec and checks it. If the candidate does not come up, it is removed and the previous image is started again (`TestSwapRestoresThePreviousImageWhenTheNewOneFails`). **Deviation:** the step says to start the new container before stopping the old one. Both cannot run at once: they share host networking and socket paths. A swap therefore leaves a short gap in which requests get no answer. Smoke 8 allows 5% of its request loop to fail; the gap has not been measured on a host. |
| 8.5 | done | Install is pull, build, check, promote. `DELETE /api/php/{ver}` is refused with 409 while a site uses the version. Removal stops the container and deletes the version's images, but keeps `php.ini`, the form settings and the pools, so installing again restores them. |
| 8.6 | done | nginx update is a fresh base pull (`Pull: true`), a candidate `nginx -t` in a one-shot container, the same swap, and rollback through activation of the previous build (`TestRollbackPutsThePreviousBuildBack`). If nginx is not running, an update records the build and does not start nginx; the boot reconcile starts it on the build that serves. |
| 8.7 | done | UI: the PHP page gets extension chips, a Rebuild dialog with the catalogue and custom PECL names, Remove (409 detail shown), and build history with Activate; the nginx status tab gets an Image card with Update and Roll back. vitest 274/274 and `tsc` clean (not clicked through in a browser). API, all under `/api`: `GET /api/php/extensions`; `POST /api/php/{ver}/pull` with an optional body; `POST /api/php/{ver}/build`; `DELETE /api/php/{ver}`; `GET /api/php/{ver}/builds`; `POST /api/php/{ver}/builds/{id}/activate`; `GET /api/nginx/image`; `POST /api/nginx/image/update`; `POST /api/nginx/image/builds/{id}/activate`. All are in the 401 and CSRF matrix (`routes_test.go`) and have behaviour tests (`handlers_images_test.go`). |
| 8.8 | written, not run | `scripts/smoke/8-docker-mgmt.sh`; the dry run passes. |

**A first build is checked less.** Nothing serves before the first apply, and the apply writes the
configuration that a check would test against (the FPM file and `php.ini` for PHP; nginx's include
files for nginx). So a first build checks only the PHP modules, and skips `nginx -t`. The first apply
then validates the configuration with the image that serves at that point. This was found by the real
sites integration test (`TestRealNginxTLS`), which failed when the first build ran `nginx -t` before
the panel had written its includes.

**Boot.** The image catalogue is reconciled before nginx. A build left `building` is failed. A family
with no current build but an image from before builds had IDs (`panel/php:<ver>-fpm`,
`panel/nginx:1.29`) adopts that image: it is tagged with a build ID, and the old tag stays. A version
whose image is gone becomes `absent`.

**Also in this milestone.** The Docker client gained `ImageTag`, `ImageRemove` and directory build
contexts. The php and nginx containers take their image from the catalogue (`php.Spec(p, ver, image)`,
`nginx.Spec(p, image)`), so a swap takes effect at the next create.

**Not done, and why.** Nothing has been run on the Ubuntu VM or on Rocky 9 in this session, so the
swap gap is unmeasured and smoke 8 is unproven. EL9 is M9.

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
