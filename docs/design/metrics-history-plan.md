# Metrics history: plan

Status: **plan, not started; decisions unratified.** Migration number reserved: **0024** (panel.db).
Sibling contract: `internal/notify` (owned by `notifications-plan.md`, written in parallel).

Written after reading the sampler and service grid (`internal/sysinfo/sampler.go`, `proc.go`,
`source.go`, `services.go`), the traffic monitor (`internal/wafmon/collector.go`, `parse.go`,
`sources.go`, `doc.go`), the nginx templates (`internal/nginx/render.go`), the store
(`internal/store/store.go`, `migrate.go`), the settings registry (`internal/settings/keys.go`,
`settings.go`), the TLS cert store (`internal/tls/store.go`, `migrations/0012_tls.sql`), the
dashboard routes (`internal/httpapi/handlers_sysinfo.go`), the daemon wiring (`cmd/panel/m7.go`,
`cmd/panel/serve.go`), and the UI (`web/src/pages/DashboardPage.tsx`, `SiteDetailPage.tsx`,
`web/src/components/Sparkline.tsx`, `TrafficChart.tsx`, `web/package.json`). Facts about cgroup
paths and `/proc/diskstats` naming come from prior knowledge; **step 0 confirms them on the
Ubuntu sandbox** before code depends on them.

Scope: Ubuntu first. EL (Rocky/RHEL) stays in **M9**.

## 1. Goal

The panel keeps what it measures, so an operator can answer "what happened last night":

- **Host history**: CPU, load, memory, swap, per-mount disk usage and IO, network throughput.
- **Service history**: state, CPU and RSS of panel-managed services (nginx, each PHP version's
  unit, apps, panel-owned containers, panel/supervisord/firewall/docker units).
- **Site history**: requests, status classes, bytes, p50/p95 request and upstream time per site,
  from the nginx access logs the panel already writes.
- **Charts** with a range picker (1h, 6h, 24h, 7d, 30d, 90d) on the dashboard and a new
  **Metrics** section in a site's menu.
- **Threshold alerts** evaluated against stored data with hysteresis, emitted as
  `notify.Event{Kind: "metrics.threshold"}`.

What does **not** change: the live dashboard (`GET /api/sysinfo`, the SSE stream and the in-memory
ring stay as they are and keep feeding the tiles), the WAF page's live hour (`wafmon` keeps its
10 s buckets and rings), the service controls, `panel.db`'s role and size class, and the fact
that the panel runs with nothing else installed (no Prometheus, no extra daemon).

## 2. Current state

| Area | Today | Path |
|---|---|---|
| Host sampling | `Sampler` every **5 s** (`DefaultInterval`), ring of **720** (1 h), lost at restart. The doc comment says it outright: "There is no time-series database" | `internal/sysinfo/sampler.go` |
| Snapshot | `CPUPercent`, `CPUCores`, `Load`, `Memory` (incl. swap), `UptimeSeconds`, `Disks[]` (usage only: total/used/free/%), `Net[]` (per-iface rates, `lo` skipped). **No disk IO** (no `/proc/diskstats` read) | `sampler.go`, `proc.go` |
| Seam | `Source{ReadProc(name), Disk(mount)}`; fixtures in `testdata/` | `source.go` |
| Services | `Services.List` = fixed systemd allowlist + `panel-php-<ver>` units + containers + `app:<name>`. One `systemctl show` exec per unit; Docker inspect per container. **State only, no CPU/RSS** | `services.go`, wiring `cmd/panel/m7.go:96-123` |
| App PIDs | supervisord reports `pid` per program | `internal/apps/supervisor.go:94`, `manager.go:97` |
| Site traffic | `wafmon.Collector` tails `<domain>.access.log` by offset, 10 s buckets for 1 h per site (`req, s2..s5, bytes, rtSum, rtMax, rtN`, top-48 keys), backfills 1 MiB of each log on start. **No percentiles, nothing on disk** | `internal/wafmon/collector.go`, wired `cmd/panel/serve.go:176` |
| Log format | `pbpanel` = combined + `rt=$request_time rid=$request_id`. **No upstream time.** Parser reads trailing `k=v` fields by prefix, so new fields are additive | `internal/nginx/render.go:134-137`, `internal/wafmon/parse.go` |
| State | SQLite (modernc, pure Go), WAL, `_txlock=immediate`, busy 5 s, pool 4; migrations embedded and forward-only, latest **0021** (0022/0023 taken by sibling plans) | `internal/store/store.go`, `migrate.go` |
| Certs | `certs.not_after` indexed | `internal/store/migrations/0012_tls.sql`, `internal/tls/store.go` |
| Settings | typed keys (`int`, `bool`, `duration`, `enum`...), groups, `Subscribe` for live changes | `internal/settings/keys.go`, `settings.go` |
| UI charts | **no chart library**; hand-rolled SVG `Sparkline` and `TrafficChart` (stacked status classes, legend, data table) | `web/src/components/` |
| Site menu | left rail of sections (`domains, proxy, config, ssl, waf, php, rewrite, logs, delete`) | `web/src/pages/SiteDetailPage.tsx:73-110` |
| Notify | does not exist yet | sibling plan |

## 3. Decisions (unratified recommendations)

| # | Decision | Recommendation |
|---|---|---|
| D1 | Storage engine | **SQLite, the driver already in the binary.** No new dependency, SQL rollups, `sqlite3` for debugging. Rejected: Prometheus TSDB as a library (large dep tree, 30-60 MB RSS), VictoriaMetrics (second daemon), `tstorage` (unmaintained, no rollups), RRD-style fixed files (zero growth but a custom format we own forever). Math in §4.3 |
| D2 | Where the samples live | **Separate `/www/panel/data/metrics.db`**, own schema ledger, WAL, `synchronous=NORMAL`. Keeps `panel.db` small and its WAL quiet, lets an operator back up config without churn, and makes "delete the file" a valid reset. **Alert rules and alert state live in `panel.db` (migration 0024)**: they are configuration and must survive a metrics reset |
| D3 | Tiers | **Host: 10 s raw for 24 h → 1 m for 7 d → 5 m for 90 d. Services: 30 s raw for 24 h → 1 m / 7 d → 5 m / 90 d. Sites: 1 m for 7 d → 15 m for 90 d** (sites have no sub-minute tier: the live hour is wafmon's job). Rollups keep avg and max (min only where useful: mem available, disk free) |
| D4 | Write batching | **One transaction per 60 s** holding all raw rows of the minute plus that minute's rollups; 5 m / 15 m rollups on their boundary; retention deletes hourly in chunks of 5 000 rows. Crash loses ≤ 60 s. No `VACUUM`; freed pages are reused |
| D5 | Raw host interval | **Persist 10 s = mean of two 5 s samples** (max kept for CPU). The sampler's interval is unchanged so the live tiles keep their feel |
| D6 | Per-service CPU/RSS source | **cgroup v2**: `system.slice/<unit>/cpu.stat` (`usage_usec`) and `memory.current` for units; `system.slice/docker-<id>.scope/` for containers; **apps by PID** from supervisord (`/proc/<pid>/stat` utime+stime, `/proc/<pid>/status` VmRSS, main process only — children not summed in v1). PHP: per **version unit**, not per pool (pool split needs `pm.status_path`; out of scope) |
| D7 | Upstream time | **Add `urt="$upstream_response_time"` to `pbpanel`** (quoted: the value can be `0.010, 0.004` across retries or `-` for static). Parser sums comma-separated parts, treats `-` as absent. Existing site files already reference `pbpanel` by name, so they pick it up on the next `panel-http.conf` write; old lines still parse |
| D8 | Percentiles | **Fixed histogram, 20 bins** (1 ms … 60 s, 1-2-5 spacing, bounds in §4.2) per site per minute for request time and upstream time. Mergeable across tiers by adding bins; p50/p95 interpolated in the bin. Error ≤ ~20 %, stated in the UI tooltip |
| D9 | Site metrics source | **A minute aggregator inside `wafmon`** fed by the same parsed `Request`s (no second tail). Restart double-count avoided by a persisted per-site watermark (last flushed minute); only **closed** minutes (end + 15 s grace) are flushed |
| D10 | Alert evaluation | **Every 60 s after the flush, against the 1 m tier** (raw for nothing). Fire when the condition holds for `for` (default 5 m); clear when the value is past the **clear threshold** for `clear_for` (default = `for`). State machine `ok → pending → firing → ok` persisted in `panel.db` so a restart neither re-fires nor forgets |
| D11 | Charts | **Extend the hand-rolled SVG charts**, no chart library (bundle size, and `TrafficChart` already sets the house style: legend + data table, colour never the only cue). One `TimeSeriesChart` with hover readout |
| D12 | Off switch | `metrics.enabled=false` stops persistence and alert evaluation, keeps the live dashboard, **does not delete** `metrics.db` (a "Clear history" button does) |

## 4. Design

### 4.1 Package layout

- `internal/metrics` (new): `DB` (opens `metrics.db`, own embedded migrations), `Recorder`
  (subscribes to `sysinfo.Sampler`, polls services every 30 s, drains wafmon minutes, flushes
  every 60 s), `Rollup`, `Retention`, `Query` (range → tier + step), `Alerts` (rules from
  panel.db, evaluator, `notify.Emitter`).
- `internal/store`: export `OpenFS(path string, migrations fs.FS) (*Store, error)` so
  `metrics.db` reuses `openDB`/`setWAL`/`migrate` with its own `migrations/` directory and ledger
  (no copy of the pragma/WAL code). `metrics.db` migrations are numbered from `0001` in
  `internal/metrics/migrations/`; panel.db keeps the global sequence (0024 here).
- `internal/sysinfo`: disk IO (`ParseDiskstats`, `DiskStat.ReadBytesPerSec/WriteBytesPerSec`),
  `Services.Usage(ctx)` (CPU % and RSS per service, from cgroup/proc via the `Source` seam).
- `internal/wafmon`: `MinuteAgg` + `Collector.DrainMinutes(closedBefore time.Time)`, `urt=` parse.

### 4.2 Data model (`metrics.db`, all timestamps unix seconds, aligned to the tier step)

```sql
-- dictionary, so series rows carry a small int, not a string
CREATE TABLE series_keys (id INTEGER PRIMARY KEY, kind TEXT NOT NULL,   -- mount|iface|service|site
                          name TEXT NOT NULL, UNIQUE(kind, name));
CREATE TABLE host_10s (ts INTEGER PRIMARY KEY, cpu REAL, cpu_max REAL, load1 REAL, load5 REAL,
  load15 REAL, mem_used INTEGER, mem_avail INTEGER, swap_used INTEGER, net_rx REAL, net_tx REAL);
CREATE TABLE host_1m  (... same columns as avg, plus cpu_max, mem_avail_min, load1_max ...);
CREATE TABLE host_5m  (... same as host_1m ...);
CREATE TABLE disk_10s (ts INTEGER, key INTEGER, used_pct REAL, used INTEGER, rd_bps REAL,
  wr_bps REAL, PRIMARY KEY (ts, key)) WITHOUT ROWID;              -- + disk_1m, disk_5m (avg/max)
CREATE TABLE svc_30s  (ts INTEGER, key INTEGER, state INTEGER, cpu REAL, rss INTEGER,
  PRIMARY KEY (ts, key)) WITHOUT ROWID;                           -- + svc_1m, svc_5m
  -- rollup: up_frac REAL (share of samples running), worst_state, cpu avg/max, rss avg/max
CREATE TABLE site_1m  (ts INTEGER, key INTEGER, req INTEGER, s2 INTEGER, s3 INTEGER, s4 INTEGER,
  s5 INTEGER, bytes INTEGER, rt_hist BLOB, urt_hist BLOB, PRIMARY KEY (ts, key)) WITHOUT ROWID;
CREATE TABLE site_15m (... same ...);
CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT);                   -- site watermarks, last rollup
```

- `(ts, key)` primary keys make every insert an append at the right edge of the B-tree; a query
  for one series scans the time range and filters `key` — at ≤ 2 000 points per response that is
  cheap. Revisit with a `(key, ts)` index only if step 6 measures a slow query.
- Network is stored as host totals (sum of non-`lo` ifaces) in v1; per-iface is a later column set.
- Histogram bins: upper bounds `1, 2, 5, 10, 20, 50, 100, 200, 300, 500, 750 ms, 1, 1.5, 2, 3, 5,
  10, 20, 60 s, +Inf` (20 bins, 1-2-5 style, denser where web latency lives). Stored as 20 uvarints.
- Service state as int: `running=1, starting=2, stopping=3, stopped=4, failed=5, missing=6, unknown=7`.

`panel.db` migration **0024_metrics_alerts.sql**:

```sql
CREATE TABLE metric_alert_rules (id INTEGER PRIMARY KEY, kind TEXT NOT NULL,
  -- disk_pct | mem_pct | swap_pct | load_per_core | cpu_pct | service_down | cert_days | site_5xx_pct
  target TEXT NOT NULL DEFAULT '*',          -- mount, service name, domain, or '*' (each one)
  threshold REAL NOT NULL, clear_threshold REAL NOT NULL,
  for_seconds INTEGER NOT NULL DEFAULT 300, clear_for_seconds INTEGER NOT NULL DEFAULT 300,
  severity TEXT NOT NULL CHECK (severity IN ('info','warning','critical')),
  enabled INTEGER NOT NULL DEFAULT 1, builtin INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE metric_alert_state (rule_id INTEGER NOT NULL REFERENCES metric_alert_rules(id)
  ON DELETE CASCADE, target TEXT NOT NULL, state TEXT NOT NULL CHECK (state IN
  ('ok','pending','firing','clearing')), since INTEGER NOT NULL, value REAL,
  PRIMARY KEY (rule_id, target));
```

Seeded (builtin, editable, not deletable — disable instead): disk 85 % warn / clear 80; disk
95 % critical / clear 90; mem 90 % / 80 for 10 m; swap 50 % / 30 for 10 m; load per core 2.0 /
1.5 for 15 m; service_down 5 m (`state ∉ {running}` for every 1 m bucket); cert_days 14 warn /
clear 21; cert_days 3 critical / clear 7. `site_5xx_pct` (5xx share ≥ 10 % with ≥ 50 req/min)
seeded **disabled**.

Event: `Kind="metrics.threshold"`, `Key="metrics.threshold:<rule_id>:<target>"`, `Severity` from
the rule, `Subject="Disk / at 91% (threshold 85%)"`, `Source="metrics"`, `Link="/"` (host) or
`/sites/<domain>?section=metrics`, `Resolved=true` on `clearing → ok`. Emitted on transitions
only; re-notification policy belongs to notify.

### 4.3 Storage and write math (12 services, 2 mounts, 20 active sites — a busy small VPS)

| Table | Rows/day | ~B/row (incl. B-tree) | Retention | Steady size |
|---|---|---|---|---|
| host_10s | 8 640 | 110 | 24 h | 1.0 MB |
| disk_10s | 17 280 | 45 | 24 h | 0.8 MB |
| svc_30s | 34 560 | 35 | 24 h | 1.2 MB |
| host_1m + disk_1m + svc_1m | 1 440 + 2 880 + 17 280 | 220 / 60 / 50 | 7 d | 2.2 + 1.2 + 6.0 MB |
| host_5m + disk_5m + svc_5m | 288 + 576 + 3 456 | same | 90 d | 5.7 + 3.1 + 15.6 MB |
| site_1m (traffic every minute) | 28 800 | 90 | 7 d | 18 MB |
| site_15m | 1 920 | 90 | 90 d | 15.6 MB |
| **Total** | | | | **≈ 70 MB worst case; ≈ 20 MB for 3 sites / 8 services** |

Write volume: one commit/min touches the right-edge leaf of ~10 tables plus interior pages ≈
15-25 pages × 4 KiB ≈ 60-100 KiB of WAL per commit → **≈ 85-145 MB/day WAL**, roughly doubled by
checkpoints into the main file → **≈ 0.2-0.3 GB/day of disk writes**, ~2 IOPS average. Committing
every 10 s would multiply that by ~6 (pages per commit barely shrink); that is why D4 batches.
Expected memory: < 1 MB of pending rows; `metrics.db` opened with `cache_size=-2048` (2 MiB),
1 writer + 2 reader connections → **target RSS increase ≤ 8 MB** (measured in §6).

### 4.4 API routes (all `requireAuth`, JSON, columnar to keep payloads small)

| Route | Returns |
|---|---|
| `GET /api/metrics/host?range=1h\|6h\|24h\|7d\|30d\|90d` (or `from`,`to` unix) | `{step, t:[], cpu:[], cpu_max:[], load1:[], mem_used:[], mem_avail:[], swap_used:[], net_rx:[], net_tx:[]}` |
| `GET /api/metrics/disks?range=` | `{step, t:[], mounts:[{mount, used_pct:[], rd_bps:[], wr_bps:[]}]}` |
| `GET /api/metrics/services?range=[&name=]` | `{step, t:[], services:[{name, kind, up_frac:[], cpu:[], rss:[]}]}` |
| `GET /api/metrics/sites/{domain}?range=` | `{step, t:[], req:[], s2..s5:[], bytes:[], rt_p50:[], rt_p95:[], urt_p50:[], urt_p95:[]}`; `404` unknown domain, `ValidDomain` first |
| `GET /api/metrics/status` | file size, rows and oldest ts per tier, last flush/error, enabled |
| `POST /api/metrics/clear` | truncates `metrics.db` tables (audit-logged) |
| `GET /api/alerts` | rules joined with current state per target |
| `POST /api/alerts/rules`, `PUT/DELETE /api/alerts/rules/{id}` | rule CRUD; builtin rules: PUT only; 422 with field name on bad input |

Tier choice: finest tier whose retention covers `range`, then server-side `GROUP BY ts / step`
with `step = max(tier, ceil(range / 1500))` rounded up to a tier multiple — no response exceeds
1 500 points per series. Gaps (daemon down) come back as `null`, never interpolated.

### 4.5 Settings keys (new group **Metrics**, all live via `Registry.Subscribe`)

| Key | Type | Default | Bounds |
|---|---|---|---|
| `metrics.enabled` | bool | `true` | |
| `metrics.raw_retention` | duration | `24h` | 1h – 72h |
| `metrics.minute_retention` | duration | `168h` | 24h – 720h |
| `metrics.coarse_retention` | duration | `2160h` | 168h – 8760h |
| `metrics.sites_enabled` | bool | `true` | |
| `metrics.alerts_enabled` | bool | `true` | |

Retention is applied by the hourly sweep; shortening it deletes on the next sweep.

## 5. Steps

Each step ends green on the gate (`go test ./cmd/... ./internal/...` in WSL, CI golangci-lint on
changed code, vitest for UI steps) and is committed on its own. Go tests never run on Windows.

### Step 0: spike on the Ubuntu sandbox (no product code)

- Confirm cgroup v2 unified on Ubuntu 26.04; record the exact paths for `panel-nginx.service`,
  `panel-php-8.4.service`, `panel-supervisord.service`, and a Docker container (`docker-<id>.scope`
  under the systemd driver), and that `cpu.stat`/`memory.current` are readable by root.
- `/proc/diskstats` names vs `/proc/mounts` devices: `/dev/sda1`, `/dev/vda1`, `/dev/nvme0n1p1`,
  `/dev/mapper/*` (→ `dm-N` via `/sys/block/dm-*/dm/name`), LVM root.
- Add `urt=` to a scratch `log_format`, hit a PHP site, a proxy site with a dead upstream and a
  static file; record the value shapes (`-`, `0.004`, `0.010, 0.004`, `0.002 : 0.003`).
- Measure a 60 s batched commit on `metrics.db` with a synthetic minute of rows: WAL bytes per
  commit (`stat` before/after), to check §4.3's page estimate.

Done when every item has a recorded answer in this file and D6/D7/§4.3 are confirmed or corrected.

### Step 1: `metrics.db` and the store seam

- What: `store.OpenFS`; `internal/metrics/db.go` opening `<DataDir>/metrics.db` with its own
  migrations (`internal/metrics/migrations/0001_init.sql`, §4.2 tables); `series_keys` cache;
  `install.Layout.MetricsDBPath()`; a corrupt file (open or `quick_check` fails) is renamed to
  `metrics.db.corrupt-<ts>` and recreated, logged once.
- Files: `internal/store/store.go`, `internal/metrics/{db.go,keys.go,migrations/0001_init.sql}`,
  `internal/install/layout.go`.
- Tests: open/migrate twice idempotent; panel.db ledger untouched by metrics migrations;
  corrupt-file recovery; key dictionary round-trip.
- Done when the package opens, migrates and recovers in tests.

### Step 2: host and disk IO recording

- What: `ParseDiskstats` + device-to-mount mapping; `DiskStat` gains `read_bytes_per_sec`,
  `write_bytes_per_sec` (also visible in `/api/sysinfo`, additive). `Recorder` subscribes to the
  sampler, folds pairs of 5 s snapshots into 10 s rows, flushes every 60 s in one `WithTx`, writes
  1 m rollups in the same transaction and 5 m rollups on their boundary; hourly retention sweep.
- Files: `internal/sysinfo/{proc.go,sampler.go,testdata/diskstats*}`, `internal/metrics/{recorder.go,
  rollup.go,retention.go}`.
- Tests: diskstats fixtures (sda, nvme, dm-), counter wrap → 0; recorder with an injected clock
  and fake sampler: exact rows per tier after 6 simulated minutes; rollup avg/max; a missed tick
  leaves a gap, not a fabricated row; retention deletes in chunks.
- Done when a 10-minute fake run produces the expected rows in all host/disk tiers.

### Step 3: service usage recording

- What: `Services.Usage(ctx) []ServiceUsage{Name, Kind, State, CPUPercent, RSSBytes}` from cgroup
  (units, containers) and `/proc/<pid>` (apps, PID from the existing app adapter); batched
  `systemctl show` over all units in one exec (blocks separated by blank lines) instead of one
  exec per unit. Recorder polls every 30 s into `svc_30s`, rolls up `up_frac`/worst state.
- Files: `internal/sysinfo/{services.go,cgroup.go}`, `cmd/panel/m7.go` (pass PIDs through
  `sysinfoApps`), `internal/metrics/recorder.go`.
- Tests: cgroup fixture tree under `testdata/cgroup/`; CPU % from two `usage_usec` readings; a
  missing cgroup (unit stopped) gives state without usage, not an error; batched-show parser.
- Done when fixture-driven usage and the 30 s rows match expectations.

### Step 4: per-site minute metrics and the log format

- What: `urt="$upstream_response_time"` appended to `pbpanel` in `httpIncludeTemplate`; parser
  reads it (sum of parts split on `,` and `:`, `-` = absent). `wafmon.MinuteAgg` per site: counts,
  bytes, two 20-bin histograms; `Collector.DrainMinutes(closedBefore)`. Recorder drains each flush,
  skips minutes ≤ the site's watermark in `meta`, writes `site_1m`, rolls up `site_15m`.
- Files: `internal/nginx/render.go` (+ golden `testdata/panel-http.conf`), `internal/wafmon/{parse.go,
  collector.go,minute.go}`, `internal/metrics/sites.go`.
- Tests: parse lines with/without `urt`, multi-upstream, `-`; histogram bin edges and p50/p95
  interpolation; restart scenario (backfill replays already-flushed minutes → no double count);
  late lines inside the 15 s grace land in the right minute.
- Done when a replayed fixture log yields identical `site_1m` rows across a simulated restart.

### Step 5: query API, settings, status

- What: §4.4 read routes and `/api/metrics/clear`, §4.5 keys in `keys.go` (group `Metrics`),
  recorder reacts to `metrics.enabled` / retention changes; wiring in `cmd/panel/serve.go` (after
  the sampler and the wafmon collector exist; nil-safe when the sampler failed to start).
  `docs/API.md` updated.
- Files: `internal/httpapi/handlers_metrics.go`, `internal/httpapi` deps, `internal/settings/keys.go`,
  `internal/metrics/query.go`, `cmd/panel/serve.go`, `docs/API.md`.
- Tests: tier/step selection table test (every range ≤ 1 500 points); gaps as `null`; bad
  `range`/domain → 400/404; disabled → 409 with a reason; settings validation bounds.
- Done when handler tests pass against a seeded `metrics.db`.

### Step 6: charts on the dashboard and the site Metrics section

- What: `TimeSeriesChart` (SVG, multiple series, hover crosshair with readout, legend, keyboard
  focusable points summary, "Show data" table like `TrafficChart`); `RangePicker` (1h…90d, kept
  in the URL search params). Dashboard: a **History** card under the tiles (CPU/load, memory/swap,
  disk %, disk IO, network) and a per-service sparkline column on the service grid. Site menu:
  new `metrics` section (requests by status class reusing `CLASS_SERIES`, bytes, p50/p95 times;
  an "upstream time not available yet" note for sites whose lines lack `urt`).
- Files: `web/src/components/{TimeSeriesChart.tsx,RangePicker.tsx}`, `web/src/lib/metrics.ts`,
  `web/src/lib/api.ts`, `web/src/pages/{DashboardPage.tsx,SiteDetailPage.tsx}`, tests beside each;
  rebuild committed `web/dist`.
- Tests (vitest): series with gaps break the line; range change refetches; empty history state;
  accessible names; percentile tooltip text.
- Done when vitest is green and the pages render against a mocked API.

### Step 7: threshold alerts

- What: migration `0024_metrics_alerts.sql` + seed; `metrics.Alerts` evaluator after each flush
  (1 m tier; `*` targets expand to every mount/service/site seen in the last hour; cert rules read
  `certs.not_after` from panel.db); state machine with hysteresis; `notify.Emitter` injected
  (no-op until the notifications plan lands — if `internal/notify` is not in `main` yet, this step
  adds only the contract types and `Nop` exactly as specified and the sibling plan adopts them);
  §4.4 alert routes; an **Alerts** card on the dashboard and a rules editor in Settings → Metrics.
- Files: `internal/store/migrations/0024_metrics_alerts.sql`, `internal/metrics/{alerts.go,
  alerts_store.go}`, `internal/httpapi/handlers_alerts.go`, `cmd/panel/serve.go`,
  `web/src/components/AlertRules.tsx`, `DashboardPage.tsx`, `docs/API.md`.
- Tests: flapping input around the threshold emits one fire and one resolve, not many; `for` not
  yet elapsed → pending, no event; restart mid-firing → no duplicate fire; rule deleted while
  firing → resolve emitted; cert renewed → resolve; fake emitter records events; vitest for editor.
- Done when the table-driven evaluator tests and the API tests pass.

### Step 8: host gate script and docs

- `scripts/smoke/12-metrics-history.sh` (§6), handover notes in this file, Settings help text.

## 6. Host gate (Ubuntu sandbox; re-confirm the IP first)

1. Install the build; `metrics.db` appears under `/www/panel/data`, `panel.db` size unchanged ±
   the 0024 tables.
2. Record `ps -o rss= -p $(pidof panel)` and `metrics.db`+WAL size at T0.
3. Generate load for 10 minutes: `stress-ng --cpu 1 --vm 1 --vm-bytes 60%`, `dd` to disk,
   `ab`/`hey` against one PHP site and one proxy site (one request path hitting a dead upstream).
   The dashboard history shows the CPU, memory, disk IO and network bumps at the right times; the
   site Metrics section shows requests, 5xx, p95 request and upstream times.
4. Fill disk past 85 % with `fallocate`; within `for` + 1 min a `metrics.threshold` event is
   emitted (log line from the no-op/real emitter); delete the file, the resolved event follows
   after `clear_for`, exactly once.
5. `systemctl stop panel-php-8.4` for 6 minutes → service_down fires; start → resolves.
6. Restart the daemon mid-load: no gap beyond the restart window, no double-counted site minute
   (compare `site_1m` request sum to `wc -l` of the access log over the same minutes).
7. Reboot: recording resumes; alert state survives (no re-fire of an already-firing rule).
8. **24 h soak** at idle plus a light `hey` loop: at T+1 h, T+6 h, T+24 h record RSS, `metrics.db`
   + `-wal` size, and `/proc/<pid>/io` `write_bytes` delta. **Pass**: RSS growth ≤ 8 MB over the
   pre-feature baseline and flat between T+6 h and T+24 h; `metrics.db` within 25 % of §4.3's
   24 h projection for the host's actual service/site count; write volume ≤ 0.5 GB/day; WAL
   stays under 4 MiB (checkpoints keep up).
9. Retention: set `metrics.raw_retention=1h`; after the next hourly sweep `host_10s` holds ≤ 1 h;
   file size stops growing (page reuse) over the following hours.
10. `metrics.enabled=false`: no further writes (`write_bytes` flat apart from the daemon's own
    logs), live dashboard unchanged.

## 7. Risks

- **cgroup paths differ** (cgroup v1 hosts, Docker `cgroupfs` driver, nested containers/LXC
  VPSes): usage becomes "not available" per service, state still recorded. Step 0 decides how
  wide the probe is; EL and v1 are M9.
- **Device mapping** for disk IO (LVM, dm-crypt, ZFS, overlay roots) can miss; the mount still
  gets usage, IO shows "not available".
- **Log format change** reloads nginx via `panel-http.conf`; an operator who copied `pbpanel` into
  a hand-edited file keeps the old format and just has no upstream percentiles.
- **Busy sites**: wafmon caps reads per poll (`maxReadPerPoll`); a log growing faster than the
  collector reads delays minutes beyond the 15 s grace — late lines would land in a closed
  minute. Mitigation: grace extends while a tail has unread backlog; counted as `late` in status.
- **Log rotation gaps**: lines written between rotation and the next poll of the old file can be
  missed (same as wafmon today); counts are "as observed", stated in the UI.
- **SQLite contention**: separate file removes contention with panel.db; the only writer is the
  recorder. Long reads (90 d queries) use reader connections and WAL snapshots — capped at 1 500
  points, so short.
- **Disk-full**: a failing flush must not wedge the recorder or the panel: drop the batch, log
  once per 10 min, mark status `error`; the disk alert is evaluated from in-memory values when the
  flush failed, so "disk full" can still alert.
- **Clock jumps** (NTP step): timestamps come from the sampler's clock; a backwards jump makes
  `INSERT OR IGNORE` drop overlapping rows rather than fail.
- **Notify contract drift** between the two parallel plans: depend only on `Event`/`Emitter`;
  step 7 re-reads `notifications-plan.md` before coding.

## 8. Out of scope

- Per-pool PHP-FPM metrics (needs `pm.status_path` per pool), per-process app trees, per-iface
  network history, per-core CPU history.
- Request-level storage (paths, IPs, user agents) beyond wafmon's live hour.
- Remote write / Prometheus `/metrics` export, Grafana, multi-server aggregation.
- Anomaly detection, forecasting ("disk full in 3 days"), custom metric expressions.
- Notification channels, routing, repeat/escalation policy (notifications plan).
- EL (Rocky/RHEL) and cgroup v1 (M9).
