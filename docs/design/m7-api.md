# M7 HTTP API contract (step 7.7, backend half)

Status: implemented in `internal/httpapi` (`handlers_sysinfo.go`, `handlers_logs.go`,
`handlers_cron.go`, `sse.go`) and wired in `cmd/panel/m7.go`. Verified only with fakes
and `go test`; nothing here has run on a host.

Every route needs a session cookie. Unsafe methods (POST, PUT, DELETE) need the CSRF
header and are written to the audit trail by the existing audit middleware. Errors use
the usual envelope `{"error": "<class>", "detail": "<sentence>", "field"?, "output"?}`.
A route whose dependency is not wired answers `503 {"error":"unavailable"}`.

Error classes added by M7: `not_allowed` (403), `control_failed` (502),
`source_unavailable` (409), `shell_not_allowed` (403). Existing classes are reused:
`not_found` (404), `bad_request` / `invalid_input` (400), `conflict` (409),
`internal_error` (500).

## Deviation from the brief: run detail path

The brief listed `GET /api/cron/runs/{runId}` and `GET /api/cron/{id}/runs`. Go's mux
refuses to register both (`/api/cron/runs/runs` matches either), so run detail is
**`GET /api/cron/{id}/runs/{runId}`**. A run is only addressable through its own job;
another job's run id answers 404.

## Sysinfo

### GET /api/sysinfo[?limit=N]

```json
{
  "interval_seconds": 5,
  "latest": { "...Snapshot..." },
  "history": [ { "...Snapshot..." } ]
}
```

`latest` is `null` and `history` is `[]` before the first sample. `history` is oldest
first, at most 720 entries (one hour at 5 s). `limit=N` keeps the newest N. A negative
or non-numeric limit is 400.

Snapshot (all keys always present):

```json
{
  "time": "2026-11-17T10:00:05Z",
  "rates": true,
  "cpu_percent": 12.5,
  "cpu_cores": [10.0, 15.0],
  "load": { "one": 0.5, "five": 0.4, "fifteen": 0.3, "running": 1, "total": 467 },
  "memory": {
    "total_bytes": 0, "available_bytes": 0, "used_bytes": 0, "free_bytes": 0,
    "buffers_bytes": 0, "cached_bytes": 0,
    "swap_total_bytes": 0, "swap_free_bytes": 0, "swap_used_bytes": 0
  },
  "uptime_seconds": 12345.6,
  "disks": [ { "mount": "/", "device": "/dev/sda1", "fstype": "ext4", "total_bytes": 0,
               "used_bytes": 0, "free_bytes": 0, "used_percent": 0.0 } ],
  "net": [ { "iface": "eth0", "rx_bytes_per_sec": 0, "tx_bytes_per_sec": 0,
             "rx_bytes": 0, "tx_bytes": 0 } ]
}
```

`rates` is `false` on the first sample after the daemon starts: CPU percentages and
network rates are differences between two readings and are `0` until there are two.

### GET /api/sysinfo/stream (server-sent events)

`Content-Type: text/event-stream`. Events:

- `event: snapshot`, `data: <Snapshot JSON>`: the newest snapshot immediately (if one
  exists), then one per sample (about every `interval_seconds`).
- Comment lines `: keep-alive` every 15 s, and `: waiting for the first sample` if
  there is no snapshot yet. EventSource ignores them.

The server stops and unsubscribes when the client disconnects. A client that reads too
slowly misses the oldest snapshots; key on `time`, a snapshot may repeat once right after
connecting.

### GET /api/services

```json
{ "services": [
  { "name": "panel", "kind": "systemd", "state": "running", "detail": "active",
    "actions": ["start", "restart"] },
  { "name": "panel-nginx", "kind": "container", "state": "running", "detail": "...",
    "actions": ["start", "stop", "restart"] },
  { "name": "app:mysite", "kind": "app", "state": "stopped", "detail": "STOPPED",
    "actions": ["start", "stop", "restart"] }
] }
```

`kind`: `systemd` | `container` | `app`. `state`: `running` | `starting` | `stopping` |
`stopped` | `failed` | `missing` | `unknown` (`unknown` rows carry the reason in
`detail`). `actions` is always an array (empty when a row cannot be controlled). Apps are
named `app:<name>`; the name in the control URL is the `name` field verbatim. Note the
older `GET /api/services/nginx` route still exists and is unrelated.

### POST /api/services/{name}/{action}

`action` is `start` | `stop` | `restart`. Body empty or `{}`. Success is `204`.

- 400 `bad_request`: unknown action, or a body with fields.
- 404 `not_found`: the name is not on the allowlist (nothing was run).
- 403 `not_allowed`: the panel refuses it (stopping the `panel` unit from its own UI).
- 502 `control_failed`: the command failed; `output` carries systemctl's or Docker's text
  (at most 1000 bytes), `detail` is generic.

The call runs to completion (60 s cap) even if the browser goes away. Audit: target
`service:<name>`, detail `{action}`.

## Logs

### GET /api/logs

```json
{ "sources": [ { "id": "site:a.test:access", "label": "a.test access", "kind": "site" } ] }
```

`kind`: `panel` | `audit` | `nginx` | `site` | `app` | `cron` | `docker`. Server paths and
container names are never in the response. Ids published by this build:

| id | what |
| --- | --- |
| `panel:audit` | the audit mirror, `/www/panel/logs/audit.log` |
| `panel:supervisord` | `/www/panel/logs/supervisord.log` |
| `docker:panel-nginx`, `docker:panel-php-<ver>` | container output (nginx's global error log goes to stderr, so it lives here) |
| `site:<domain>:access`, `site:<domain>:error` | per-site nginx logs |
| `site:<domain>:php` | PHP error log of a PHP site |
| `app:<name>` | the app's newest `out.log*` file |
| `cron:<job name>` | the job's newest run log (only jobs that have a run with a log) |

The panel daemon itself logs to the journal, so there is no `panel` file source; read it
with `journalctl -u panel`. Ids are opaque: list, then use what you got. A provider that
fails does not fail the list; its sources are just missing.

### GET /api/logs/{id}/tail[?lines=N&follow=1] (server-sent events)

`lines` defaults to 200 and is capped at 5000. `follow` accepts `1`/`true`/`0`/`false`.

- `event: line`, `data: {"line":"..."}`: one per log line, oldest first, backlog first.
- `event: end`, `data: {}`: a non-followed tail finished.
- `event: error`, `data: {"error":"<class>","detail":"..."}`: the stream failed after it
  had started; the stream then closes.
- Comment `: keep-alive` every 15 s.

Problems found before the first line are ordinary JSON errors, not a stream: 404
`not_found` (unknown id), 409 `source_unavailable` (the file does not exist yet, or Docker
is not reachable), 400 for bad `lines`/`follow`. A followed file that is truncated or
rotated is reopened from its start. A followed app log does not hop to the next
`out.log.<timestamp>` after a restart within one open stream; reconnect.

### GET /api/logs/{id}/search?q=...[&regex=1&ci=1&last_mb=16&limit=1000]

Chosen format: **NDJSON** (`Content-Type: application/x-ndjson`), one JSON object per
line, read with `fetch` and a stream reader, not EventSource (the response is finite, and
EventSource would reconnect when it ends).

```
{"type":"match","file":"a.test.access.log.1","line":42,"partial":false,"text":"..."}
{"type":"match", ...}
{"type":"done","stats":{"files":2,"bytesScanned":1048576,"matches":2,"limitHit":false,"windowCapped":false}}
```

- Matches are oldest first. `file` is a base name (rotated siblings are searched too).
  `line` is 1-based within the file; `partial` (omitted when false) means the file was
  scanned from the middle, so `line` counts from the first scanned line.
- The stream ends with exactly one `done` line, or one
  `{"type":"error","error":"internal_error","detail":"..."}` line if it failed part way.
- `q` is required. `regex=1` is RE2 syntax, `ci=1` is case-insensitive, `last_mb` (default
  16, max 1024) is how far back to look in uncompressed MB, `limit` (default 1000, max
  10000) is the match cap; `stats.limitHit` / `windowCapped` say when either bit.
- Before the first hit, failures are normal JSON errors: 400 (`invalid_input`, field `q`
  for an empty, oversized or uncompilable pattern; `bad_request` for a bad number or
  boolean), 404 unknown id, 409 `source_unavailable`.
- No heartbeat: a search that finds nothing sends nothing until it ends.

## Cron

Job (as returned by every job route):

```json
{
  "id": 1, "name": "backup", "schedule": "0 3 * * *", "kind": "argv", "user": "root",
  "argv": ["/usr/bin/true"], "script_path": "", "script_body": "", "workdir": "",
  "timeout_seconds": 3600, "overlap": "skip", "retention": 50, "enabled": true,
  "created_at": "...", "updated_at": "...",
  "next_run": "2026-11-18T03:00:00+07:00",
  "last_run": { "id": 11, "source": "schedule", "status": "succeeded", "exit_code": 0,
                "started_at": "...", "finished_at": "..." }
}
```

`argv`, `script_path`, `script_body`, `workdir` are omitted when empty. `next_run` is
`null` for a disabled job. `last_run` is `null` for a job that has never run, and carries
no output (fetch a run for that). `kind`: `argv` (argv[0] absolute, no shell) | `script`
(an existing executable at `script_path`) | `shell` (`script_body`, refused unless the
`cron.allow_shell` setting is on). `overlap`: `skip` | `queue` | `allow`.

Run:

```json
{ "id": 11, "job_id": 1, "source": "schedule|manual", "started_at": "...",
  "finished_at": "...", "exit_code": 0,
  "status": "running|succeeded|failed|timeout|canceled|interrupted|skipped",
  "log_path": "...", "output_tail": "last 8 KiB", "truncated": false, "error": "..." }
```

`finished_at`, `log_path`, `error` are omitted when empty/not finished.

| Route | Body | Success | Notes |
| --- | --- | --- | --- |
| `GET /api/cron` | | 200 `{"jobs":[Job]}` | ordered by name |
| `POST /api/cron` | job input | 201 Job | audit target `cron:<name>` |
| `GET /api/cron/{id}` | | 200 Job | |
| `PUT /api/cron/{id}` | job input | 200 Job | name cannot change; may be omitted |
| `DELETE /api/cron/{id}` | | 204 | also removes history and logs, cancels runs |
| `POST /api/cron/{id}/enable` | `{}` | 200 Job | |
| `POST /api/cron/{id}/disable` | `{}` | 200 Job | a disabled job can still be run by hand |
| `POST /api/cron/{id}/run` | `{}` | see below | |
| `GET /api/cron/{id}/runs[?limit=N]` | | 200 `{"runs":[Run]}` | newest first, default 50 |
| `GET /api/cron/{id}/runs/{runId}` | | 200 Run | 404 if the run is another job's |

Job input (POST and PUT), unknown fields are 400; PUT replaces the definition:

```json
{ "name": "backup", "schedule": "0 3 * * *", "kind": "argv", "user": "root",
  "argv": ["/usr/bin/true"], "script_path": "", "script_body": "", "workdir": "",
  "timeout_seconds": 0, "overlap": "", "retention": 0, "enabled": null }
```

Zero values mean the service default (timeout 3600 s, overlap `skip`, retention 50);
`enabled` omitted is on for create and unchanged for update. Schedule is five standard
cron fields.

`POST .../run` answers `{"outcome": "started|queued|skipped", "run": Run|null}`:
`202` for `started` and `queued` (it runs in the background; poll the run), `200` for
`skipped` (the overlap policy dropped it and recorded a skipped run). `run` is `null` for
`queued`, because a queued firing has no run until the running one ends.

Errors: 404 `not_found` (job or run), 409 `conflict` with `field:"name"` (duplicate),
400 `invalid_input` (validation, `detail` names the field), 403 `shell_not_allowed`.

## Settings added

| key | type | default | class |
| --- | --- | --- | --- |
| `cron.allow_shell` | bool | `false` | live (read on every create/update/run) |
| `logs.rotate.max_bytes` | int | `52428800` | restart required |
| `logs.rotate.keep` | int | `7` | restart required |

They appear in the existing settings catalogue under new groups `Cron` and `Logs`.
