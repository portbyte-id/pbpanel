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
certificate from renewing. The challenge page's own endpoints are exempt the same
way: see `challenge-page.md`.

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

## The open-appsec engine

A site picks one inspection engine: ModSecurity + CRS (the default, everything
above) or Check Point's open-appsec. Rate limit, path blocklist, IP rules, the
challenge page and the block page are engine-independent.

**Tracking warning.** The open-appsec agent keeps a continuous TLS connection to
Check Point's cloud (`inext-agents.cloud.ngen.checkpoint.com`) even in local-policy
mode. No supported flag turns it off (`--offline_mode` does not), the traffic is on
the policy-apply path, and blocking it makes `--apply-policy` fail and the agent stop
inspecting. Using this engine can subject the host and its traffic to Check Point's
tracking. The Store card, the install confirmation, the site engine picker and the
firewall overview all say so. Installing requires acknowledging the exact text
(`appsec.Warning`). Evidence: `open-appsec-plan.md` §0.7 and §6.1.

| Piece | Where |
| --- | --- |
| Attachment (nginx module) | built into the nginx build, `modules/ngx_cp_attachment_module.so` |
| Agent | installed and removed from the Store (`appsec.install` / `appsec.uninstall` jobs); uninstall is refused while a site uses the engine |
| Module loader | `waf/main/open-appsec.conf`, present while any site uses the engine |
| Policy | `/etc/cp/conf/local_policy.yaml`, rendered from every open-appsec site, default practice `inactive` |
| Events | the agent's numbered security logs, read by `wafmon` and tagged with the engine |

`detect` maps to `detect-learn` and `block` to `prevent-learn`; confidence is the
minimum that blocks (`critical`, `high`, `medium`). Sites that do not use the engine
get `cp-nano-nginx-attachment off;`.

Limits worth knowing:

- **No path exceptions.** The agent's `urlPath` exception does not compile, so none
  are rendered. `hostName` compiles but is not offered.
- **A rejected policy is invisible.** `--apply-policy` exits 1 on success and failure
  and prints "New policy applied" either way. Only the absence of
  `Could not generate appsec local policy` in the orchestration log tells the truth
  (`open-appsec-step4-findings.md`).
- **One owner on the serving port.** A second nginx on `:80` makes results ambiguous.
- **Footprint.** The agent idles near 219 MB RSS and about 10% of a core (step 0).
- **Ubuntu only.** EL hosts are M9.

## Known limits

- The monitor starts from the tail of each log, so history before a daemon
  restart is only what the backfill reads (1 MiB of access log, 4 MiB of audit
  log per site).
- ModSecurity timestamps carry no zone; they are read as UTC, which holds when
  the container runs in UTC.
- Log rotation renames `*.log` and asks nginx to reopen; whether libmodsecurity
  reopens its audit file on that signal has not been checked.

## Global firewall

The WAF page's Protection and Block page tabs hold one firewall that applies to
every site (`waf.Global`, stored in the `waf_global` table, migration 0021). It sits
under each site's own settings and is rendered into every site's firewall block.

| Rule | Renders | Notes |
| --- | --- | --- |
| Bot blocking | `map` on the User-Agent in `waf/http/_global.conf`, `if` in the site block | Presets: scanners (on by default), AI crawlers, SEO crawlers; plus an empty-User-Agent switch and custom names |
| Dotfiles | `map` on the URI | Refuses `/.git`, `/.env`, `/.svn`; `/.well-known/` stays open |
| TRACE and TRACK | `map` on the method | nginx core already answers TRACE with 405; the rule is a backstop |
| Address deny list | `geo` plus `map` | Every site; a site's own allow list does not override it |
| Block page | `error_page` and an internal location | See below |

Each map is keyed on `$pbwaf_g_skip`: requests for the block page and for
`/.well-known/acme-challenge/` are never refused, so the page cannot refuse
itself. The maps are `volatile`, since nginx would otherwise keep a value across
the internal redirect to the page.

**Saving** re-renders every site's block in one confedit apply: `nginx -t` runs on
the result, and a rejection restores the files and the stored settings. A site
that has no firewall block gets one only when a global rule is on.

**Block page** is `<root>/panel/nginx/waf/errors/blocked.html`, served by nginx
with `ssi on`. The one directive allowed is `<!--#echo ... -->`, so a page cannot
include files or run commands. A 403 keeps its status and a 429 stays a 429. The
page also replaces any 403 the site's own application returns, while the global
block page is on.

Tests run real nginx: `TestGlobalFirewallParsesInNginx` runs `nginx -t` and
`TestGlobalFirewallBlocksInNginx` starts nginx and checks what a client gets.
They skip where nginx is not installed.

## Where requests come from

The WAF overview shows a world map shaded by requests per country, with tables of
countries and the busiest addresses. Countries come from the **DB-IP Lite country
database** (free, CC BY 4.0, no key). The panel downloads
`dbip-country-lite-YYYY-MM.csv.gz` from `download.db-ip.com` on request, checks
that it parses, and installs it under `<root>/panel/geoip/`. Lookups are local.
The page carries DB-IP's credit, which the license requires.

Routes: `GET /api/waf/sources?range=`, `GET /api/waf/geo`,
`POST /api/waf/geo/download`. Counts come from the monitor's buckets, which keep the
busiest addresses per interval, so a very busy host is counted as `untracked`
rather than guessed.

The map is lazy-loaded: `world-atlas` (Natural Earth 110 m, public domain) and
`d3-geo` project it.
