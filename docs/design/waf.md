# Web application firewall

Per-site protections for the panel's nginx, a traffic monitor, and an audit
viewer for ModSecurity events.

## What a site can switch on

| Control | Needs | Where it lives |
| --- | --- | --- |
| Rate limit (requests/s per client, burst, 429) | any nginx | `limit_req_zone` in `waf/http/<domain>.conf`, `limit_req` in the site's `waf` block |
| Path blocklist (prefix, case-insensitive, matched on the decoded `$uri`) | any nginx | a `map` in `waf/http/<domain>.conf`, `if (...) { return 403; }` in the block |
| IP rules (allow list, deny list, CIDR or single address) | any nginx | a `geo` in `waf/http/<domain>.conf`; the more specific entry wins, so deny beats allow |
| ModSecurity v3 + OWASP CRS, mode `off` / `detect` / `block`, paranoia 1-4 | the post-quantum nginx flavor | `modsecurity on;` in the block, rules in `waf/rules/<domain>.conf` |
| Rule exclusions (rule off site-wide, off under a path prefix, argument exempt from a rule) | ModSecurity | rendered into the rules file around the CRS include |

Certificate challenges (`/.well-known/acme-challenge/`) are never blocked by IP
rules and never inspected by ModSecurity, so an allow list cannot stop a
certificate from renewing.

**Not built: geo (country) rules.** They need a GeoIP database and an nginx
module for it, neither of which the image has. Address and network rules cover
what nginx can do on its own.

## Files

All under `<root>/panel/nginx/waf/`, inside the read-only config mount:

- `main/modsecurity.conf` loads the module. It exists while at least one site
  uses ModSecurity.
- `http/<domain>.conf` holds one site's zone and maps. It exists for every site
  and is a comment when nothing is switched on, so the block's `if` lines never
  read an undefined variable.
- `rules/<domain>.conf` is the site's ModSecurity configuration: engine mode,
  audit log (JSON, one event per line), CRS setup at the site's paranoia level,
  the CRS include, then the exclusions.

`panel-main.conf` and `panel-http.conf` carry `include .../waf/main/*.conf;` and
`include .../waf/http/*.conf;`. A glob that matches nothing is not an error.
The first firewall change on a host that predates the firewall rewrites those two
files in the same validated apply.

Each site file gets a `# >>> pb:waf` / `# <<< pb:waf` block, like the existing
domain, TLS and PHP blocks. A site file made before the block existed gets it,
inserted after its TLS block, the first time something is switched on; one whose
settings ask for nothing is never touched.

The audit log is `<root>/panel/logs/sites/<domain>.waf-audit.log`. The daemon
creates it owned by `www` before nginx is asked to write it.

## Applying a change

`PUT /api/sites/{domain}/waf` normalizes and validates the settings, stores them,
and sends the site file block, the site's firewall files, the module loader and
any stale settings file through `confedit.Apply` in one change: written, `nginx
-t` run on the result, nginx reloaded, health checked. A rejection puts every file
back and restores the stored settings, and the response carries the validator's
output. Everything that reaches a template is validated first (paths are limited
to letters, digits and `/ _ . ~ % -`; addresses are re-written as masked
prefixes), so templates never escape.

ModSecurity settings are refused with a field error on `mode` when the nginx
version that serves is not a `pqc-` one.

## Image

The post-quantum flavor bundles libmodsecurity 3.0.14, the nginx connector as a
dynamic module, and OWASP CRS 4.30.0 (release tarball pinned to its published
SHA-256) at `/etc/nginx/modsec/crs`. An image built before this change has no
CRS: rebuild it on the Nginx page.

## Monitoring

`internal/wafmon` tails every site's access and audit log by offset (about every
second), keeps ten-second buckets for an hour and rings of the latest 2000
requests and events, all in memory. New sites log in the `pbpanel` format
(combined plus `rt=` request time and `rid=` request id); older sites keep
`combined` and are read too. Routes: `GET /api/waf/traffic`, `/requests`,
`/events`, `/events/summary`, and `GET /api/waf/status` for every site's mode.
The UI polls every two seconds while the page is open.

## Known limits

- The monitor starts from the tail of each log, so history before a daemon
  restart is only what the backfill reads (1 MiB of access log, 4 MiB of audit
  log per site).
- ModSecurity timestamps carry no zone; they are read as UTC, which holds when
  the container runs in UTC.
- Log rotation renames `*.log` and asks nginx to reopen; whether libmodsecurity
  reopens its audit file on that signal has not been checked.
