# Security checks: plan

Status: **plan, not started; decisions unratified.** Migration number reserved: `0026`.

Written after reading the distro boundary (`internal/osdep/osdep.go`, `ubuntu.go`), the SSH journal
monitor (`internal/sshauth/*`), the firewall (`internal/firewall/service.go`, `apply.go`, `ssh.go`,
`panelport.go`, `model.go`, migration `0010_firewall.sql`), ClamAV (`internal/clamav/clamav.go`,
`internal/httpapi/handlers_clamav.go`), the web-root allowlist (`internal/webroots/webroots.go`),
panel self-update (`internal/update/*`), cron built-ins (`internal/cron/builtin.go`,
`cmd/panel/tls.go`), the settings table (`internal/settings/keys.go`), the jobs API
(`internal/jobs/jobs.go`), the panel certificate reloader (`internal/httpapi/certreload.go`), the
daemon wiring (`cmd/panel/serve.go`, `m7.go`), the unit (`internal/install/unit.go`) and the scope
doc (`docs/plan.md`). apt, sshd and unattended-upgrades behaviour below is prior knowledge, not a
check of Ubuntu 26.04: **step 0 confirms or corrects each item before code is written.**

Scope: Ubuntu first. EL (Rocky/RHEL, dnf, SELinux) stays in **M9**; §8 lists what changes there.

Sibling plans written in parallel: `docs/design/notifications-plan.md` owns `internal/notify`;
the auto-ban plan (`docs/design/auto-ban-plan.md`) owns brute-force banning. This plan
reads SSH failures only as evidence and never bans.

## 1. Goal

A **Security** page that answers "what on this server should I fix first":

- A score (0-100) and per-family summary from the last scan, with the scan time.
- Findings, each with a severity, the evidence that raised it, a **Fix** button where the fix is
  safe and reversible, and **Accept risk** with a required reason (optionally expiring).
- Five families: OS updates, sshd hardening, network exposure, web-root file integrity, the panel.
- Findings history (opened / resolved / reopened / accepted / fixed), scans on demand as a job
  and on schedule as cron built-ins, and notification events on every open/resolve transition.

What does **not** change: the firewall's rule model and its 60 s confirm flow (`firewall.Manager`
is read, never re-implemented for iptables); the SSH attempts page (`SSHPage.tsx`) and its
monitor; ClamAV's lifecycle (we call `Scan`, nothing else); the settings precedence; the `www`
1500:1500 ownership model (`docs/plan.md` decision 23, `internal/install/layout.go`). No scan ever
changes the host; only an operator's click does.

## 2. Current state

| Area | What exists | Gap for this feature |
|---|---|---|
| Distro boundary | `osdep.OS`: `InstallPackages`, `EnsureUser`, `EnsureDocker`, `MaskFirewallServices`, `SELinux`. `Runner.Run` returns only an error; `UbuntuEnv` sets `DEBIAN_FRONTEND=noninteractive` | No package-update query, no command output seam, no sshd unit name. `docs/plan.md` §3.1: only `osdep` may know apt vs dnf |
| sshd config | `firewall.DetectSSHPorts` runs `sshd -T` and parses only `port` (`internal/firewall/ssh.go`), with a config-file fallback | Nothing reads other effective keys; nothing writes sshd config |
| SSH evidence | `sshauth.Monitor.Attempts` returns journal attempts with `method` (`password`/`publickey`) and user (`monitor.go`, `parse.go`) | Usable as lockout-guard evidence: "user X logged in by publickey recently" |
| Confirm/revert | `firewall.Manager`: `Apply` → `Pending{Token,StartedAt,Deadline}`, `Confirm(token)`, `Revert`, timer via `After`, `Recover` at boot rolls back any persisted pending row (`apply.go`, table `fw_pending`) | Pattern to copy for sshd; the type is iptables-specific |
| Firewall state | `Service.State`: `Enabled`, `Dirty`, `SSHPorts`, `PanelPort`, `DockerPorts` (`PublishedPorts` from `docker ps`), `Builtins`; `Rules()` | Enough to compute "intended exposure" |
| Listening sockets | **None.** No `ss`, no `/proc/net/tcp` reader anywhere in `internal/` or `cmd/` | New reader needed (`internal/sysinfo` already parses `/proc`) |
| ClamAV | `clamav.Manager.Scan(ctx, path, progress)` → `ScanResult` with `[]Infection`; `Status().Ready()`; one scan at a time (`ErrScanBusy`) | One path per call; a list of flagged files needs N calls or a new `ScanFiles` |
| Web roots | `sites.Site.Root`, `nginx.Paths.SiteRoot(domain)` default `/www/services/sites/<domain>`; `webroots.Holder` allowlist | Scan targets = roots of static/PHP sites; proxy sites have none |
| Panel version | `update.Checker.Info` → `Current`, `Latest`, `Available`, cached 6 h (`check.go`) | Directly usable |
| Panel TLS | `CertReloader` `IsBootstrap()` / `SelfSigned` (`certreload.go`) | Directly usable |
| Settings | `panel.port` 8443, `panel.bind` 0.0.0.0, `login.max_attempts` 5, `login.lockout` 15m, `session.idle_timeout` 30m, `cron.allow_shell` false | Read for the panel family; new group `Security` |
| Cron | `defaultJobs` seeded by `EnsureDefaults`; `cronBuiltins` map in `cmd/panel/tls.go`; operator owns schedule after seeding | Add two built-ins |
| Jobs | `jobs.Manager.Start(ctx, kind, Func)`; handlers start with `context.WithoutCancel` (`handlers_clamav.go`) | Scan, apply-updates, baseline, fix all run as jobs |
| Daemon unit | `NoNewPrivileges=yes`, `MemoryDenyWriteExecute`, filesystem protections off (`internal/install/unit.go`) | A full `apt-get upgrade` must not inherit these (D11) |
| 2FA | Not implemented (`grep -ri totp internal` is empty) | D12 |
| Notifications | `internal/notify` does not exist yet (sibling plan) | Emit through `notify.Emitter` only |

## 3. Decisions (unratified recommendations)

| # | Decision | Recommendation |
|---|---|---|
| D1 | Where the code lives | New `internal/security` (engine, catalogue, store, score, one file per family) and `internal/security/sshfix` (drop-in + confirm). Package queries go into `osdep` (new `Updates` interface on `*Ubuntu`); the socket reader into `internal/sysinfo/sockets.go`. `security` never names apt or a distro |
| D2 | Counting security updates | Parse `apt-get -s -o Debug::NoLocking=1 upgrade --with-new-pkgs` `Inst` lines; a package is security when any archive in its `(…)` list ends in `-security` (covers "noble-updates, noble-security"). Phased-deferred packages are counted separately and not as pending. Step 0 cross-checks against `/usr/lib/update-notifier/apt-check` where present; it is **not** relied on (absent on minimal installs) |
| D3 | When scans run | The page reads the **last stored run**; it never shells out on load. A scan is a job (`security.scan`), started by the button or by the cron built-ins. Only the scan job runs `apt-get update` |
| D4 | sshd fix mechanism | One panel-owned drop-in `/etc/ssh/sshd_config.d/00-pbpanel.conf` (sshd takes the **first** value it reads, so it must sort before cloud-init's `50-cloud-init.conf`), mode 0600, written atomically; `sshd -t` must pass; `systemctl reload ssh` (reload, never restart: live sessions keep their process); verify with `sshd -T`; then a **120 s** confirm window. Pending state persisted (`security_ssh_pending`); daemon start with a pending row reverts it, like `firewall.Manager.Recover` |
| D5 | Lockout guard | Refuse `PasswordAuthentication no` unless at least one account that sshd will still admit has a non-empty, parseable `AuthorizedKeysFile` entry (paths from `sshd -T`). Refuse `PermitRootLogin no` unless a non-root account in `sudo`/`admin` has a key. Show as evidence, and warn (not block) when the journal shows no publickey login for that account in 30 days. The Fix dialog tells the operator to open a **new** SSH session before confirming |
| D6 | SSH port change | **No auto-fix in v1.** A port change needs the firewall rule, the `ssh.socket` listener on Ubuntu 22.10+, and later SELinux on EL; the finding is `info` with instructions |
| D7 | What is auto-fixable | Only: enable security-only unattended-upgrades, sshd keys in D4 (root login, password auth, empty passwords, max auth tries), `chmod o-w` on a world-writable web-root file, `chown 1500:1500` on a wrongly owned web-root file, (re)baseline a site. Everything else links to the page that owns it (Firewall, TLS, Settings, Updates). Scheduled scans never fix |
| D8 | Accept risk / ignore | Keyed by `(check_id, target)`; reason required (1-500 chars); optional expiry; `target='*'` ignores the whole check. Accepted findings stay visible, are excluded from the score, and re-open when the acceptance expires. Acceptances outlive the finding, so a flapping finding stays accepted |
| D9 | Score | `100 − Σ weight` over open, unaccepted findings: critical 25, high 10, medium 4, low 1, info 0; floor 0. Per-family counts shown beside it. Simple and explainable; no hidden caps |
| D10 | Integrity baseline | SQLite rows per file (path, size, mode, uid, gid, mtime, sha256). Hash every regular file ≤ `security.integrity.max_hash_bytes`; larger files compare size+mtime. Findings only for "code" extensions (`.php .phtml .phar .php[3-8] .inc .htaccess .user.ini`); other changes are counted in the `integrity.changed` event. Per-site cap `security.integrity.max_files`; over the cap → `integrity.truncated` finding, not silent |
| D11 | Applying updates | `apt-get upgrade --with-new-pkgs` (never `dist-upgrade`), `--force-confdef --force-confold`, `-o DPkg::Lock::Timeout=300`, `NEEDRESTART_MODE=l`. Security-only = `unattended-upgrade -v` (it already restricts to allowed origins). Run in a **transient unit** via `systemd-run --wait --pipe --collect --unit=pbpanel-apt-<id>` so it escapes the panel unit's `NoNewPrivileges`/`MemoryDenyWriteExecute` and survives a panel restart, as `internal/update/start.go` does for upgrades. Never reboots |
| D12 | 2FA finding | **Omit** until 2FA exists; a permanent unfixable finding trains operators to ignore the page. Reserve id `panel.2fa_off` |
| D13 | Notifications | Emit `security.finding` only on transitions (opened, reopened, resolved), Key = `check_id + ":" + target`, `Resolved` true on clear; one `integrity.changed` per site per scan with counts, only when something changed. Depend on `notify.Emitter`; nil → `notify.Nop` |
| D14 | Listening sockets source | Parse `/proc/net/{tcp,tcp6,udp,udp6}` and map inodes via `/proc/<pid>/fd` (root). No `ss` dependency, fixture-testable |

## 4. Design

### 4.1 Check catalogue

Severity is the default; a check may raise it with evidence (noted). Fix = button in the UI.

| id | Family | Detected by | Severity | Fix |
|---|---|---|---|---|
| `updates.pending` | updates | D2 count of non-security upgradable packages > 0 | low (medium ≥ 50) | yes: apply all (job) |
| `updates.security` | updates | D2 count of security packages > 0; evidence lists packages | high | yes: apply security-only (job) |
| `updates.reboot_required` | updates | `/var/run/reboot-required` exists; evidence = `.pkgs` | medium | no (operator reboots) |
| `updates.unattended_off` | updates | `apt-config dump` lacks `APT::Periodic::Unattended-Upgrade "1"` or package absent | medium | yes: install + enable security-only |
| `updates.lists_stale` | updates | newest mtime in `/var/lib/apt/lists` > 7 days (scan's own `apt-get update` failed) | low | no |
| `ssh.root_login` | ssh | `sshd -T` `permitrootlogin yes` | high | yes: `prohibit-password` (default) or `no` (guarded, D5) |
| `ssh.password_auth` | ssh | `passwordauthentication yes`, or `kbdinteractiveauthentication yes` with `usepam yes` | medium (high if root login also `yes`) | yes: both off (guarded, D5) |
| `ssh.empty_passwords` | ssh | `permitemptypasswords yes` | critical | yes |
| `ssh.max_auth_tries` | ssh | `maxauthtries` > 4 | low | yes: 4 |
| `ssh.default_port` | ssh | `port 22` | info | no (D6) |
| `ssh.unreadable` | ssh | `sshd -T` fails or sshd absent | medium | no |
| `exposure.firewall_off` | exposure | `firewall.State.Enabled == false` | high | no: link to Firewall |
| `exposure.firewall_dirty` | exposure | `State.Dirty` | low | no: link |
| `exposure.public_listener` | exposure | socket on wildcard/non-loopback addr whose port is not intended (rule, SSH, panel, 80/443) | medium; high for 3306/5432/6379/11211/27017/9200 or any port when the firewall is off; info when firewall on and blocking | no: evidence names process and suggests binding to 127.0.0.1 |
| `exposure.docker_published` | exposure | `firewall.PublishedPorts` non-empty (bypasses INPUT, `docs/plan.md` §3.5) | high | no |
| `exposure.unused_rule` | exposure | enabled rule with no listener on its ports | info | no |
| `panel.default_port` | panel | `panel.port` = 8443 | info | no |
| `panel.bootstrap_cert` | panel | `CertReloader.IsBootstrap()` | medium | no: link to TLS |
| `panel.outdated` | panel | `update.Info.Available` | medium | no: link to update |
| `panel.login_throttle_weak` | panel | `login.max_attempts` > 10 or `login.lockout` < 5m | low | no: link to Settings |
| `panel.idle_timeout_long` | panel | `session.idle_timeout` > 12h | low | no: link |
| `panel.cron_shell` | panel | `cron.allow_shell` = true | info | no: link |
| `integrity.no_baseline` | integrity | site with a root has no baseline | info | yes: create baseline |
| `integrity.php_changed` | integrity | code file hash differs from baseline | high | yes: accept into baseline |
| `integrity.php_new` | integrity | code file not in baseline | high | yes: accept into baseline |
| `integrity.php_in_uploads` | integrity | code file under a path segment `uploads`/`upload`/`files` (baseline or not) | high | no |
| `integrity.world_writable` | integrity | mode & 0o002 on file or dir | medium | yes: `chmod o-w` |
| `integrity.wrong_owner` | integrity | uid/gid ≠ 1500 | low | yes: `chown 1500:1500` |
| `integrity.malware` | integrity | ClamAV infection on a flagged file | critical | no: link to Files |
| `integrity.truncated` | integrity | per-site file cap hit | low | no |

Target per finding: package-free for updates (`-`), the sshd key for ssh, `proto/port` for
exposure, the setting name for panel, `<site>:<relpath>` for integrity file findings (one finding
per file, capped at 200 per check per site with an "and N more" evidence line).

### 4.2 Data model (migration `0026_security.sql`)

```sql
CREATE TABLE security_runs (
  id INTEGER PRIMARY KEY, trigger TEXT NOT NULL,            -- manual | cron | fix
  families TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '',
  score INTEGER, counts TEXT NOT NULL DEFAULT '{}', errors TEXT NOT NULL DEFAULT '{}', job_id TEXT NOT NULL DEFAULT '');
CREATE TABLE security_findings (
  id INTEGER PRIMARY KEY, check_id TEXT NOT NULL, target TEXT NOT NULL, family TEXT NOT NULL,
  severity TEXT NOT NULL, status TEXT NOT NULL,             -- open | resolved
  title TEXT NOT NULL, evidence TEXT NOT NULL,              -- JSON, bounded 16 KiB
  first_seen TEXT NOT NULL, last_seen TEXT NOT NULL, resolved_at TEXT NOT NULL DEFAULT '',
  last_run INTEGER NOT NULL REFERENCES security_runs(id), UNIQUE (check_id, target));
CREATE TABLE security_accepts (
  check_id TEXT NOT NULL, target TEXT NOT NULL, reason TEXT NOT NULL,
  accepted_at TEXT NOT NULL, expires_at TEXT NOT NULL DEFAULT '', PRIMARY KEY (check_id, target));
CREATE TABLE security_events (                              -- findings history
  id INTEGER PRIMARY KEY, check_id TEXT NOT NULL, target TEXT NOT NULL, at TEXT NOT NULL,
  kind TEXT NOT NULL,                                       -- opened|resolved|reopened|accepted|unaccepted|fixed|fix_failed
  run_id INTEGER, detail TEXT NOT NULL DEFAULT '');
CREATE INDEX security_events_target ON security_events(check_id, target, at);
CREATE TABLE security_integrity_sites (
  site TEXT PRIMARY KEY, root TEXT NOT NULL, baselined_at TEXT NOT NULL,
  files INTEGER NOT NULL, truncated INTEGER NOT NULL DEFAULT 0);
CREATE TABLE security_integrity_files (
  site TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, mode INTEGER NOT NULL,
  uid INTEGER NOT NULL, gid INTEGER NOT NULL, mtime TEXT NOT NULL, sha256 TEXT NOT NULL,
  PRIMARY KEY (site, path)) WITHOUT ROWID;
CREATE TABLE security_ssh_pending (
  id INTEGER PRIMARY KEY CHECK (id = 1), token TEXT NOT NULL, started_at TEXT NOT NULL,
  deadline TEXT NOT NULL, prev_present INTEGER NOT NULL, prev_body TEXT NOT NULL, new_body TEXT NOT NULL);
```

Retention: `security_runs` and `security_events` trimmed to 365 days by the scan job. Site
delete removes its integrity rows (hook in the sites manager's delete path).

### 4.3 Engine

`security.Check` = `{ID, Family, Run(ctx, Env) ([]Finding, error)}`; `Env` carries the seams
(osdep `Updates`, sshd `Outputter`, `firewall.Service`, socket reader, settings registry,
`update.Checker`, cert reloader, sites lister, ClamAV). A family that errors records the error in
`security_runs.errors` and **keeps its previous findings** (a failed `sshd -T` is not "fixed").
Reconcile per family in one transaction: new → `opened`; previously resolved → `reopened`;
missing → `resolved`. Transitions feed history and `notify.Emitter`.

### 4.4 API routes (all `requireAuth`; mutating ones go through the existing audit middleware)

| Route | Purpose |
|---|---|
| `GET /api/security` | score, counts, last run, families (state, error), open + accepted findings, SSH pending |
| `GET /api/security/findings?status=&family=` | list incl. resolved (last 30 days) |
| `GET /api/security/findings/{id}/history` | events for that `(check_id,target)` |
| `GET /api/security/runs` | last 50 runs |
| `POST /api/security/scan` `{families?: []}` | 202 + job (`security.scan`); 409 while one runs |
| `POST /api/security/findings/{id}/accept` `{reason, expires_at?}` / `DELETE …/accept` | D8 |
| `POST /api/security/findings/{id}/fix` | 202 + job, or for ssh checks 200 + `Pending`; 422 when the guard refuses (message names the reason) |
| `GET /api/security/updates` | last scan's package list (name, from, to, security, phased) |
| `POST /api/security/updates/apply` `{security_only}` | 202 + job (`security.updates`) |
| `PUT /api/security/unattended` `{enabled}` | 202 + job |
| `POST /api/security/ssh/confirm` `{token}` / `POST /api/security/ssh/revert` | D4; mirror `/api/firewall/confirm` errors (expired, token, none) |
| `GET /api/security/integrity` | per-site baseline meta |
| `POST /api/security/integrity/{site}/baseline` | 202 + job; replaces the site's baseline with the current tree |

### 4.5 Settings keys (new group `Security`, all live)

| Key | Type | Default | Meaning |
|---|---|---|---|
| `security.integrity.exclude` | string | `wp-content/cache,storage/framework/cache,node_modules,.git` | relative path prefixes skipped per site |
| `security.integrity.max_files` | int | `200000` | per-site cap (range 1000-2000000) |
| `security.integrity.max_hash_bytes` | int | `67108864` | files above are compared by size+mtime |
| `security.integrity.clamav` | bool | `true` | scan flagged files when ClamAV is ready |
| `security.ssh.confirm_window` | duration | `120s` | range 60s-600s |

Cron built-ins (seeded, operator-owned afterwards): `security-scan` → `security.scan` at
`23 4 * * *` (all families but integrity), `security-integrity` → `security.integrity` at
`41 4 * * *`, both root, timeout 2 h, overlap skip.

## 5. Steps

Each step ends green on the gate (`go test ./cmd/... ./internal/...` in WSL, CI golangci-lint on
changed code, vitest for UI steps) and is committed on its own. `docs/API.md` is updated in the
step that adds a route.

### Step 0: spike on the Ubuntu sandbox (no product code)

By hand on the current Ubuntu sandbox; findings recorded in this file:

- apt: `apt-get -s upgrade --with-new-pkgs` output format on 26.04, the archive list for security
  packages, phased-update lines, and agreement with `apt-check` on a host with pending updates.
- unattended-upgrades: installed by default? default `Allowed-Origins`; does a panel file
  `51pbpanel-unattended` with `#clear Unattended-Upgrade::Allowed-Origins;` take effect
  (`apt-config dump`, `unattended-upgrade --dry-run -d`).
- sshd: `Include` position in `sshd_config`; first-wins with `00-pbpanel.conf` vs
  `50-cloud-init.conf`; `ssh.socket` activation and whether `systemctl reload ssh` re-reads
  config and keeps a live session; `sshd -T` vs `sshd -T -C user=…,host=…,addr=…` with a `Match`.
- `/proc/net/tcp6` dual-stack listeners vs `ss -tlnp` on the same host (Docker, nginx, panel).
- `apt-get upgrade` under `systemd-run --pipe --wait` from the panel unit: output streams,
  exit status, needrestart behaviour with `NEEDRESTART_MODE=l`, panel survives.
- Integrity scan time and DB size on a WordPress site (files/s, rows, MB).

Done when every item has an answer and D2, D4, D6, D11 are confirmed or amended.

### Step 1: store and engine

- What: migration `0026_security.sql`; `internal/security`: `Finding`, `Severity`, `Check`,
  `Engine.Run(ctx, trigger, families)`, reconcile, score (D9), accept/unaccept/expiry (D8),
  history, retention, `Emitter` hook (D13). Families registered later.
- Files: `internal/store/migrations/0026_security.sql`, `internal/security/{doc,model,engine,store,score}.go`.
- Tests: fake checks drive open → resolve → reopen; a family error keeps old findings; accepted
  excluded from score; expiry re-opens; emitter receives exactly the transitions; migration test.
- Done when: engine runs fake families end to end against a temp store.

### Step 2: osdep updates seam

- What: `osdep.Outputter` (argv → stdout, stderr in error); `osdep.Updates` on `*Ubuntu`:
  `PendingUpdates`, `RebootRequired`, `AutoUpdates`, `SetAutoUpdates(securityOnly bool)`,
  `ApplyUpdates(ctx, securityOnly, w io.Writer)` (D11 via `systemd-run`), `SSHUnit() = "ssh"`.
- Files: `internal/osdep/{updates.go,updates_ubuntu.go,updates_test.go}`, fixtures under `testdata/`.
- Tests: golden `apt-get -s` outputs (security, updates-only, phased, empty, locked); argv of
  apply and unattended toggle asserted through the fake runner; written apt.conf file content.
- Done when: parser matches step 0 fixtures exactly.

### Step 3: updates family + routes

- What: `updates.*` checks; jobs `security.updates` and unattended toggle; `GET/POST
  /api/security/updates*`, `PUT /api/security/unattended`; base routes `GET /api/security`,
  `/scan`, `/findings*`, `/runs`, accept. Wire `security.Engine` into `httpapi.Deps` and
  `cmd/panel/serve.go` (nil → 503, as ClamAV does).
- Files: `internal/security/updates.go`, `internal/httpapi/handlers_security.go`, `deps.go`, `cmd/panel/serve.go`.
- Tests: check table tests on fake `Updates`; handler tests (202 + job, 409 busy, accept validation).
- Done when: a scan job populates updates findings through the API in the httptest harness.

### Step 4: sshd read checks

- What: `sshd -T` parser (all keys, lower-cased; multi-value keys kept); `ssh.*` checks.
  Reuse `firewall.ParseSSHDT` semantics for `port`.
- Files: `internal/security/sshd.go`, `sshd_test.go`, fixtures from step 0.
- Tests: fixtures for Ubuntu default, cloud-init password-on, kbd-interactive trap, sshd missing.
- Done when: every ssh check is green on fixtures.

### Step 5: sshd fix with guard and revert

- What: `internal/security/sshfix`: render drop-in from the wanted keys merged with the
  current panel drop-in; guard (D5) using `sshd -T` `authorizedkeysfile`, `/etc/passwd`,
  `/etc/group`, and `sshauth.Monitor` for evidence; write → `sshd -t` (on failure restore and
  stop) → reload → `sshd -T` verify → persist pending → timer. `Confirm`, `Revert`, `Recover`
  (called from `serve.go` before routes, like the firewall's). Fix route returns `Pending`.
- Files: `internal/security/sshfix/{sshfix.go,guard.go,*_test.go}`, `handlers_security.go`.
- Tests: guard refuses with no key, with an unreadable/empty `authorized_keys`, root-only key for
  `PermitRootLogin no`; `sshd -t` failure leaves the old file byte-identical; expiry and restart
  revert (fake clock, fake `After`); prior drop-in absent vs present restored exactly.
- Done when: the full apply/confirm/expire/recover matrix passes against fakes.

### Step 6: sockets reader + exposure family

- What: `sysinfo.Listeners()` from `/proc/net/*` + inode → pid/comm (D14); exposure checks
  from `firewall.Service.State` + `Rules` + listeners.
- Files: `internal/sysinfo/sockets.go` (+`_linux`/`_other`), `internal/security/exposure.go`.
- Tests: `/proc/net` fixtures (v4, v6 wildcard, v4-mapped, loopback, UDP); exposure matrix:
  firewall off/on, rule covers port, Docker published port, DB port high.
- Done when: matrix green; non-Linux build returns `ErrUnsupported` and the family reports it.

### Step 7: panel family

- What: `panel.*` checks from settings, `update.Checker.Info(ctx,false)` (cached; never forces a
  GitHub call), `CertReloader.IsBootstrap`.
- Files: `internal/security/panel.go`, test.
- Done when: table tests green; outdated check uses the cache only.

### Step 8: integrity family

- What: walker (WalkDir, never follows symlinks; files opened `O_RDONLY|O_NOFOLLOW` and
  `fstat`-checked regular before hashing), excludes, caps (D10); baseline job; scan diff;
  fixes `chmod o-w`/`chown 1500:1500` by fd (`Fchmod`/`Fchown` after `O_NOFOLLOW` open) on a path
  re-checked inside the site root; ClamAV on flagged files via `clamav.Scan` per file, skipped
  with a run note when not `Ready` or `ErrScanBusy`; `integrity.changed` events; site-delete hook.
- Files: `internal/security/integrity.go`, `integrity_walk.go`, tests (`*_unix_test.go` for modes
  and owners, per the Windows `os.Chmod` trap).
- Tests: new/changed PHP, PHP in uploads, symlink to `/etc/shadow` is not read, cap hit, exclude
  honoured, fix refuses a path outside the root and a swapped symlink.
- Done when: a 10k-file fixture tree baselines and diffs correctly in WSL.

### Step 9: settings, cron built-ins, notify wiring

- What: §4.5 keys in `keys.go`; built-ins `security.scan` / `security.integrity` registered in
  `cronBuiltins` and added to `defaultJobs`; pass the sibling's `notify.Emitter` into the engine
  (needs `internal/notify` merged; until then the engine's nil default is `notify.Nop`).
- Files: `internal/settings/keys.go`, `internal/cron/builtin.go`, `cmd/panel/tls.go` (or a new
  `cmd/panel/security.go`), `serve.go`.
- Tests: settings validation ranges; `EnsureDefaults` seeds both jobs once; built-in output lines.
- Done when: a fresh store shows both jobs on the Cron page API.

### Step 10: Security page (UI)

- What: `web/src/routes/_app/security.tsx`, `pages/SecurityPage.tsx`, nav entry in
  `components/layout/nav.ts`; score + family cards, findings table (severity, target, evidence
  drawer, history), Accept dialog (reason required, optional expiry), Fix buttons that start a
  job and follow it, SSH confirm banner with countdown and "open a new SSH session first",
  Updates panel (package list, apply all / security only, unattended toggle), Integrity panel
  (per-site baseline, re-baseline). Types and queries in `lib/api.ts`, `lib/queries.ts`.
- Tests: vitest for the page states (no scan yet, family error, accepted row, guard 422 message,
  SSH countdown and expiry, job progress).
- Done when: vitest green and `web/dist` rebuilt (e2e drives the committed dist).

### Step 11: smoke script

- `scripts/smoke/14-security.sh` implementing §6 with `lib.sh`. Done when it passes on the sandbox.

## 6. Host gate (Ubuntu sandbox)

1. Fresh scan: score shown; `updates.*` counts equal step 0's `apt-check` / `apt-get -s` numbers.
2. Apply security-only: job streams output, panel stays up, `updates.security` resolves on rescan,
   `reboot_required` appears if a kernel was pulled; `security.finding` resolved event emitted.
3. Unattended toggle on → `apt-config dump` shows periodic upgrade "1" and security-only origins;
   off → back; `unattended-upgrade --dry-run` lists only `-security` packages.
4. **sshd lockout safety**, from a second SSH session kept open throughout:
   - a. No `authorized_keys` for any admitted account → Fix password auth returns 422; config unchanged.
   - b. Add a key, Fix → `sshd -T` shows `passwordauthentication no`; existing session alive;
     **do not confirm** → after 120 s the drop-in is restored and password login works again.
   - c. Fix again, `systemctl restart panel` before confirming → on start the change is reverted.
   - d. Fix, open a new key-based session, confirm → persists across `systemctl reload ssh` and reboot.
   - e. Corrupt the drop-in body (test hook) → `sshd -t` fails, nothing reloaded, original file intact.
   - f. `PermitRootLogin no` with only a root key → 422.
5. Exposure: `python3 -m http.server 8099` on 0.0.0.0 with the firewall off → high finding naming
   python3; firewall on → info; bind 127.0.0.1 → resolved. A `docker run -p 8088:80` → `docker_published`.
6. Integrity: baseline a PHP site; drop `wp-content/uploads/x.php` and edit `index.php` → three
   findings; EICAR in a new PHP file → `integrity.malware` when ClamAV is ready; `chmod 666` a
   file → Fix → mode 664; symlink `evil.php → /etc/shadow` → not hashed, no read in `strace`.
7. Accept risk with reason and 1-minute expiry → excluded from score, re-opens after expiry.
8. Cron: set `security-scan` to every minute → a run appears with trigger `cron`; restore.
9. Reboot: findings, accepts and baseline survive; no SSH pending row left.

## 7. Risks

- **SSH lockout.** Mitigated by D4/D5, the revert timer, revert-on-restart and the panel being a
  separate access path. Residual: a host whose panel port is also unreachable; documented.
- **cloud-init / vendor drop-ins** that sort before `00-` would silently win; the post-reload
  `sshd -T` verify catches it and reverts with a message naming the file that won.
- **`ssh.socket` (Ubuntu 22.10+)**: reload semantics differ from `ssh.service`; step 0 decides.
- **apt under the panel**: dpkg lock contention with unattended-upgrades (lock timeout + clear
  409 message); needrestart restarting `panel.service` mid-job (transient unit, `NEEDRESTART_MODE=l`);
  conffile prompts (confold); a Docker or nginx package upgrade restarting services the panel runs.
- **Security-count drift**: ESM/Pro origins (`-apps-security`, `-infra-security`) count as security
  only when attached; phased updates are not "pending". Fixture-pinned.
- **Integrity cost**: large media trees; caps + excludes + size+mtime for big files. Priority
  cannot be lowered per goroutine; if step 0 shows I/O impact, the walker moves into a
  `systemd-run --nice=10 -p IOSchedulingClass=idle` child (assumption, not designed here).
- **False positives** from deploys (Composer, WP auto-update) → "accept into baseline" per site;
  `integrity.changed` events would otherwise be noisy, so they are emitted once per site per scan.
- **TOCTOU in web roots writable by `www`**: all reads and fixes are fd-based with `O_NOFOLLOW`
  (the M2 descriptor-based write lesson).
- **Emitter not merged yet**: step 9 depends on the notifications plan; engine works with `Nop`.

## 8. Out of scope, and what changes on EL (M9)

Out of scope: brute-force banning (auto-ban plan); 2FA itself (D12); CVE lookups per package;
kernel live-patching; rootkit scanners (rkhunter/aide); auto-fix of exposure (owned by the
Firewall page); scanning app directories or the panel tree; automatic reboots; Debian.

EL changes, all inside `osdep` and `sshfix` unless noted:

- Updates: `dnf -q check-update --security` / `dnf updateinfo list --security`; reboot via
  `needs-restarting -r` (dnf-utils); unattended = `dnf-automatic` with `upgrade_type = security`
  and `dnf-automatic-install.timer`.
- sshd unit `sshd.service`; EL9 has `sshd_config.d/50-redhat.conf` (first-wins applies the same);
  crypto-policies may also set ciphers (not touched).
- SELinux: a port change needs `semanage port -a -t ssh_port_t` (still no auto-fix); `chown`/
  `chmod` keep file contexts, but a new baseline should record the context too
  (`security_integrity_files` gains a column then — not in 0026).
- Firewall: unchanged (panel iptables chains; firewalld masked by `MaskFirewallServices`).
