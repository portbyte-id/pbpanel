# HTTP API reference

<!-- AUTO-GENERATED from the mux.Handle registrations in internal/httpapi/*.go. Regenerate with /ecc:update-docs rather than editing by hand. -->

The daemon serves a REST/JSON API under `/api` and the embedded SPA elsewhere. There is no
`openapi.yaml`; the route table below is generated from the code that registers the routes.

## Conventions

- **Auth.** Every route except `POST /api/login`, `GET /api/login/challenge` and `GET /api/health`
  needs a session cookie. An unauthenticated request answers `401`.
- **Login check.** After a failed login from an address, its next `POST /api/login` must carry
  `challenge` and `nonce`: a proof-of-work answer from `GET /api/login/challenge`. A login without
  a solved check answers `403 {"error":"challenge_required"}` before the password is checked. Each
  answer opens one attempt. The check stays on until a successful login, and a lockout (`429`)
  still applies on top of it.
- **CSRF.** Unsafe methods (`POST`, `PUT`, `DELETE`, `PATCH`) need the session's token in the
  `X-CSRF-Token` header. A missing or wrong token answers `403`.
- **Audit.** Every state-changing route writes an audit entry through the audit middleware.
- **Errors.** The envelope is `{"error": "<class>", "detail": "<sentence>"}` with optional `field`
  (the rejected input) and `output` (verbatim validator text). A route whose dependency is not
  wired answers `503 {"error":"unavailable"}`.
- **Live streams.** Jobs and some logs use Server-Sent Events, resumable with `Last-Event-ID`.

## Routes (166 under `/api`)

### Session and account

| Method | Path |
|--------|------|
| `POST` | `/api/login` |
| `GET` | `/api/login/challenge` (no session needed) |
| `POST` | `/api/logout` |
| `GET` | `/api/me` |
| `POST` | `/api/password` |
| `POST` | `/api/username` |
| `GET` | `/api/health` (no session needed) |

### Settings, audit, jobs

| Method | Path |
|--------|------|
| `GET` | `/api/settings` |
| `PUT` | `/api/settings` |
| `GET` | `/api/audit` |
| `GET` | `/api/jobs` |
| `GET` | `/api/jobs/{id}/events` (SSE) |

### Sites

| Method | Path |
|--------|------|
| `GET` | `/api/sites` |
| `POST` | `/api/sites` |
| `GET` | `/api/sites/{domain}` |
| `PUT` | `/api/sites/{domain}` |
| `DELETE` | `/api/sites/{domain}` |
| `GET` | `/api/sites/{domain}/file` |
| `PUT` | `/api/sites/{domain}/file` |
| `GET` | `/api/sites/{domain}/includes` |
| `GET` | `/api/sites/{domain}/includes/{name}` |
| `PUT` | `/api/sites/{domain}/includes/{name}` |
| `POST` | `/api/sites/{domain}/regenerate` |
| `GET` | `/api/sites/{domain}/waf` |
| `PUT` | `/api/sites/{domain}/waf` |
| `GET` | `/api/waf/status` |

### Nginx and config history

| Method | Path |
|--------|------|
| `GET` | `/api/nginx/settings` |
| `PUT` | `/api/nginx/settings` |
| `GET` | `/api/nginx/conf` |
| `PUT` | `/api/nginx/conf` |
| `GET` | `/api/services/nginx` |
| `POST` | `/api/nginx/container/{action}` |
| `POST` | `/api/nginx/image/version` |
| `GET` | `/api/nginx/image` |
| `POST` | `/api/nginx/image/update` |
| `POST` | `/api/nginx/image/builds/{id}/activate` |
| `GET` | `/api/config-history/{file}` |
| `GET` | `/api/config-history/{file}/{version}` |
| `POST` | `/api/config-history/{file}/{version}/restore` |

### PHP

| Method | Path |
|--------|------|
| `GET` | `/api/php` |
| `GET` | `/api/php/extensions` |
| `POST` | `/api/php/{ver}/pull` |
| `DELETE` | `/api/php/{ver}` |
| `POST` | `/api/php/{ver}/build` |
| `GET` | `/api/php/{ver}/builds` |
| `POST` | `/api/php/{ver}/builds/{id}/activate` |
| `GET` | `/api/php/{ver}/settings` |
| `PUT` | `/api/php/{ver}/settings` |
| `GET` | `/api/php/{ver}/ini` |
| `PUT` | `/api/php/{ver}/ini` |
| `GET` | `/api/sites/{domain}/pool` |
| `PUT` | `/api/sites/{domain}/pool` |
| `PUT` | `/api/sites/{domain}/php-version` |

### TLS

| Method | Path |
|--------|------|
| `GET` | `/api/sites/{domain}/tls` |
| `POST` | `/api/sites/{domain}/tls/upload` |
| `POST` | `/api/sites/{domain}/tls/issue` |
| `POST` | `/api/sites/{domain}/tls/renew` |
| `PUT` | `/api/sites/{domain}/tls/redirect` |
| `DELETE` | `/api/sites/{domain}/tls` |
| `GET` | `/api/tls/certs` |
| `POST` | `/api/tls/certs/upload` |
| `POST` | `/api/tls/certs/issue` |
| `DELETE` | `/api/tls/certs/{domain}` |
| `GET` | `/api/tls/providers` |
| `PUT` | `/api/tls/providers/{name}` |
| `DELETE` | `/api/tls/providers/{name}` |
| `POST` | `/api/tls/providers/{name}/scan` |
| `GET` | `/api/panel/cert` |
| `POST` | `/api/panel/cert/upload` |
| `POST` | `/api/panel/cert/issue` |
| `GET` | `/api/panel/cert/ip` |
| `POST` | `/api/panel/cert/ip` |

### File manager

| Method | Path |
|--------|------|
| `GET` | `/api/fm/list` |
| `GET` | `/api/fm/stat` |
| `GET` | `/api/fm/disks` |
| `POST` | `/api/fm/mkdir` |
| `POST` | `/api/fm/create` |
| `POST` | `/api/fm/rename` |
| `POST` | `/api/fm/move` |
| `POST` | `/api/fm/copy` |
| `POST` | `/api/fm/chmod` |
| `POST` | `/api/fm/chown` |
| `POST` | `/api/fm/delete` |
| `GET` | `/api/fm/trash` |
| `POST` | `/api/fm/trash/{id}/restore` |
| `DELETE` | `/api/fm/trash/{id}` |
| `GET` | `/api/fm/file` |
| `PUT` | `/api/fm/file` |
| `POST` | `/api/fm/uploads` |
| `GET` | `/api/fm/uploads/{id}` |
| `PUT` | `/api/fm/uploads/{id}` |
| `POST` | `/api/fm/uploads/{id}/complete` |
| `DELETE` | `/api/fm/uploads/{id}` |
| `GET` | `/api/fm/download` |
| `POST` | `/api/fm/archive` |
| `POST` | `/api/fm/extract` |

### Firewall and WAF

| Method | Path |
|--------|------|
| `GET` | `/api/firewall` |
| `GET` | `/api/firewall/rules` |
| `POST` | `/api/firewall/rules` |
| `PUT` | `/api/firewall/rules/{id}` |
| `DELETE` | `/api/firewall/rules/{id}` |
| `GET` | `/api/firewall/blocklist` |
| `POST` | `/api/firewall/blocklist` |
| `DELETE` | `/api/firewall/blocklist/{id}` |
| `GET` | `/api/firewall/allowlist` |
| `POST` | `/api/firewall/allowlist` |
| `DELETE` | `/api/firewall/allowlist/{id}` |
| `POST` | `/api/firewall/enable` |
| `POST` | `/api/firewall/apply` |
| `POST` | `/api/firewall/confirm` |
| `POST` | `/api/firewall/revert` |
| `POST` | `/api/firewall/disable` |
| `GET` | `/api/waf/traffic` |
| `GET` | `/api/waf/requests` |
| `GET` | `/api/waf/events` |
| `GET` | `/api/waf/events/summary` |
| `GET` | `/api/waf/global` |
| `PUT` | `/api/waf/global` |
| `GET` | `/api/waf/sources` |
| `GET` | `/api/waf/geo` |
| `POST` | `/api/waf/geo/download` |

`GET /api/sites/{domain}/waf`, `GET /api/waf/status` and the two `PUT`s carry an
`open_appsec` object: the same availability the Store card returns (`available`,
`reason`, `installed`, `agent_active`, `agent_version`, `policy_load`,
`pinned_version`, `warning`, `acknowledgement`). It is omitted on a panel with no
appsec package, so a client can tell "no engine" from "engine, unavailable". A
site may choose the open-appsec engine only while `available` is true.

### Store: open-appsec agent

The second firewall engine, Check Point's open-appsec. The agent is installed and
removed from the Store, as jobs. Answers 503 on a panel with no appsec package.

| Method | Path |
|--------|------|
| `GET` | `/api/appsec` |
| `POST` | `/api/appsec/install` |
| `POST` | `/api/appsec/uninstall` |

`GET /api/appsec` returns the availability object:

```json
{
  "available": true,
  "reason": "",
  "installed": true,
  "agent_active": true,
  "agent_version": "1.1.37-open-source",
  "policy_load": "Success",
  "pinned_version": "1.1.37",
  "warning": "The open-appsec agent keeps a continuous TLS connection to Check Point's cloud …",
  "acknowledgement": "open-appsec-cloud-connection-accepted"
}
```

`available` means the serving nginx build has the attachment **and** the agent is
installed and active. `warning` is the tracking statement and `acknowledgement`
is the value to send back; both come from the server so the interface renders the
one text the operator agreed to.

`POST /api/appsec/install` requires `{"acknowledge": "<the acknowledgement value>"}`.
Without it the route answers `422 invalid_input` on field `acknowledge` and no job
starts. With it, a job of kind `appsec.install` runs and the response is
`202 {"job": {…}, …availability}`.

`POST /api/appsec/uninstall` starts a job of kind `appsec.uninstall`. It is
refused — the job fails with the reason — while any site's firewall has the
open-appsec engine.

### Store: ClamAV

The open-source antivirus scanner, installed from the distribution's packages
(`clamav`, `clamav-daemon`, `clamdscan`, `clamav-freshclam`) and removed from the Store, as
jobs. Answers 503 on a panel built without it. Ubuntu only, like the rest of the
package layer.

| Method | Path |
|--------|------|
| `GET` | `/api/clamav` |
| `POST` | `/api/clamav/install` |
| `POST` | `/api/clamav/uninstall` |
| `POST` | `/api/clamav/scan` |

`GET /api/clamav` returns the state, read from the scanner and systemd rather than
from files:

```json
{
  "installed": true,
  "version": "1.4.3",
  "signature_version": "27800",
  "signature_date": "Tue Nov 25 08:00:00 2026",
  "daemon_active": true,
  "updater_active": true,
  "scanning": false,
  "reason": "",
  "packages": ["clamav", "clamav-daemon", "clamdscan", "clamav-freshclam"]
}
```

`reason` is one sentence on what is wrong or missing, empty when healthy.
`scanning` is true while a scan started from the panel is running.

`POST /api/clamav/install` starts a job of kind `clamav.install`: apt install,
the first `freshclam` download (about 300 MB, with its output in the job log),
then the updater and daemon are started and the job waits for the daemon's
socket. `POST /api/clamav/uninstall` starts `clamav.uninstall`, which purges the
packages and the signatures and is refused while a scan runs. Both answer
`202 {"job": {…}, …state}`.

`POST /api/clamav/scan` takes `{"path": "/var/www/site"}`, validates the path
through the file manager (`400` invalid, `404` missing, `403` protected) and
starts a job of kind `clamav.scan`. It answers `409 conflict` when ClamAV is not
installed or another scan is running; scans never overlap. The scan uses the
daemon (`clamdscan --fdpass --multiscan`) when it is running and `clamscan`
otherwise. The job's output lists each infected file as `<path>: <signature>`.
**A scan that finds infected files fails the job**, so a clean run is the only
green one; nothing is quarantined or deleted.

### Apps and runtimes

| Method | Path |
|--------|------|
| `GET` | `/api/apps` |
| `POST` | `/api/apps` |
| `GET` | `/api/apps/{name}` |
| `PUT` | `/api/apps/{name}` |
| `DELETE` | `/api/apps/{name}` |
| `POST` | `/api/apps/{name}/start` |
| `POST` | `/api/apps/{name}/stop` |
| `POST` | `/api/apps/{name}/restart` |
| `GET` | `/api/apps/{name}/logs` |
| `GET` | `/api/apps/{name}/logs/stream` |
| `GET` | `/api/runtimes` |
| `POST` | `/api/runtimes/node` |
| `DELETE` | `/api/runtimes/node/{version}` |

### Cron

| Method | Path |
|--------|------|
| `GET` | `/api/cron` |
| `POST` | `/api/cron` |
| `GET` | `/api/cron/{id}` |
| `PUT` | `/api/cron/{id}` |
| `DELETE` | `/api/cron/{id}` |
| `POST` | `/api/cron/{id}/enable` |
| `POST` | `/api/cron/{id}/disable` |
| `POST` | `/api/cron/{id}/run` |
| `GET` | `/api/cron/{id}/runs` |
| `GET` | `/api/cron/{id}/runs/{runId}` |

Note: run detail is `/api/cron/{id}/runs/{runId}`, not `/api/cron/runs/{runId}` — Go's mux cannot
register both. See [design/m7-api.md](design/m7-api.md).

### Dashboard, services, logs

| Method | Path |
|--------|------|
| `GET` | `/api/sysinfo` |
| `GET` | `/api/sysinfo/stream` (SSE) |
| `GET` | `/api/services` |
| `POST` | `/api/services/{name}/{action}` |
| `GET` | `/api/logs` |
| `GET` | `/api/logs/{id}/tail` |
| `GET` | `/api/logs/{id}/search` |

### SSH

| Method | Path |
|--------|------|
| `GET` | `/api/ssh/attempts` |
| `GET` | `/api/ssh/attempts/abuseipdb` |

### Docker

| Method | Path |
|--------|------|
| `GET` | `/api/docker` |
| `GET` | `/api/docker/containers` |
| `GET` | `/api/docker/stats` |
| `GET` | `/api/docker/containers/{id}/stats` |
| `GET` | `/api/docker/containers/{id}/top` |
| `GET` | `/api/docker/containers/{id}/logs` |
| `POST` | `/api/docker/containers/{id}/start` |
| `POST` | `/api/docker/containers/{id}/stop` |
| `POST` | `/api/docker/containers/{id}/restart` |
| `POST` | `/api/docker/containers/{id}/kill` |
| `DELETE` | `/api/docker/containers/{id}` |
| `GET` | `/api/docker/images` |
| `DELETE` | `/api/docker/images/{id}` |
| `GET` | `/api/docker/volumes` |
| `DELETE` | `/api/docker/volumes/{name}` |

## Detail per module

Nested payloads (request and response bodies) are documented per module:

- [design/m7-api.md](design/m7-api.md) — sysinfo, services, logs, cron
- Site, nginx, PHP and TLS routes: `internal/httpapi/handlers_*.go`, one file per module.
<!-- END AUTO-GENERATED -->

## Notifications

The bell, the channels and the delivery log (`docs/design/notifications-plan.md` §4.5). Every route
needs a session. Without a notify service (no `data/secret.key` for channel writes, or a failed start)
the routes answer 503. A channel never comes back with its secrets: `secret_set` names the stored
fields. A write that omits a secret keeps its stored value; `""` clears it.

| Route | Purpose |
|---|---|
| `GET /api/notify/events?limit=&before=&unread=1` | the event list, newest first |
| `GET /api/notify/summary` | `{unread, open_critical, open_warning}` for the badge |
| `POST /api/notify/events/read` | `{"ids":[...]}` or `{"all":true}`; answers `{updated}` |
| `GET /api/notify/stream` | SSE; `event: notification` with the event JSON; a comment every 15 s |
| `GET /api/notify/kinds` | the known kinds with default severity and label |
| `GET /api/notify/channels`, `POST` | list, or create (201); 422 names the bad field, 409 a taken name |
| `GET/PUT/DELETE /api/notify/channels/{id}` | read, update (200), delete (204) |
| `POST /api/notify/channels/{id}/test` | fixed test send, bypasses filters; answers `{ok, error}` and is logged as a delivery |
| `GET /api/notify/deliveries?channel=&state=&limit=&before=` | the delivery log, with channel and event names |
| `POST /api/notify/deliveries/{id}/retry` | a failed delivery back in the queue; 409 if it is not failed |

Channel write body: `name`, `type` (`smtp`, `webhook`, `telegram`, `discord`), `enabled`, `min_severity`,
`kinds`, `quiet_start`/`quiet_end` (`HH:MM`, local time), `send_resolved`, `config` (non-secret fields),
`secret` (secret fields only: `password`, `hmac_secret`, `bot_token`, `webhook_url`).

## Metrics history

Stored history of the host, disks, services and sites (`docs/design/metrics-history-plan.md` §4.4).
Every route needs a session. Without a history database (a failed start) the routes answer 503. The
four chart routes answer `409` while `metrics.enabled` is off; status and clear keep working.

Every chart response is **columnar**: `step` (seconds per point), `t` (unix start of each point) and
one array per series, the same length as `t`. A point with no data is `null`, never interpolated:
that is a daemon that was down, or a series that did not exist yet. The finest tier whose retention
reaches back to the start of the range is used, and `step` is raised so that no series has more than
1 500 points.

| Route | Purpose |
|---|---|
| `GET /api/metrics/host?range=` | `cpu`, `cpu_max`, `load1`, `load1_max`, `load5`, `load15`, `mem_used`, `mem_avail`, `swap_used`, `net_rx`, `net_tx` |
| `GET /api/metrics/disks?range=` | `mounts: [{mount, used_pct, used_pct_max, used, rd_bps, wr_bps}]`; IO is `null` for a device that could not be mapped |
| `GET /api/metrics/services?range=&name=` | `services: [{name, state, up_frac, cpu, cpu_max, rss, rss_max}]`; `state` is the worst state in the bucket (1 running, 2 starting, 3 stopping, 4 stopped, 5 failed, 6 missing, 7 unknown) |
| `GET /api/metrics/sites/{domain}?range=` | `req`, `s2`..`s5`, `bytes`, `rt_p50`, `rt_p95`, `urt_p50`, `urt_p95` (seconds); 404 for a domain that is not a site |
| `GET /api/metrics/status` | file and WAL size, rows and oldest timestamp per tier, last flush, last error, rows dropped |
| `POST /api/metrics/clear` | deletes the whole history (audit-logged); alert rules are not touched |

`range` is one of `1h`, `6h`, `24h`, `7d`, `30d`, `90d` (default `1h`), or `from` and `to` as unix
seconds. Anything else is `400`. For a site, a minute with no requests while the daemon was recording
is `0`; a minute the daemon did not record is `null`. Percentiles are interpolated inside a fixed
histogram bin and are good to about 20%.

### Alert rules

Threshold alerts evaluated against the one-minute history after every flush. Rules and their state
live in `panel.db` (migration 0024), so a history reset does not touch them. An alert goes
`ok -> pending -> firing -> clearing -> ok`: it fires once its value has stayed past `threshold` for
`for_seconds`, and clears once it has stayed past `clear_threshold` for `clear_for_seconds`. A
notification (`metrics.threshold`) goes out on the fire and on the clear, and not in between.

| Route | Purpose |
|---|---|
| `GET /api/alerts` | `{rules: [{id, kind, target, threshold, clear_threshold, for_seconds, clear_for_seconds, severity, enabled, builtin, states: [{target, state, since, value}]}], kinds}` |
| `POST /api/alerts/rules` | create (201); omitted `target` is `*`, `enabled` defaults to true, `for_seconds` and `clear_for_seconds` to 300 |
| `PUT /api/alerts/rules/{id}` | change a rule; omitted fields keep their value. A built-in rule keeps its `kind` and `target` |
| `DELETE /api/alerts/rules/{id}` | delete (204); a built-in rule answers 409: switch it off instead |

`kind` is one of `disk_pct`, `mem_pct`, `swap_pct`, `load_per_core`, `cpu_pct`, `service_down` (the
value is the percent of the minute the service was not running), `cert_days` (days left, so it fires
at or below the threshold and `clear_threshold` must be at least `threshold`) and `site_5xx_pct`
(judged only on minutes with 50 or more requests). The host-wide kinds always target `host`. A bad
value answers `422` with the name of the field. Switching a firing rule off, deleting it, or losing the
target's data resolves its alerts first.
