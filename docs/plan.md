# Server Panel — Build Plan

Date: 2026-11-16. Status: agreed in interview, not started.

A single-server management panel: files, websites (static, reverse proxy, PHP), nginx and PHP
configuration, iptables firewall, and Go/Node.js apps. Ubuntu first, Rocky 9 (EL9) second.

## 1. Decisions

| # | Topic | Decision |
|---|-------|----------|
| 1 | Starting point | Greenfield repo, Go backend. The earlier `server-panel` project is a source of lessons only; no code is ported. |
| 2 | Privileges | One Go daemon running as root under a hardened systemd unit (see 3.2). |
| 3 | Accounts | A single admin account (username + password). No roles. |
| 4 | Panel access | The daemon serves HTTPS itself on a configurable port, with a self-signed certificate generated at install and replaceable from the panel. It never depends on the managed nginx. |
| 5 | Login protection | argon2id hashes, HttpOnly/Secure/SameSite=Strict session cookie, CSRF token, failed-login throttling with temporary lockout, idle session timeout, all sessions revoked on password change. No secret entrance path, no IP allowlist. |
| 6 | State | SQLite at `/www/panel/data/panel.db` (pure-Go driver, static binary). |
| 7 | Settings | Every setting lives in the database and is edited in the UI. No env vars, no config file. A root-only CLI on the same binary is the recovery path. |
| 8 | Runtime | nginx and php-fpm run in Docker from stage 1, through a deliberately thin wrapper. Image builds and version management come in stage 8. |
| 9 | Container network | nginx and PHP containers use host networking. PHP pools listen on Unix sockets only. |
| 10 | Firewall | iptables enforced. ufw / firewalld are stopped and masked. Rules live in panel-owned chains; chains the panel does not own are never flushed. |
| 11 | Firewall scope | Default-deny inbound, open outbound. Port / range / protocol / optional source CIDR rules plus an IP blocklist. SSH and panel port rules are built in and undeletable. Every apply has a 60 s confirm-or-rollback timer. IPv4 and IPv6 together. |
| 12 | Site config | Generated once from the create form, then the operator owns the file as raw text. |
| 13 | Later changes | Panel actions only rewrite small panel-owned include files pulled in by the site file. A missing include line shows that feature as "detached". |
| 14 | Site TLS | Upload, ACME HTTP-01, and ACME DNS-01 (wildcards), with automatic renewal. |
| 15 | PHP layout | One container per PHP version, one FPM pool per site. |
| 16 | PHP images | Built locally by the panel with selectable extensions — deferred to stage 8. Until then, official images as-is. nginx comes first. |
| 17 | Supervisor | [ochinchina/supervisord](https://github.com/ochinchina/supervisord) as its own systemd service. |
| 18 | App scope | An app is a command to run. No build step, no Git deploy. |
| 19 | Runtimes | nvm under `/www/panel/nvm`; the operator installs Node versions from a Runtimes page. Go apps are prebuilt binaries; no Go toolchain is managed. |
| 20 | File manager | Browse, create, rename, move, copy, delete, text editor, chunked upload, download (folders as archive), chmod/chown, zip and tar.gz create/extract, recycle bin. |
| 21 | File guardrails | Small hard-deny list plus typed confirmation for recursive destructive operations (see 3.7). |
| 22 | Paths | Panel internals in `/www/panel`. Sites in `/www/services/sites/<domain>` (the folder is the web root). Apps default to `/www/services/apps/<name>`. Site logs in `/www/panel/logs/sites`. |
| 23 | Ownership | One shared `www` user with a fixed UID/GID for nginx workers, all PHP pools, and apps by default. |
| 24 | Extras in scope | Dashboard with service status, log viewer with audit log, cron jobs. |
| 25 | Out of scope | Databases, web terminal, multi-user/roles, Git deploy, build pipelines, Go toolchains, multi-server. |
| 26 | Install | `panel install` on a copied static binary does everything. `panel upgrade` swaps the binary. |
| 27 | Frontend | React + TypeScript + Vite + Tailwind + shadcn/ui, TanStack Router and Query, CodeMirror 6. Embedded in the binary with `go:embed`. REST/JSON API, Server-Sent Events for live output. |
| 28 | Global config | A common-settings form plus a raw editor for nginx.conf, php.ini and pool settings. |
| 29 | Done means | Go and frontend tests green, plus a scripted smoke run on a real Ubuntu VM. EL9 is verified once, in stage 9. |

## 2. Filesystem layout

```
/www
├── panel/
│   ├── bin/                 panel, supervisord
│   ├── data/                panel.db, secret.key (0600)
│   ├── nginx/
│   │   ├── nginx.conf       generated once, then raw
│   │   ├── panel-http.conf  written by the common-settings form
│   │   ├── sites/<domain>.conf            generated once, then raw
│   │   └── includes/<domain>/             panel-owned
│   │       ├── listen.conf  listen + server_name
│   │       ├── tls.conf     certificate + redirect
│   │       ├── acme.conf    /.well-known/acme-challenge
│   │       ├── php.conf     fastcgi_pass to the site's pool socket
│   │       └── proxy.conf   upstream target
│   ├── php/<version>/       php.ini, conf.d/zz-panel.ini, pool.d/<domain>.conf
│   ├── run/                 PHP sockets (shared with the nginx container)
│   ├── ssl/                 panel cert, site certs, ACME account, HTTP-01 webroot
│   ├── supervisor/          supervisord.conf, conf.d/<app>.conf
│   ├── nvm/
│   ├── history/             last 10 versions of every edited config
│   ├── logs/                panel, audit, sites/, apps/, cron/
│   ├── uploads/             in-progress chunked uploads
│   └── trash/
└── services/
    ├── sites/<domain>/      web root
    └── apps/<name>/
```

Outside `/www` (system files only): `/etc/systemd/system/panel.service`,
`panel-supervisord.service`, `panel-firewall.service`, and the `/usr/local/bin/pbp`
symlink that puts the binary on PATH.

Every bind mount uses the same absolute path inside the container as on the host, so a path in a
config file means the same thing in both places and never changes between stages.

## 3. Architecture

### 3.1 Backend

One Go module, one binary, three roles: `panel serve` (daemon), `panel install|upgrade`,
and recovery commands (`panel info`, `panel reset-password`, `panel set <key> <value>`,
`panel firewall restore|disable`).

```
cmd/panel/            entry point and CLI
internal/
  store/              SQLite, migrations
  settings/           typed settings registry (default, validation, restart-required flag)
  auth/               password, sessions, throttling, CSRF
  httpapi/            router, middleware, handlers, embedded SPA
  jobs/               background jobs with persisted output, SSE stream
  audit/              who did what, when
  confedit/           snapshot -> write -> validate -> reload -> rollback, version history
  docker/             thin Docker Engine API client over the Unix socket
  nginx/  php/        templates, includes, validation commands
  sites/              site model for the three kinds
  tls/                upload, ACME (lego), renewal
  fm/                 file manager, uploads, archives, trash
  firewall/           rule model, iptables-restore rendering, confirm timer
  apps/  runtimes/    supervisord programs, nvm
  cron/               in-process scheduler
  sysinfo/            metrics and service status
  osdep/              everything distro-specific (packages, users, SELinux, firewall services)
web/                  React SPA
scripts/smoke/        one smoke script per stage
```

Rules that hold everywhere:

- **Database first.** A change is committed to SQLite, then rendered to disk. Rendering is
  idempotent and can be re-run at boot.
- **One apply pipeline.** Every config write goes through `confedit`: snapshot the current file,
  write the new one atomically, run the validator (`nginx -t`, `php-fpm -t`), reload, check the
  service is still up. Any failure restores the snapshot and returns the validator's output to
  the UI. The last 10 versions are kept for manual restore.
- **No shell strings.** External commands are run with argument vectors; user input is never
  interpolated into a shell line.
- **Distro logic in one place.** Only `osdep` knows about apt vs dnf, ufw vs firewalld, SELinux.

### 3.2 systemd hardening

The unit runs as root with `NoNewPrivileges`, `LockPersonality`, `RestrictRealtime`,
`ProtectClock`, `ProtectHostname`, `SystemCallArchitectures=native`, `MemoryDenyWriteExecute`,
`Restart=always`. Filesystem protections (`ProtectSystem`, `ProtectHome`, `ReadOnlyPaths`)
stay off because the file manager must write anywhere. This narrows a few escalation paths; it
does not contain a compromised session. Authentication is the real boundary.

### 3.3 Sites and nginx

Create-site form: kind (static / reverse proxy / PHP), domains, and per kind a root, a proxy
target, or a PHP version. The panel writes the site file once from a template, plus its include
files. After that:

- The **site file** is edited as raw text in the panel (CodeMirror, validate, history, rollback).
- **Panel actions** (domains, TLS, PHP version, proxy target) rewrite only their include file.
- If the operator removes an `include` line, the panel detects it and marks that feature
  "detached" rather than editing the raw file. "Regenerate site file" is offered as an explicit,
  confirmed action.

Global nginx: `nginx.conf` is generated once, then raw. The common-settings form (worker
processes, body size, gzip, timeouts) writes `panel-http.conf`. nginx rejects most duplicated
directives, so the generated `nginx.conf` does not set those directives itself; if the operator
adds one by hand, `nginx -t` fails and the panel reports the duplicate.

### 3.4 PHP

One container per PHP version, host network, running as `www`. Each PHP site has a pool file
with its own socket in `/www/panel/run`. php.ini per version: raw editor, plus a form (memory
limit, upload sizes, execution time, timezone) that writes `conf.d/zz-panel.ini`, which loads
last and wins. Pool form: process manager mode and child limits. Switching a site's version moves
its pool file and rewrites its `php.conf` include in one apply.

Until stage 8, containers use the official `php:<ver>-fpm` images unchanged, which is enough to
prove config editing but not to run apps that need extra extensions.

### 3.5 Firewall

- Chains `PANEL-INPUT` (filter, v4 and v6) hooked from `INPUT`. The chain ends in `DROP`; the
  `INPUT` policy is left alone, so unhooking the chain reopens the server.
- Built-in rules: established/related, loopback, ICMP essentials, the SSH port (read from sshd
  at apply time), the panel port.
- Applied atomically with `iptables-restore --noflush`. `panel-firewall.service` restores the
  chains at boot from the database.
- Apply flow: new rules go live, the UI asks "keep?", and the previous ruleset returns after
  60 s without confirmation.
- ufw / firewalld are stopped and masked **when the firewall stage is first enabled**, not at
  install during stages 0–3. Otherwise the server would sit unfiltered until stage 4 ships.
- Known limit: containers the operator starts by hand with published ports bypass `INPUT`. The
  panel's own containers use host networking and are not affected. Documented, not solved.

### 3.6 Apps

supervisord runs from `/www/panel/bin` under `panel-supervisord.service`, so apps survive panel
restarts and upgrades. The panel writes one program file per app (command, directory,
environment, user, autostart, restart policy, log files under `/www/panel/logs/apps`) and
controls it through supervisord's RPC on a Unix socket. For Node apps the panel resolves the
absolute `node` path for the chosen nvm version. "Create reverse-proxy site for this app" links
an app's port to a new site.

### 3.7 File manager

- Refused outright: delete, move, or recursive chmod/chown of `/` and the top-level system
  directories themselves (`/etc`, `/usr`, `/bin`, `/sbin`, `/lib*`, `/boot`, `/var`, `/www/panel`
  and similar), and any write under `/proc`, `/sys`, `/dev`. Their contents stay editable.
- Recursive delete and recursive chmod/chown elsewhere require typing the folder name.
- Symlinks are never followed during recursive operations. Archive extraction rejects entries
  that resolve outside the target directory.
- Delete moves to `/www/panel/trash` with restore. Cross-filesystem deletes are copied then
  removed; a size limit and a retention period are panel settings.
- The text editor refuses files above a configurable size and binary files.

### 3.8 TLS

Built on the lego ACME library. HTTP-01 uses a webroot under `/www/panel/ssl` served through each
site's `acme.conf` include. DNS-01 starts with one provider (Cloudflare) behind an interface that
admits more; provider credentials are encrypted in the database with `data/secret.key`. A daily
job renews certificates inside 30 days of expiry and reports failures on the dashboard.

## 4. Stages

Each stage ends with tests green and its `scripts/smoke/<stage>.sh` passing on a real Ubuntu VM.

| Stage | Scope | Smoke proves |
|-------|-------|--------------|
| 0 Foundation | Repo, daemon, SQLite and migrations, settings registry and Settings page, login and sessions, jobs + SSE, audit log, UI shell, `panel install` for Ubuntu, recovery CLI, systemd unit. | Fresh VM → install → log in → change port from UI → panel returns on the new port → `reset-password` works. |
| 1 Nginx and sites | Docker client, nginx container, `confedit` pipeline, nginx.conf form + raw editor, static and reverse-proxy sites, include files, detached detection. | Create both site kinds and curl them; a broken edit is rejected and rolled back with nginx still serving. |
| 2 PHP config | PHP containers on official images, php.ini form + raw editor, pools, PHP sites, version switch. | `phpinfo()` served; a form change is visible in it; switch version; bad ini rolled back. |
| 3 File manager | Everything in decision 20 and 3.7. | Round-trip a 2 GB upload with an interrupted connection; deny list holds; trash restore; hostile archive rejected. |
| 4 Firewall | Decisions 10–11 and 3.5. | Port closed by default, open after a rule, rollback fires without confirmation, rules survive reboot, Docker chains intact. |
| 5 Apps | supervisord, nvm, Runtimes page, app CRUD, logs, link to proxy site. | Go binary and Node app run, restart after `kill -9`, survive a panel restart, reachable through a proxy site. |
| 6 TLS | Upload, HTTP-01, DNS-01, renewal, panel certificate replacement. | Certificates issued against the Let's Encrypt staging endpoint by both challenge types; forced renewal works. |
| 7 Dashboard, logs, cron | Metrics, service status and controls, log tail and search, cron jobs. | Stopped service shows as stopped and restarts from the UI; a cron job runs on schedule and its output is recorded. |
| 8 Docker management | Local PHP image builds with extension selection, PHP version install/remove, nginx image update with rollback. | Build a PHP image with extra extensions and swap it under a live site; failed build leaves the old container serving. |
| 9 EL9 pass | Rocky 9 support in `osdep`: dnf, Docker repo, firewalld, SELinux labels on bind mounts and sockets. | Every earlier smoke script passes on Rocky 9 with SELinux enforcing. |

## 5. Risks and things to verify early

1. **`www` inside official images.** nginx's `user` directive needs a name that exists in the
   container, and the fixed host UID must match. Stage 1 starts with a spike: either a two-line
   derived image that adds `www`, or a mounted passwd entry. This is a trivial local build and
   does not pull stage 8 forward.
2. **supervisord RPC surface.** The Go supervisord implements a subset of the original's
   XML-RPC API. Stage 5 starts by confirming start/stop/status/reload-config and log access; the
   fallback is its bundled `ctl` command.
3. **Root exposure.** One password guards root on a public port, with no IP allowlist or second
   factor by decision. Throttling and session handling get a dedicated security review at the end
   of stage 0, and again after the file manager.
4. **Raw site files.** Operators can write configs the panel cannot interpret. The panel never
   parses the raw file beyond checking that include lines are present.
5. **Host networking for PHP.** PHP code can reach every loopback service on the host. Accepted
   in exchange for outbound access and a host database at `127.0.0.1`.
6. **Shared `www` user.** One compromised PHP site can read and write the others. Accepted.
7. **EL9 late.** SELinux and firewalld issues will surface only in stage 9. `osdep` exists from
   stage 0 so the pass changes one package, not the codebase.
