# Automatic IP bans (fail2ban-style): plan

Status: **plan, not started; decisions unratified.** Migration number reserved: `0025`. Depends on
the `notify.Emitter` interface from `docs/design/notifications-plan.md` (step 6 only).

Written after reading the firewall (`internal/firewall/model.go`, `render.go`, `apply.go`,
`service.go`, `testdata/v4.golden`), the boot restore (`cmd/panel/firewall.go`,
`internal/install/unit.go`), the monitors (`internal/sshauth/*`, `internal/wafmon/collector.go`,
`parse.go`, `abuseipdb.go`), the login throttle (`internal/auth/throttle.go`, `service.go`), the
routes (`internal/httpapi/handlers_firewall.go`, `handlers_sshauth.go`, `panel_ip.go`,
`session.go`, `auditmw.go`), the settings registry (`internal/settings/keys.go`) and the three
pages (`web/src/pages/FirewallPage.tsx`, `SSHPage.tsx`, `WafPage.tsx`). Facts about ipset on
Ubuntu 26.04 are prior knowledge: **step 0 confirms or corrects them before any code is written.**

Scope: Ubuntu first. EL (firewalld masked, iptables-nft, ipset packaging) stays in **M9**; §8 says
what changes there.

## 1. Goal

The panel bans abusive addresses on its own, from what its monitors already see, with no fail2ban:

- **Jails**: `sshd` (failed SSH logins), `waf` (ModSecurity/open-appsec *blocked* events per IP),
  `panel_login` (failed panel logins), `http_4xx` (4xx flood across all sites), `scanner`
  (requests for known probe paths), and an optional `abuseipdb` score check.
- Each jail: **N events in window W → ban for D**, repeat offenders get longer bans
  (D × factor^(n-1), capped), optionally permanent after K bans.
- Bans are enforced by the host firewall at once, survive daemon restart and reboot, and expire on
  their own. The operator sees each ban with its reason and evidence and can unban, extend, or make
  it permanent. An optional AbuseIPDB report goes out per ban.

What does **not** change: the rendered ruleset, the 60 s confirm/revert flow and `fw_state` for
operator-edited rules, `fw_blocklist`/`fw_allowlist` and their routes, the rule that the panel
never flushes chains it does not own (`render.go` emits `-F` only for `PANEL-INPUT`, `PANEL-BLOCK`,
`PANEL-FORWARD`), per-site nginx IP rules (`internal/waf` `IPRules`), and the in-memory login
throttle (`internal/auth/throttle.go`), which keeps working alongside the `panel_login` jail.

## 2. Current state

- **Chains.** The brief says `PBSERVER-INPUT`; the code says `PANEL-INPUT`, `PANEL-BLOCK`,
  `PANEL-FORWARD` (`render.go:12-18`). `PANEL-INPUT` order: `lo` accept → allowlist ACCEPT →
  `-j PANEL-BLOCK` → ESTABLISHED accept → ICMP → built-in ports → web ports → user rules → DROP.
  So a block drops **established** flows too, and the allowlist wins over any block.
  `PANEL-FORWARD` (from `DOCKER-USER`) jumps to `PANEL-BLOCK` **before** its allowlist RETURN
  (`renderForward`), so for published container ports a block beats the allowlist (§7 R6).
- **Blocklist.** `fw_blocklist(id, addr UNIQUE, comment)` (`0010_firewall.sql`), `BlockEntry` has
  only ID/Addr/Comment. Each entry is one `-A PANEL-BLOCK -s X -j DROP` line. `AddBlock` only
  stores; it goes live on the next `Apply`, which needs the 60 s confirm (`apply.go`, `Apply` →
  `Confirm`; `State.Dirty` compares the render to `fw_state.confirmed_v4/v6`).
- **Apply/restore.** `Manager.Apply` runs `iptables-restore --test --noflush`, saves `fw_pending`,
  applies, arms the timer; `Recover` rolls back at start. `RestoreConfirmed` replays
  `confirmed_v4/v6` from `panel-firewall.service` (`Before=network-pre.target`, runs
  `panel firewall restore` with only the DB). `disableAll` unhooks and flushes the owned chains.
- **ipset is not used anywhere** in product code (grep: only `open-appsec-plan.md:768`, where the
  step-0 spike ran `iptables -I OUTPUT -m set --match-set cpsink dst -j REJECT` on `.106`, so the
  tool and `xt_set` worked there once). No `nft` use. `osdep.Ubuntu.InstallPackages` exists.
- **sshd monitor** (`internal/sshauth`) is **stateless**: each query runs
  `journalctl -o json --since --until -n 20000 -u sshd.service -u ssh.service -u sshd-session.service`
  (`journal.go:58`). No cursor, no follower. One failed connection can yield two records
  (`Failed password …` and `Connection closed by authenticating user …`, both `OutcomeFailed`).
- **WAF monitor** (`internal/wafmon/collector.go`) polls every 1 s, tails `<domain>.access.log`
  and `<domain>.waf-audit.log` plus open-appsec logs into rings (`RingSize` 2000) and 10 s buckets.
  `Request{IP, Path, Status, Site, Time}` and `Event{IP, Action blocked|detected, Engine, RuleID}`
  exist. There is **no subscriber hook**; on restart each tail backfills the last 1 MiB (access) /
  4 MiB (audit), so old events are re-read.
- **AbuseIPDB**: both packages only **write a bulk-report CSV** (`sshauth.WriteAbuseIPDB`,
  `wafmon.WriteAbuseIPDB`, served by `GET /api/ssh/attempts/abuseipdb`). There is **no API client,
  no score lookup and no key setting**. `reportable()` already filters private/loopback/link-local.
- **Panel login**: `auth.Service.VerifyLogin` (`service.go:202`) counts failures in an in-memory
  `Throttle` per TCP peer (`clientIP`, `session.go:126`, never X-Forwarded-For); lockout 5 / 15 m
  (`login.max_attempts`, `login.lockout`). No hook out of it.
- **nginx client IP**: no `set_real_ip_from`/`real_ip_header` anywhere in `internal/`, so a site
  behind a CDN logs the CDN's address as `$remote_addr` (§7 R3).
- **"panel_ip"**: `tlspkg.PublicIPv4()` (`panel_ip.go`) finds the host's public IPv4 on its
  interfaces; `panel.domain` may itself be an IP. Sessions store their creation IP
  (`sessions.ip`, `0004_auth.sql`).
- **Audit**: `withAudit` (`auditmw.go`) records every unsafe-method request automatically.
- **Notifications**: `internal/notify` does not exist yet (sibling plan).

## 3. Decisions (unratified recommendations)

| # | Decision | Recommendation |
|---|---|---|
| D1 | Where bans are enforced | **ipset**: two sets `panel-ban4` (`hash:net family inet`) and `panel-ban6` (`family inet6`), `timeout 0 counters maxelem 262144`, matched by **one static line** at the top of `PANEL-BLOCK`: `-A PANEL-BLOCK -m set --match-set panel-ban4 src -j DROP`. One hash lookup per packet instead of a linear walk of thousands of `-s` rules; per-entry `timeout` means the kernel expires bans with no daemon; `ipset add -exist` / `del` change one entry without touching iptables. Native nft sets are ruled out on Ubuntu: our chains are iptables (nft-compat) rules and cannot reference a native nft set |
| D2 | Where bans are stored | **A separate `fw_bans` table**, not a column on `fw_blocklist`. A ban has history (several rows per address, needed for escalation), evidence, jail, expiry and lift reason; `fw_blocklist.addr` is UNIQUE and every row there becomes an iptables line that needs a confirmed apply. The DB is the truth; the kernel set is a cache rebuilt from it |
| D3 | Confirm/revert flow | **Bans skip it; enabling auto-ban does not.** Adding the one set-match line changes the rendered ruleset, so the first enable goes through the normal `Apply` + 60 s confirm. After that a ban is set membership only: the render is unchanged, `State.Dirty` stays false, nothing to confirm. Safe because a ban can only add a DROP for one address — it never removes an ACCEPT — and the never-ban list (D5) covers every address the operator reaches the host from. Recovery stays `pbp firewall disable`, plus a new `pbp ban flush` |
| D4 | Firewall disabled | Jails keep running and `fw_bans` keeps filling, the sets are populated, but nothing references them; the UI says **"Not enforced: the firewall is off"**. `disableAll` destroys both sets after flushing the chains (host left clean); the next apply recreates and repopulates them |
| D5 | Never ban | Always: loopback, the host's own addresses (every interface address + `PublicIPv4()` + `panel.domain` if an IP), every `fw_allowlist` entry, every live session's `sessions.ip`, every IP that made an authenticated panel request in the last 24 h (in-memory, fed by `requireAuth`), every IP with an `Accepted` SSH login in the last `autoban.trust_recent_logins` (default 7 d), `autoban.ignore` CIDRs. By default also private, CGNAT (100.64/10), link-local and ULA ranges (`autoban.ban_private` = false). Applies to manual bans too: the API refuses with a field-named 422 |
| D6 | IPv6 granularity | Ban the **/64** (`autoban.ipv6_prefix` = 64): one host owns a whole /64 and rotates within it. Never-ban checks use prefix overlap, not equality |
| D7 | Safety cap | **30 new bans per rolling minute** (`autoban.max_per_minute`). Over the cap, further bans in that minute are **dropped, not queued**, a `ban.cap_hit` warning goes out once, and an audit row is written. Hard ceiling: refuse new auto-bans at 90% of `maxelem` |
| D8 | Escalation | Per jail: `ban = base × factor^(n-1)` capped at `max_ban`, where n counts this address's bans (any jail) in `autoban.history` (90 d). `permanent_after` K (0 = never). Defaults: sshd 5 in 10 m → 1 h, ×4, max 7 d; others in §4.4 |
| D9 | Permanent bans | Stay in `fw_bans` with `expires_at NULL` and `timeout 0` in the set. Not copied into `fw_blocklist` (that would need a confirmed apply) |
| D10 | Event intake | Push, not poll-the-rings: wafmon gets `OnRequest`/`OnEvent` observer funcs called after its lock is released; sshauth gets a cursor-based follower (`journalctl --after-cursor`, every 5 s); httpapi's login handler calls the engine. One buffered channel into the engine (8192), drops counted and shown |
| D11 | Audit volume | Automatic bans are **not** written to `audit_log` (`fw_bans` is their record; a scan would bury the trail). Operator actions are audited by `withAudit` as today; cap hits are audited |
| D12 | AbuseIPDB | **Off by default, opt-in with a key** (the PRD says no telemetry). New minimal client `internal/abuseipdb` (`GET /api/v2/check`, `POST /api/v2/report`). Check: only public addresses, only on an address's first event per day, result cached 24 h in `fw_abuse_cache`, daily budget (default 900, free tier is 1000). Report: once per ban, never for manual bans unless the operator presses Report |
| D13 | ipset missing | Enabling auto-ban runs `osdep.InstallPackages(["ipset"])` as a job if `ipset` is not on PATH. `RestoreConfirmed` never lets a missing set block the whole restore: if creating the sets fails it strips the set-match line and restores the rest (logged) |
| D14 | Web jails default | `waf` on; `http_4xx` and `scanner` **off** until the operator turns them on: without real-IP handling a site behind a CDN would ban the CDN (§7 R3) |

## 4. Design

### 4.1 Data model (`internal/store/migrations/0025_autoban.sql`)

```sql
CREATE TABLE fw_bans (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    addr       TEXT    NOT NULL,              -- canonical prefix: /32, /128 or /64
    family     INTEGER NOT NULL CHECK (family IN (4, 6)),
    jail       TEXT    NOT NULL,              -- sshd|waf|panel_login|http_4xx|scanner|abuseipdb|manual
    reason     TEXT    NOT NULL,              -- "7 failed SSH logins in 10m"
    evidence   TEXT    NOT NULL DEFAULT '[]', -- JSON, at most 20 samples {at, detail}
    events     INTEGER NOT NULL,
    offense    INTEGER NOT NULL DEFAULT 1,    -- n in the escalation formula
    created_at TEXT    NOT NULL,
    expires_at TEXT,                          -- NULL = permanent
    lifted_at  TEXT,                          -- NULL = active
    lifted_by  TEXT    NOT NULL DEFAULT '',   -- expired|operator|allowlist|disabled
    reported_at TEXT,
    abuse_score INTEGER
);
CREATE UNIQUE INDEX fw_bans_one_active ON fw_bans (addr) WHERE lifted_at IS NULL;
CREATE INDEX fw_bans_addr_created ON fw_bans (addr, created_at);
CREATE INDEX fw_bans_expires ON fw_bans (expires_at) WHERE lifted_at IS NULL;

CREATE TABLE fw_jails (
    name            TEXT PRIMARY KEY,
    enabled         INTEGER NOT NULL,
    threshold       INTEGER NOT NULL CHECK (threshold BETWEEN 1 AND 10000),
    window_s        INTEGER NOT NULL CHECK (window_s BETWEEN 10 AND 86400),
    ban_s           INTEGER NOT NULL CHECK (ban_s BETWEEN 60 AND 31536000),
    factor          REAL    NOT NULL CHECK (factor BETWEEN 1 AND 10),
    max_ban_s       INTEGER NOT NULL,
    permanent_after INTEGER NOT NULL DEFAULT 0,
    options         TEXT    NOT NULL DEFAULT '{}' -- jail-specific JSON (scanner paths, 4xx codes)
);
-- seeded with the §4.4 defaults

CREATE TABLE fw_abuse_cache (addr TEXT PRIMARY KEY, score INTEGER NOT NULL, checked_at TEXT NOT NULL);
```

Times are RFC3339Nano text, as in `fw_pending`. Lifted rows older than `autoban.history` are
deleted by the sweeper. Evidence strings are network-sourced (usernames, paths): clipped to 200
bytes each, JSON only, never argv.

### 4.2 Firewall rendering and the kernel sets

- `RenderInput` gains `BanSets bool`. When true, `renderFamily` emits, right after
  `-F PANEL-BLOCK` and before the blocklist lines:
  `-A PANEL-BLOCK -m set --match-set panel-ban4 src -j DROP` (v6: `panel-ban6`). False renders
  exactly today's text, so `testdata/v4.golden`/`v6.golden` do not change; new
  `v4-bans.golden`/`v6-bans.golden`.
- `Service.input` sets `BanSets` from `autoban.enabled`. Turning the setting on or off marks the
  ruleset dirty; the UI's existing Apply bar does the rest (D3).
- New `internal/firewall/banset.go` (same `Cmd`, argv only):
  `EnsureSets(ctx, cmd, ipv6)` = `ipset create panel-ban4 hash:net family inet timeout 0 counters maxelem 262144 -exist`;
  `SyncSets(ctx, cmd, want []SetEntry)` = `ipset save panel-ban4` → diff → one `ipset restore -exist`
  batch of `add X timeout N` / `del X`; `AddBan`/`DelBan` single-entry calls; `DestroySets` after a flush.
- `Manager.Apply` calls `EnsureSets` before `--test` when `in.BanSets` (the test fails on a
  missing set). Rollback snapshots already carry the line via `iptables-save`; sets are never
  destroyed while referenced. `disableAll` calls `DestroySets` after the flush (D4).
- `RestoreConfirmed`: if the confirmed text contains `--match-set panel-ban`, run `EnsureSets`,
  then load active bans from `fw_bans` with their **remaining** time (expired rows skipped) in one
  `ipset restore`, **then** replay iptables. On failure: strip the line, restore the rest (D13).
  This runs at `network-pre`, so bans hold before any service listens.

### 4.3 Engine (`internal/autoban`)

- `Observation{Jail, Addr netip.Addr, At time.Time, Detail string}`. `Engine.Observe` is
  non-blocking (buffered channel, drop counter).
- Per jail an in-memory sliding window per address (timestamps ring, at most `threshold` kept),
  map bounded at 100 000 addresses (oldest dropped). Lost on restart by design; wafmon's backfill
  rebuilds the web jails, the sshd follower starts at `now - window`.
- Events at or before an address's last `lifted_at` by `operator` are ignored, so a backfill
  cannot re-ban an address the operator just unbanned.
- On threshold: never-ban check (D5) → cap check (D7) → offense count → duration (D8) → insert
  `fw_bans` → `firewall.AddBan` (`timeout` = remaining seconds) → optional AbuseIPDB report queue
  → digest accumulator. If the insert hits the active unique index, extend instead.
- Sweeper every 60 s: mark expired rows `lifted_by='expired'`; `SyncSets` against the DB (catches
  manual `ipset` edits and a set lost to `ipset flush`); prune history.
- sshd: count distinct connections (IP + source port), so the two records of one failure count
  once. Only `OutcomeFailed`; an `Accepted` record feeds the D5 trust list.
- `waf`: only `Action == blocked`. `http_4xx`: statuses in the jail's list (default 400, 401, 403,
  404, 405, 444), excluding the ACME path. `scanner`: path prefix list (default `/.env`, `/.git/`,
  `/.aws/`, `/phpmyadmin`, `/cgi-bin/`, `/vendor/phpunit/`, `/boaform`, `/HNAP1`), threshold 3.
- `abuseipdb`: not a log source. On an address's first event of the day in any jail, a lookup is
  queued; score ≥ `abuseipdb.check_threshold` bans at once with jail `abuseipdb`.

### 4.4 Jail defaults

| Jail | On | N / W | Ban | Factor / max | Permanent after |
|---|---|---|---|---|---|
| sshd | yes | 5 / 10 m | 1 h | ×4 / 7 d | 0 |
| panel_login | yes | 10 / 15 m | 1 h | ×4 / 7 d | 0 |
| waf | yes | 5 / 10 m | 1 h | ×4 / 7 d | 0 |
| http_4xx | no | 200 / 1 m | 15 m | ×2 / 1 d | 0 |
| scanner | no | 3 / 10 m | 1 d | ×4 / 30 d | 0 |
| abuseipdb | no | score ≥ 90 | 1 d | ×2 / 30 d | 0 |

`panel_login` sits above the throttle's 5 so the throttle answers first and a typo streak by the
operator never reaches the firewall (and the operator's IP is already trusted by D5).

### 4.5 API routes (auth required, 503 when firewall or engine is nil, like `firewallRoutes`)

| Route | What |
|---|---|
| `GET /api/firewall/bans?state=active\|history&jail=&q=&before=&limit=` | Page of bans, with country (`geoip.Store.Country`) and set hit counters |
| `GET /api/firewall/bans/summary` | Active per jail, bans in last 24 h, cap state, dropped observations, enforcement (`enforced`, `reason`) |
| `POST /api/firewall/bans` | Manual ban `{addr, duration\|permanent, reason}`; 422 `field: addr` for never-ban addresses |
| `DELETE /api/firewall/bans/{id}` | Unban (lifted_by `operator`) |
| `POST /api/firewall/bans/{id}/extend` | `{duration}` added to the remaining time |
| `POST /api/firewall/bans/{id}/permanent` | `expires_at = NULL`, set timeout 0 |
| `POST /api/firewall/bans/{id}/report` | Report to AbuseIPDB now (409 when not configured) |
| `GET /api/firewall/jails`, `PUT /api/firewall/jails/{name}` | Jail config; range errors are field-named 400s |

`AddAllow` also lifts any active ban that overlaps the new entry (`lifted_by='allowlist'`).
`docs/API.md` gets every route.

### 4.6 Settings keys (group `Firewall`, `internal/settings/keys.go`)

| Key | Type | Default | Notes |
|---|---|---|---|
| `autoban.enabled` | bool | false | Renders the set line; needs one confirmed apply |
| `autoban.max_per_minute` | int | 30 | 1..1000 |
| `autoban.ignore` | string | "" | CIDR list, validated with `firewall.ParsePrefix` |
| `autoban.ban_private` | bool | false | |
| `autoban.trust_recent_logins` | duration | 168h | 0 turns it off |
| `autoban.ipv6_prefix` | int | 64 | 48..128 |
| `autoban.history` | duration | 2160h | Escalation lookback and row retention |
| `abuseipdb.api_key` | string, secret, optional | "" | |
| `abuseipdb.check_threshold` | int | 0 | 0 off, 1..100 |
| `abuseipdb.daily_budget` | int | 900 | |
| `abuseipdb.report` | bool | false | |

### 4.7 Notifications (step 6, `notify.Emitter` only)

- `ban.added`, severity info, **digested**: the engine keeps a per-jail accumulator and emits at
  most one event per jail per 15 min (first ban starts the window, emit at its end):
  `Key "ban.added:<jail>"`, Subject `"12 addresses banned by sshd"`, Body the top 10 with reason,
  `Link "/firewall?tab=bans&jail=sshd"`, `Source "autoban"`. Manual bans emit nothing.
- `ban.cap_hit`, severity warning, `Key "ban.cap_hit"`, once per cap episode; a `Resolved: true`
  event with the same key after a full minute under the cap.
- Wiring takes `notify.Emitter`; nil becomes the package's no-op.

### 4.8 UI

- `FirewallPage.tsx`: a fourth tab **Bans** (next to Rules / Blocklist / Allow list): summary strip,
  enforcement banner (firewall off / apply needed / ipset missing), table (address, country, jail,
  reason, events, offense, expires or "Permanent", hits), row menu Unban / Extend / Make
  permanent / Report, an evidence sheet, Active/History toggle, jail filter, manual Ban dialog.
- A **Jails** sub-section on the same tab: one card per jail with on/off, N, W, ban, factor, max,
  permanent-after, and the jail options (scanner paths, 4xx codes); the AbuseIPDB fields link to
  Settings.
- `SSHPage.tsx` top-IP list and `WafPage.tsx` top tables: a **Ban** action and a "banned" badge.
- Types and queries in `web/src/lib/api.ts` / `queries.ts`; `alerts.ts` adds "auto-ban enabled
  but not enforced".

## 5. Steps

Each step ends green on the gate (`go test ./cmd/... ./internal/...` in WSL, CI golangci-lint on
changed code, vitest for UI steps) and is committed on its own.

### Step 0: host spike on the Ubuntu sandbox (no product code)

Record answers in this file: is `ipset` installed on a fresh Ubuntu 26.04 (`dpkg -s ipset`) and is
`iptables` the nft backend (`iptables -V`); does `-m set --match-set` work through iptables-nft with
`timeout`, `counters`, `-exist` re-add (does it reset the timeout?) and `timeout 0`; the exact
`iptables-restore --test` error for a missing set; time and RSS to `ipset restore` 50 000 entries
and the per-packet cost (`ab` against nginx with 0 vs 50 000 entries); a banned address is dropped
on an established SSH session and on a published Docker port (`PANEL-FORWARD`); sets are gone
after reboot and `ipset` works from `panel-firewall.service` at `network-pre`
(module autoload, `/usr/sbin/ipset`); `journalctl --after-cursor` / `__CURSOR` on the sshd units.
Done when each item has a recorded answer and D1/D13 are confirmed or changed.

### Step 1: data model and the set primitives

- `0025_autoban.sql` (§4.1); `internal/firewall/banset.go` (§4.2) with a fake `Cmd`.
- Tests: argv for create/add/del/restore, `SyncSets` diff (add, remove, timeout refresh, v6
  /64), migration applies on a store with data. Done when the gate is green.

### Step 2: rendering, apply, restore, disable

- `RenderInput.BanSets`, the line, new goldens; `Manager.Apply` ensures sets; `RestoreConfirmed`
  loads remaining bans then replays, strips the line on set failure; `disableAll` destroys sets.
- Tests: goldens unchanged with `BanSets=false`; restore order (ipset before iptables-restore) and
  remaining-time arithmetic with a fake clock; restore with ipset failing still restores the rest;
  rollback snapshot keeps the line. Done when the gate is green.

### Step 3: the engine

- `internal/autoban`: `Engine`, windows, never-ban (`NeverBan` with injected sources), cap,
  escalation, sweeper, `pbp ban flush` / `pbp ban list` / `pbp unban <addr>` in `cmd/panel`.
- Tests: table tests for threshold/window edges, distinct-connection counting, escalation and
  cap, permanent-after, /64 folding, every D5 category (incl. prefix overlap), post-unban
  backfill ignored, unique-index extend, sweeper marks and syncs. Done when the gate is green.

### Step 4: event sources

- `wafmon.Options.OnRequest/OnEvent` (called after `c.mu` is released, from a batch);
  `sshauth.Follower` (cursor, 5 s, `Accepted` feed); login hook in `handlers_auth.go` on
  `ErrInvalidCredentials` and `ErrLockedOut` (not in `handlers_password.go`); `requireAuth` feeds
  the trusted-IP set. Wiring in `cmd/panel/serve.go` / `m7.go`.
- Tests: observer receives parsed lines once, not under the lock (race test); follower resumes
  from its cursor and dedupes; login handler observes failures only. Done when the gate is green.

### Step 5: API and settings

- `handlers_bans.go` (§4.5), settings keys (§4.6), `AddAllow` lifts overlapping bans,
  `State` gains `bans {enforced, reason}`; ipset install job when missing (D13); `docs/API.md`.
- Tests: routes with a fake engine, 422 for own IP / session IP / allowlisted, 503 wiring,
  settings validation. Done when the gate is green.

### Step 6: notifications and AbuseIPDB

- Digest and cap events through `notify.Emitter` (needs the sibling's `internal/notify` step 1);
  `internal/abuseipdb` client with an `httptest` server, budget, cache, report-once.
- Tests: digest groups bans per jail per window, cap fires once and resolves; client never sends
  a private address, respects the budget, caches. Done when the gate is green.

### Step 7: UI

- Bans tab and jail cards (§4.8), Ban actions on SSH and WAF pages, alerts.
- vitest: table states (empty, firewall off, apply needed), row actions call the right routes,
  jail form validation, 422 shown on the field. Rebuild `web/dist` before Playwright. Done when
  vitest and the gate are green.

## 6. Host gate (Ubuntu sandbox, `scripts/smoke/13-autoban.sh`)

The sandbox hosts are on a private LAN (`10.10.11.0/24`), which D5 exempts by default, so items
2-8 run with `autoban.ban_private = true`; item 9 checks both values.

1. Enable firewall + auto-ban, confirm the apply; `iptables -S PANEL-BLOCK` shows the set line;
   `State.dirty` false.
2. From a second host (not the operator's): 5 bad SSH passwords → banned within 10 s; `ipset test`
   hits; new and **established** connections from it time out; row shows evidence.
3. WAF: 5 requests tripping CRS from the second host → `waf` ban; Docker published port also
   unreachable from it.
4. Panel login: 10 bad passwords → throttle 429 first, then `panel_login` ban.
5. Unban in the UI → reachable again at once; backfill after a daemon restart does **not**
   re-ban it.
6. Extend and Make permanent change `ipset list` timeouts (permanent = 0).
7. Restart the daemon: bans intact, nothing re-applied. **Reboot**: bans present before
   `panel.service` starts (`journalctl -u panel-firewall`), expired ones absent.
8. Cap: 100 distinct offenders in one minute (a second host with 100 secondary addresses, each
   failing SSH 5 times) → 30 bans, `ban.cap_hit` emitted once, audit row present.
9. **Lockout safety**: from the operator's own address, with a live panel session: 20 bad SSH
   passwords, 20 bad panel logins, 50 scanner paths, a manual ban of the own IP via the API →
   no ban, 422 on the manual one, panel and SSH stay reachable (with `ban_private` true, so only
   the session/trust rules protect it). Repeat from an allowlisted address, and from a private
   address with `ban_private` false. Then `pbp ban flush` from the console empties both sets.
10. Disable the firewall → sets destroyed, chains unhooked; re-enable + confirm → sets back with
    every active ban.

## 7. Risks

- **R1 Lockout of the operator** by a ban on their NAT or VPN egress. Mitigated by D5 (live
  sessions, recent authenticated requests, recent SSH logins), the 422 on manual bans, and
  `pbp ban flush` / `pbp firewall disable` on the console. An operator with a new IP and no
  session who fails SSH 5 times is still bannable; that is the feature.
- **R2 ipset missing at boot** must not leave the host unfiltered: D13 strips the line and
  restores the rest. Step 2 tests it; step 0 confirms the error mode.
- **R3 CDN/reverse proxy in front of a site**: nginx logs the proxy's IP (no `real_ip` config
  exists), so web jails would ban Cloudflare and drop every site behind it. Web 4xx/scanner jails
  default off (D14); the jail cards say why. Real-IP support is out of scope.
- **R4 Spoofed sources**: SSH and HTTP need a TCP handshake, so log-derived IPs are not spoofable;
  UDP is not a source. Low.
- **R5 Backfill and restart double counting**: covered by window bounds and the post-unban rule.
- **R6 `PANEL-FORWARD` order**: block before allowlist for container ports. Auto-ban never bans
  allowlisted addresses, so it adds no new case; the existing inconsistency is noted, not fixed.
- **R7 Throughput**: the `OnRequest` hook runs per access line; it must only filter by status/path
  and enqueue. Drops are counted and shown, never block wafmon.
- **R8 Set full**: 90% of `maxelem` stops new auto-bans with a warning rather than an `ipset`
  error mid-batch.
- **R9 AbuseIPDB is a third party**: off by default, key required, budgeted, public addresses only.
- **R10 `restoreText` does not declare `PANEL-FORWARD`** (`apply.go`), so a rollback snapshot
  holding forward lines may misapply. Pre-existing; step 2 adds a test that touches it and reports
  rather than silently fixing.

## 8. Out of scope

- EL (M9): firewalld is masked as on Ubuntu, iptables is the nft backend on EL9/EL10; `ipset` is a
  separate package (`dnf install ipset`) and its future on EL10 needs checking — if it is gone,
  the set moves to a native nft set and the panel chains would need to become native nft too.
  SELinux may need `ipset` allowed from `panel-firewall.service`.
- Real-IP (`set_real_ip_from`) handling for sites behind CDNs; CDN range allowlists.
- Country bans (geo rules), banning by ASN, sharing ban lists between servers.
- Per-site (nginx-level) bans: bans are host-wide.
- Reading `/var/log/auth.log` or any fail2ban config; importing fail2ban jails.
- Mail, FTP or database jails (no such services are managed).
