# Notifications and alerts: plan

Status: **steps 1-5 built on branch `notify-plan`; step 6 partly wired (engine and dispatcher run in the daemon, producers not yet); steps 6 producers, 7, 8 and the host gate not done; decisions D1-D13 ratified 2026-11-25 as recommended.** Telegram and Discord request shapes are still "(assumed)" (the doc check could not run). Migration `0022` in use.

Written after reading the dashboard alerts (`web/src/lib/alerts.ts`, `web/src/pages/DashboardPage.tsx`),
the job manager (`internal/jobs/jobs.go`, `events.go`), the cron service (`internal/cron/builtin.go`,
`scheduler.go`, `service.go`), the settings registry (`internal/settings/keys.go`, `settings.go`),
`internal/secretbox/secretbox.go`, the audit writer and its redaction (`internal/audit/audit.go`,
`redact.go`, `internal/httpapi/auditmw.go`), the HTTP layer (`internal/httpapi/deps.go`, `server.go`,
`sse.go`), the daemon wiring (`cmd/panel/serve.go`, `tls.go`, `firewall.go`, `m7.go`), the renewal
failure path (`internal/tls/renew.go`) and the firewall state (`internal/firewall/service.go`, `apply.go`).
Telegram and Discord API details come from prior knowledge and are marked **(assumed)**; step 4
checks them against the live APIs before the senders are called done.

Scope: Ubuntu first. Nothing here is distro-specific, but the host gate runs on Ubuntu only; EL stays in **M9**.

This plan **owns the shared contract** (section 4.1) that the backups, metrics history, auto-ban,
security checks and config-as-code plans consume. Step 1 lands the contract alone so those plans
can start before the rest of notify exists.

## 1. Goal

Events the panel already detects reach the operator when they are not looking at the dashboard:

- Channels: **SMTP email**, **generic webhook** (JSON, optional HMAC-SHA256 signature),
  **Telegram** bot, **Discord** webhook.
- Per channel: minimum severity, kind filter, quiet hours, "send resolved" toggle, enabled flag.
- Dedupe (same `Key` not re-sent within a window, escalation re-sends), a per-channel rate limit,
  "resolved" follow-ups when a condition clears.
- A test-send button per channel; a delivery log with attempts, errors and manual retry; automatic
  retries with backoff that survive a daemon restart.
- Credentials sealed with `internal/secretbox`, never returned by the API, never in audit or logs.
- Every event is also kept in an in-panel list behind a bell in the header (unread count, live via SSE).

What does **not** change: the dashboard's `AlertsPanel` keeps deriving its alerts client-side from
the same endpoints (`web/src/lib/alerts.ts`); the audit log stays the record of actions; the TLS
renewer, job manager and cron scheduler keep their behaviour and gain only an optional hook each;
no producer can fail or slow down because notify is broken, disabled or not wired.

## 2. Current state

- **Alerts exist only in the browser.** `web/src/lib/alerts.ts` `deriveAlerts` builds them from
  `/api/tls/certs` (renewal failed, expired, expiring in 14 days or 2 for IP certs), `/api/firewall`
  (`pending`, `!enabled`, `dirty`), site details (`site_file_missing`, detached features), cron jobs
  (last run bad), the audit log (failures in 24 h) and services (`failed`). Nothing server-side
  turns these into events. The file's comment that there is no jobs list is stale:
  `GET /api/jobs` exists (`internal/httpapi/server.go:91`).
- **TLS renewal failure** is reported through `tls.RenewerOptions.Audit` (`internal/tls/renew.go:43-61`,
  called from `report`, line 298), wired in `cmd/panel/tls.go:92-107` to an `audit.Entry`
  `tls.renew` with outcome `renewed|failed|skipped`. This closure is the natural producer seam.
- **Jobs** finish in `jobs.Manager.run` (`internal/jobs/jobs.go:258-297`) with a terminal `State`
  (`succeeded|failed|canceled|interrupted`) and `Err`. There is no finish hook today.
- **Cron runs** finish in `Service.execute` (`internal/cron/scheduler.go:220-235`), which writes
  `status` (`failed`, `timeout`, ...) and `error`. No hook. Built-ins are registered through
  `cron.Options.Builtins` and seeded by `EnsureDefaults` (`builtin.go:32-48`); `cmd/panel/tls.go:123`
  registers `tls.renew` (job `ssl-renew`, 03:17 daily).
- **Firewall** exposes `State{Enabled, Dirty, Pending, ...}` (`internal/firewall/service.go:81`) via
  `httpapi.FirewallService.State`. `Manager.expire` rolls back an unconfirmed apply and reports only
  through `Options.OnError` (`apply.go:368`, wired to the logger in `cmd/panel/firewall.go:34`).
- **Detached features** are computed per request by `sites.Manager.Detail` (`internal/sites/includes.go:130`).
- **Settings** are typed keys with groups (`internal/settings/keys.go`). `Meta.Secret` only masks the
  input; values are stored in plain text (`internal/settings` does not import secretbox). So channel
  credentials must not live in the registry.
- **secretbox**: `Box.Seal(plain, aad)` / `Open(sealed, aad)`, `v1:<keyid>:<b64>`; the box is built
  from `data/secret.key` in `cmd/panel/tls.go:41-48` and is not shared with anything else yet.
  Existing aad style: `"dns_providers:credentials:<name>"` (`internal/tls/store.go:381`).
- **Audit**: the middleware audits every non-GET request with status, request id and IP, never the
  body (`internal/httpapi/auditmw.go:54-90`); detail keys containing `secret`, `token`, `key`,
  `password`, ... are redacted (`internal/audit/redact.go:17`).
- **SSE**: `startSSE` / `event` / `comment` with a 15 s heartbeat (`internal/httpapi/sse.go`).
- **Deps**: optional features are nil-able fields on `httpapi.Deps`; nil means their routes answer
  503 (`internal/httpapi/deps.go`). Routes are registered per feature in `server.go:72-87`.
- No new Go module is needed: `net/smtp`, `crypto/tls`, `crypto/hmac`, `net/http` are stdlib.

## 3. Decisions

Ratified 2026-11-25: every row below is accepted as its recommendation, which the steps implement.

| # | Decision | Ratified (2026-11-25) |
|---|---|---|
| D1 | Durability of `Emit` | **Synchronous insert of the event row (2 s budget, detached from the caller's cancellation), asynchronous delivery from a DB-backed queue.** A restart loses nothing queued; a producer never waits on the network |
| D2 | Where channel credentials live | **`notify_channels.secret`, sealed with secretbox** (aad `notify_channels:secret:<id>`), not the settings registry (plain text). A host without `secret.key` gets bell-only notify: channel create answers 503 with the reason |
| D3 | Dedupe | **Per (channel, Key), window `notify.dedupe_window` = 6 h.** A higher severity for an open Key re-sends at once; a resolved follow-up is sent once, only if a problem for that Key was sent on that channel |
| D4 | Quiet hours | **Per channel `HH:MM`-`HH:MM` in the server's local time zone. Non-critical events are not sent (they stay in the bell and the log as `suppressed: quiet`); critical always goes.** No deferred digest |
| D5 | Rate limit | **Per channel, `notify.rate.per_hour` = 30.** Over the limit a delivery is `suppressed: rate`; critical is limited too (a storm of criticals is still a storm) |
| D6 | Webhook URL rules | **https anywhere; http only to loopback or RFC 1918/ULA; refuse link-local (169.254/16, fe80::/10) and unspecified.** Checked on save and again on each dial (DNS can change) |
| D7 | Which dashboard signals become events in this plan | **`tls.renew_failed`, `tls.expiring`, `job.failed`, `cron.failed`, `firewall.disabled`, `site.detached`.** Not `firewall.dirty`/`pending` (the operator is in the UI by definition) and not audit failures (an action the operator just took) |
| D8 | How polled conditions are found | **A cron built-in `notify.sweep` (job `notify-sweep`, every 5 min, root)**, visible and pausable on the Cron page like `ssl-renew`, rather than a hidden ticker |
| D9 | Default routing of a new channel | **Min severity `warning`, all kinds.** `info` kinds (`backup.succeeded`, `ban.added`) reach a channel only when picked explicitly |
| D10 | A delivery that fails for good | **Bell-only event `notify.delivery_failed`, never routed to a channel** (no loop, no storm) |
| D11 | Kind registry | **Open:** any well-formed kind is accepted and shown as-is; the known kinds are constants in `notify/kinds.go` with default severity and a label for the filter UI |
| D12 | Email | **Plain text only, stdlib `net/smtp`; STARTTLS, implicit TLS, or none only to loopback; PLAIN auth.** No OAuth2 (Gmail/M365 need an app password) |
| D13 | Retention | **Events: newest 1000 and at most 30 days** (`notify.events.keep`, `notify.events.retain_age`); deliveries go with their event |

## 4. Design

### 4.1 Contract for other features

The only file sibling plans import. `internal/notify/event.go` and `kinds.go` depend on the
standard library alone, so they land in step 1 with no store, no settings and no wiring.

```go
package notify

type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// Event is one thing that happened, or one condition that started or cleared.
type Event struct {
	Kind     string            // "<area>.<event>", see the naming rule; required
	Severity Severity          // info | warning | critical; empty means the kind's default
	Key      string            // dedupe identity; empty means Kind. Convention: Kind + ":" + subject id
	Subject  string            // one line, <= 200 bytes after trimming; required
	Body     string            // plain text, <= 4 KiB; may be empty
	Source   string            // producing feature: "tls", "jobs", "backup", ...; required
	At       time.Time         // when it happened; zero means now
	Resolved bool              // this Key's condition has cleared
	Link     string            // panel-relative path, e.g. "/tls" or "/sites/example.com"; optional
	Fields   map[string]string // <= 16 short labels for webhooks (domain, job, metric, value); optional
}

// Emitter is what producers depend on. Emit never blocks on the network, never
// returns an error and never panics; a malformed event is logged and dropped.
type Emitter interface {
	Emit(ctx context.Context, e Event)
}

// Syncer is for producers that recompute a whole set of conditions each pass
// (the sweep, threshold checks, security checks). For every kind in kinds,
// a Key that is open for this source and absent from current gets a Resolved
// event; every event in current is emitted as by Emit.
type Syncer interface {
	Sync(ctx context.Context, source string, kinds []string, current []Event)
}

// Nop discards everything. Producers default to it, so they can land and be
// tested before notify is wired.
type Nop struct{}

func (Nop) Emit(context.Context, Event)                     {}
func (Nop) Sync(context.Context, string, []string, []Event) {}

// Or returns e, or Nop when e is nil.
func Or(e Emitter) Emitter
```

Rules producers follow:

- **Wiring:** a producer takes `Notify notify.Emitter` in its existing `Options` struct (or a
  closure in `cmd/panel`, as the TLS renewer does) and calls `notify.Or(o.Notify)` once. It never
  imports anything else from notify.
- **Kind naming:** `^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$`, at most 48 bytes. `<area>` is the feature
  (`tls`, `job`, `cron`, `firewall`, `site`, `backup`, `metrics`, `ban`, `security`, `config`,
  `notify`); `<event>` is a past participle or a state (`failed`, `added`, `expiring`, `threshold`).
  A problem and its clearing share one kind: the clearing is `Resolved: true`, never a `*.ok` kind.
  An unrelated success worth telling is its own `info` kind (`backup.succeeded`).
- **Key:** stable across restarts and passes: `Kind + ":" + the subject's natural id` (domain, job
  kind, cron job name, metric and target, check id). Never a timestamp or a random id, or dedupe and
  resolve do nothing.
- **Subject/Body:** written for a human on a phone: no secrets, no full file paths of credentials,
  no raw stack traces; `Body` may carry the trimmed error text.
- **Resolved** is cheap to send: notify drops it unless the Key is open.

Known kinds (constants in `notify/kinds.go`; "owner" lands the producer):

| Kind | Default severity | Key | Resolved when | Owner |
|---|---|---|---|---|
| `tls.renew_failed` | critical | `tls.renew_failed:<domain>` | the next renewal of that domain succeeds | this plan |
| `tls.expiring` | warning (critical once expired) | `tls.expiring:<domain>` | days left back above the window (14, or 2 for IP certs, as `alerts.ts`) | this plan (sweep) |
| `job.failed` | warning | `job.failed:<job kind>` | a later job of that kind succeeds | this plan |
| `cron.failed` | warning | `cron.failed:<job name>` | the job's next run succeeds | this plan |
| `firewall.disabled` | warning | `firewall.disabled` | the firewall is enabled again | this plan (sweep) |
| `site.detached` | warning | `site.detached:<domain>` | every feature is attached again | this plan (sweep) |
| `notify.delivery_failed` | warning | `notify.delivery_failed:<channel id>` | the channel delivers again | this plan (bell only, D10) |
| `backup.failed` | critical | `backup.failed:<backup job>` | the next run of that backup succeeds | backups plan |
| `backup.succeeded` | info | `backup.succeeded:<backup job>` | never (not a condition) | backups plan |
| `metrics.threshold` | warning / critical | `metrics.threshold:<metric>[:<target>]`, metric one of `disk`, `cpu`, `mem`, `service` (e.g. `metrics.threshold:disk:/`, `metrics.threshold:service:panel-nginx`) | the value is back under the threshold / the service runs | metrics history plan |
| `ban.added` | info | `ban.added:<addr>` | never | auto-ban plan |
| `security.finding` | from the check | `security.finding:<check id>` | the check passes | security checks plan |

Reserved for the config-as-code plan, which defines them: `config.*` (for example `config.drift`,
`config.apply_failed`). The `ssl-renew` cron job's own failure is **not** also sent as `cron.failed`;
`tls.renew_failed` covers it.

### 4.2 Data model (`internal/store/migrations/0022_notify.sql`)

```sql
CREATE TABLE notify_channels (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    type          TEXT NOT NULL CHECK (type IN ('smtp', 'webhook', 'telegram', 'discord')),
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    min_severity  TEXT NOT NULL DEFAULT 'warning' CHECK (min_severity IN ('info', 'warning', 'critical')),
    kinds         TEXT NOT NULL DEFAULT '[]',   -- JSON array; [] = every kind at or above min_severity
    quiet_start   TEXT NOT NULL DEFAULT '',     -- 'HH:MM' local, '' = no quiet hours
    quiet_end     TEXT NOT NULL DEFAULT '',
    send_resolved INTEGER NOT NULL DEFAULT 1 CHECK (send_resolved IN (0, 1)),
    config        TEXT NOT NULL DEFAULT '{}',   -- JSON, non-secret fields per type
    secret        TEXT NOT NULL DEFAULT '',     -- secretbox-sealed JSON of the secret fields
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
CREATE TABLE notify_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, kind TEXT NOT NULL,
    severity TEXT NOT NULL, key TEXT NOT NULL, subject TEXT NOT NULL, body TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL, link TEXT NOT NULL DEFAULT '', fields TEXT NOT NULL DEFAULT '{}',
    resolved INTEGER NOT NULL DEFAULT 0, read_at TEXT
);
CREATE INDEX notify_events_at ON notify_events (at DESC);
CREATE TABLE notify_state (           -- one row per Key ever opened
    key TEXT PRIMARY KEY, kind TEXT NOT NULL, source TEXT NOT NULL, open INTEGER NOT NULL,
    severity TEXT NOT NULL, opened_at TEXT NOT NULL, last_event_id INTEGER NOT NULL
);
CREATE TABLE notify_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id   INTEGER NOT NULL REFERENCES notify_events (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES notify_channels (id) ON DELETE CASCADE,
    state      TEXT NOT NULL CHECK (state IN ('queued', 'sent', 'failed', 'suppressed')),
    reason     TEXT NOT NULL DEFAULT '',   -- suppressed: dedupe | quiet | rate | disabled
    attempts   INTEGER NOT NULL DEFAULT 0, next_at TEXT, last_error TEXT NOT NULL DEFAULT '',
    sent_at TEXT, created_at TEXT NOT NULL
);
CREATE INDEX notify_deliveries_due ON notify_deliveries (state, next_at);
```

Assumed, checked in step 2: `store.Open` turns on `PRAGMA foreign_keys` (the cascade relies on it).
Events filtered out by a channel's severity/kind filter get **no** delivery row (the log would drown).

Per-type fields (config / secret): smtp `host, port, security(starttls|tls|none), username, from, to[]` /
`password`; webhook `url, sign(bool)` / `hmac_secret, headers{}`; telegram `chat_id` / `bot_token`;
discord — / `webhook_url` (the URL carries the token, so it is secret).

### 4.3 Routing and delivery

`Emit` (in a `*notify.Service`): normalise (trim, cap lengths, default severity from the kind,
`At` = now), drop a malformed event with one log line, insert the event, then in the same transaction:
update `notify_state` (open on a problem, close on resolved; a resolved for a Key that is not open
is stored in the list but routed nowhere), and for each enabled channel whose filter matches insert a
delivery row: `queued`, or `suppressed` with the reason from quiet hours, dedupe (D3) or rate (D5,
an in-memory token bucket per channel, refilled from the clock). Then publish to the bell's SSE
subscribers and wake the dispatcher.

The dispatcher (one goroutine) takes due `queued` rows, sends with a 15 s timeout, and on failure
sets `next_at` with backoff 30 s, 2 m, 10 m, 30 m, 2 h; after 5 attempts the row is `failed` and
D10 fires. A 4xx other than 408/429 is permanent at once; `429` honours `Retry-After` / Telegram's
`retry_after` (assumed). Every error text passes a redactor that removes the channel's secret
values and any URL path/query (Go's `*url.Error` prints the Telegram URL, token included).

Payloads:

- **Email:** `Subject: [<label>] <SEVERITY>: <subject>` (or `RESOLVED:`), body = body, kind, key,
  time, link. `<label>` is `notify.label` or the hostname.
- **Webhook:** `POST`, `Content-Type: application/json`, body
  `{"version":1,"id":<event id>,"kind","severity","key","subject","body","source","at"(RFC 3339),
  "resolved","link"(absolute, or omitted),"fields":{},"host":"<label>"}`. Headers `X-PBPanel-Event`
  (event id, for idempotency), `X-PBPanel-Timestamp` (unix seconds) and, when signing,
  `X-PBPanel-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>`.
- **Telegram (assumed API):** `POST https://api.telegram.org/bot<token>/sendMessage`,
  `{"chat_id","text","disable_web_page_preview":true}`, no `parse_mode` (no escaping bugs), text
  capped at 4096.
- **Discord (assumed API):** `POST <webhook_url>` with one embed (title = subject, description =
  body capped at 4096, colour by severity, footer = kind and host), `allowed_mentions: {parse: []}`.

Absolute links: `notify.panel_url` if set, else `https://<panel.domain>:<panel.port>` when
`panel.domain` is bound, else no link.

### 4.4 Settings keys (new group "Notifications" in `internal/settings/keys.go`)

| Key | Type | Default | Notes |
|---|---|---|---|
| `notify.enabled` | bool | `true` | Off: events still go to the bell; nothing is delivered (rows `suppressed: disabled`) |
| `notify.dedupe_window` | duration | `6h` | D3; read on each event |
| `notify.rate.per_hour` | int | `30` | D5; per channel |
| `notify.events.keep` | int | `1000` | D13 |
| `notify.events.retain_age` | duration | `720h` | D13 |
| `notify.panel_url` | string, optional | `""` | https URL used for links; validated like `runtimes.url` |
| `notify.label` | string, optional | `""` | Host label in messages; empty = `os.Hostname()` |

### 4.5 API routes (`internal/httpapi/handlers_notify.go`, `Deps.Notify`, nil = 503)

| Route | Purpose |
|---|---|
| `GET /api/notify/events?limit=&before=&unread=` | the list behind the bell and the Events tab |
| `GET /api/notify/summary` | `{unread, open_critical, open_warning}` for the badge |
| `POST /api/notify/events/read` | `{ids:[...]}` or `{all:true}` |
| `GET /api/notify/stream` | SSE, `event: notification` with the event JSON; heartbeat from `sse.go` |
| `GET /api/notify/kinds` | the known kinds, default severity, label (filter UI) |
| `GET /api/notify/channels`, `POST` | list (secrets as `{"<field>_set":true}`) / create |
| `GET/PUT/DELETE /api/notify/channels/{id}` | a secret field omitted on PUT keeps the stored value; `""` clears it |
| `POST /api/notify/channels/{id}/test` | synchronous send of a fixed `notify.test` event (info, bypasses filters, quiet, dedupe, rate), 20 s cap; `{ok, error}`; logged as a delivery |
| `GET /api/notify/deliveries?channel=&state=&limit=&before=` | delivery log with event subject joined |
| `POST /api/notify/deliveries/{id}/retry` | `failed` back to `queued`, attempts reset |

Mutations are audited by the existing middleware (body never recorded). Handlers add audit notes only
with the channel name and type (`notes` keys never include config or secret). `docs/API.md` documents all.

## 5. Steps

Each step ends green on the gate (`go test ./cmd/... ./internal/...` in WSL, CI golangci-lint on the
changed code, vitest for UI steps) and is committed on its own.

### Step 1: the contract

- What: `internal/notify/event.go` (types, `Emitter`, `Syncer`, `Nop`, `Or`), `kinds.go` (constants,
  default severities, labels, `ValidKind`), `normalize.go` (`Normalize(Event, now) (Event, error)`:
  kind regex, caps, default severity/Key/At, Fields limit). `doc.go` restates section 4.1's rules.
- Tests: table tests for `ValidKind`, `Normalize` (caps on rune boundaries, default Key, severity
  from kind, unknown kind accepted, bad kind refused), `Nop` satisfies both interfaces.
- Done when: the sibling plans can import `notify` and pass `notify.Nop{}`; no other package changed.

### Step 2: migration and store

- What: `0022_notify.sql` (section 4.2); `internal/notify/store.go`: channel CRUD with sealing
  (needs `*secretbox.Box`; aad `notify_channels:secret:<id>`, sealed in the insert transaction after
  the id is known), event insert/list/read/prune, state upsert, delivery insert/claim/update/list.
  Check `foreign_keys` is on; if not, delete deliveries explicitly.
- Tests: migration applies on a fresh DB and on a DB at 0021; a sealed secret round-trips and a
  value copied to another row fails to open; GET shape never carries the secret; prune keeps the
  newest N and drops by age with deliveries.
- Done when: store tests pass against the real migration set.

### Step 3: the engine

- What: `internal/notify/service.go`: `New(Options{Store, Box, Settings reader, Clock, Logger})`,
  `Emit`, `Sync`, routing (filter, quiet hours across midnight, dedupe with escalation, resolved
  rules, rate bucket), SSE fan-out (`Subscribe() (<-chan Event, func())`, like `sysinfo.Sampler`).
  Settings keys of 4.4 added to `keys.go` with validators.
- Tests (fake clock): dedupe inside/outside the window; escalation warning to critical re-sends;
  resolved sent once and only after a sent problem; `send_resolved=false`; quiet window 22:00-07:00
  at 23:00, 06:59, 07:00; critical in quiet hours; rate bucket at 30 and refill; `notify.enabled`
  off; `Sync` resolves missing open Keys of its source and kinds only; `Emit` with a canceled ctx
  still stores; a DB error does not panic or block past 2 s.
- Done when: every D3/D4/D5 rule has a named test.

### Step 4: senders and dispatcher

- What: `sender_smtp.go`, `sender_webhook.go`, `sender_telegram.go`, `sender_discord.go` behind
  `type sender interface { Send(ctx, Channel, Event) error }`; `dispatch.go` (claim due rows,
  backoff, permanent vs retryable, D10, restart resume); `redact.go` for error text; `dialguard.go`
  for D6 (resolve, check every address, dial the checked one).
- Tests: `httptest` servers for webhook (signature verified by an independent HMAC in the test),
  Telegram and Discord (base URLs injectable); an in-test SMTP server on a `net.Listener` for
  STARTTLS with a test cert, implicit TLS and auth; 500 then 200 retries; 400 permanent; 429 with
  `Retry-After`; an error with the token in the URL comes out without it; D6 refuses
  `169.254.169.254` and accepts `127.0.0.1` over http; queued rows resume after a new Service.
- Done when: gate green, and Telegram/Discord request shapes checked once by hand against their docs
  (record the check in this file, correcting the "(assumed)" marks).

### Step 5: HTTP API

- What: `handlers_notify.go` with section 4.5's routes, `Deps.Notify NotifyService` (interface,
  nil = 503), registration in `server.go`; validation errors field-named 422 as elsewhere;
  `docs/API.md`.
- Tests: route table (auth required, 503 when nil); create/update keeps an omitted secret and
  clears `""`; GET never returns a secret; test-send result; SSE yields a `notification` event;
  the audit row for a channel create carries no config values.
- Done when: `routes_test.go` lists the new routes and handler tests pass.

### Step 6: wiring and producers

- What: `cmd/panel/notify.go` builds the Service (shares the secretbox built for TLS: move the box
  construction out of `newTLS` into one helper both use), starts the dispatcher, passes it to
  `httpapi.Deps`. Producers:
  - TLS: extend the `Audit` closure in `cmd/panel/tls.go:95` to emit `tls.renew_failed`
    (failed) / resolved (renewed); `internal/tls` unchanged.
  - Jobs: `jobs.WithOnFinish(func(Job))` option, called in `run` after `finish`; `cmd/panel` maps
    `failed` to `job.failed`, `succeeded` to resolved; `canceled`/`interrupted` emit nothing.
  - Cron: `cron.Options.OnRunFinished func(Job, Status, string)`, called in `execute` after the
    row update; `failed`/`timeout` to `cron.failed`, `succeeded` to resolved; built-in `tls.renew`
    skipped.
  - Sweep: built-in `notify.sweep` (job `notify-sweep`, `*/5 * * * *`, root, 2 min timeout) added
    to `defaultJobs` and registered next to `tls.renew` in `cmd/panel`; it reads firewall `State`,
    each site's `Detail`, and the cert list, then calls `Sync("sweep", {firewall.disabled,
    site.detached, tls.expiring}, current)`, writing one line per condition to the run output.
- Tests: `jobs` and `cron` hook tests (called once, right status, nil hook safe); sweep against fakes
  (opens then resolves each kind); `cmd/panel` wiring test that a host without `secret.key` still
  serves notify bell-only.
- Done when: a failing cron job in a test produces one event and one queued delivery.

### Step 7: interface

- What: `web/src/lib/notify.ts` (types, queries, SSE hook), a bell in `components/layout/AppLayout.tsx`
  beside `UserMenu` (badge = unread, popover with the latest 10, "mark all read", link to the page),
  `pages/NotificationsPage.tsx` at `/notifications` with tabs **Events** (filter by severity/kind,
  unread), **Channels** (create/edit dialog per type, secrets as write-only fields showing "set",
  severity, kinds, quiet hours, send resolved, **Send test** with the result inline), **Delivery log**
  (state, reason, attempts, last error, Retry). Nav entry in `nav.ts`; route in `routes/_app`.
- Tests (vitest): badge count and live increment from a mocked stream; channel form per type keeps a
  secret it was not given; test-send success and failure render; delivery log Retry calls the route.
- Done when: vitest green and the page viewed once in a browser against a local daemon.

### Step 8: smoke script

- What: `scripts/smoke/10-notifications.sh` (section 6), using `scripts/smoke/lib.sh`.
- Done when: it passes on the Ubuntu sandbox and the result is recorded in this file.

## 6. Host gate (Ubuntu sandbox, `scripts/smoke/10-notifications.sh`)

On the current Ubuntu sandbox (re-confirm the IP), root SSH, panel installed from this branch:

1. Start a loopback webhook receiver and a loopback SMTP sink (small Python scripts the smoke writes
   to a temp dir; `smtpd` is gone from Python 3.12+, so a `socketserver` sink). Create a webhook
   channel (signed) and an smtp channel (`security: none` to 127.0.0.1).
2. **Test send** on each: 200 `{ok:true}`; the receiver's body verifies with `openssl dgst -hmac`;
   the sink got one message.
3. **Secret at rest:** `sqlite3 panel.db "SELECT secret FROM notify_channels"` starts with `v1:`
   and does not contain the HMAC secret or password; `GET /api/notify/channels` does not either;
   neither does `audit.log` or `journalctl -u panel`.
4. **cron.failed + resolved:** a cron job running `false`, run twice inside the window: one webhook
   delivery, the second `suppressed: dedupe`; change it to `true`, run: one resolved delivery.
5. **firewall.disabled via the sweep:** disable the firewall, run `notify-sweep` through
   `POST /api/cron/{id}/run`: one delivery; enable and confirm, run the sweep: resolved.
6. **tls.renew_failed:** a staging cert for a name that does not resolve to the host,
   `RenewNow`: one critical delivery on both channels.
7. **Quiet hours:** set quiet hours covering now on the webhook channel; a warning is
   `suppressed: quiet`, the critical from 6 is delivered.
8. **Retry and restart:** stop the receiver, trigger an event, see `attempts` grow; restart the
   panel; start the receiver; the delivery ends `sent` without a new event.
9. **Bell:** `GET /api/notify/summary` counts match; the SSE stream yields the event of step 4.
10. Optional, `SKIP` without tokens: Telegram and Discord test sends with operator-supplied
    credentials passed as environment to the script (never committed).

## 7. Risks

- **Secret leakage through error text.** HTTP client errors print the request URL (Telegram token,
  Discord webhook token). Mitigation: redactor on every stored or logged error, tested with the
  secret in the URL. Audit notes carry name and type only.
- **SSRF from the webhook URL.** Single admin, but a stolen session could probe the metadata
  service. D6 plus dial-time re-check; no redirects followed for webhooks.
- **Storms.** A sweep on a host with many detached sites, or a flapping service from the metrics
  plan, could flood a channel. Dedupe per Key plus the per-channel rate limit; flapping control
  (hysteresis) is the producer's job and the metrics plan must say so.
- **Producer slowdown.** `Emit` writes SQLite on the producer's goroutine; with a busy database
  this waits up to the 2 s budget. The cron and jobs hooks run after their own row is written, so a
  slow emit delays nothing the operator sees.
- **SMTP reality.** Many VPS providers block outbound port 25; Gmail/M365 need app passwords; stdlib
  `net/smtp` is frozen (no OAuth2). Documented on the channel form; 587/465 are the defaults.
- **Assumed third-party APIs.** Telegram/Discord shapes and 429 bodies are from prior knowledge;
  step 4 checks them.
- **Quiet hours and DST.** Local-time windows shift by an hour twice a year; tested at the boundaries,
  accepted.
- **Two lists of alerts.** The dashboard keeps its client-side alerts and the bell has events; they
  can disagree in wording or timing (5 min sweep). Accepted for this plan; unifying is out of scope.
- **secret.key rotation.** A replaced key makes channel secrets unreadable (`ErrUnknownKey`); the
  channel shows "credentials unreadable, re-enter" rather than failing silently.

## 8. Out of scope

- Slack, Microsoft Teams, ntfy, Gotify, SMS, browser push (`Notification` API); a generic webhook
  with a payload template would cover several of these and is a later step.
- Digest emails, escalation policies, on-call schedules, acknowledging from a channel.
- Per-event message templates, HTML email, attachments.
- Moving the dashboard's alerts onto the server event list.
- Notifying for audit failures, `firewall.dirty`/`pending`, or a firewall rollback after timeout.
- Multiple users or per-user subscriptions (decision 3: one admin).
- EL (Rocky/RHEL) host gate: **M9**.
