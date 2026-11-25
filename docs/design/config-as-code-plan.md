# Panel config as code: plan

Status: **plan, not started; decisions unratified.** Scope: Ubuntu first; EL (Rocky/RHEL) stays in
**M9**. No migration is needed (0027 stays free for a sibling unless D9 changes).

Written after reading the sites manager (`internal/sites/manager.go`, `includes.go`, `files.go`,
`php.go`, `waf.go`, `reconcile.go`), the firewall (`internal/firewall/service.go`, `model.go`,
`apply.go`), cron (`internal/cron/model.go`, `service.go`, `builtin.go`), apps
(`internal/apps/model.go`, `manager.go`), TLS (`internal/tls/store.go`), PHP
(`internal/php/manager.go`), nginx global (`internal/nginx/global.go`), the settings registry
(`internal/settings/keys.go`, `settings.go`), `internal/confedit/confedit.go`,
`internal/secretbox/secretbox.go`, `internal/jobs/jobs.go`, `internal/audit/audit.go`, every
migration up to `0021_waf_global.sql`, the CLI table (`cmd/panel/commands.go`) and the only
existing "WAF export", which is the AbuseIPDB CSV download (`internal/httpapi/handlers_wafmon.go`
route `GET /api/waf/events/abuseipdb`, test `handlers_waf_export_test.go`). Claims marked
**(assumption)** were not checked in code.

## 1. Goal

An operator can:

1. **Export** the panel's declarative state to one YAML document, `apiVersion: pbpanel/v1`, for the
   whole panel, one or more sites, or chosen sections. Secrets are left out by default, or included
   encrypted under a passphrase.
2. **Plan** a document against a host: a per-object list of `create` / `update` / `delete` /
   `replace` / `noop`, field-level diffs, blockers, and the follow-up actions import will queue
   (PHP builds, certificate issuance).
3. **Apply** it as a job, through the same service methods the UI calls, so every validator and the
   confedit rollback (`nginx -t` first, old files back on rejection) still hold. Re-applying the same
   document is a no-op. Deleting objects the document does not list (**prune**) is off by default.

Surfaces: `pbp config export|plan|apply` (the binary is `pbp`, see `cmd/panel/commands.go`; the
brief's `panel config` maps to this), a Config page in the UI, `/api/config/*` routes, and a
published JSON Schema for editor validation.

**Boundary with `docs/design/backups-plan.md`.** Backups are byte-exact copies of `panel.db` and the
web roots, restored on the same panel version, for disaster recovery. This feature is the
human-readable, diffable, portable layer: a document a person reviews in a pull request, applies to
a different host or a newer panel version, and partially (one site). It never carries web-root
content, the ACME account, issued private keys, job history, audit rows, sessions or the admin
password. To clone a host fully: restore web roots from a backup, then apply a config document.

What does **not** change: the confedit pipeline, the service methods (they are called, not
bypassed: no applier writes SQL to a definition table), the firewall's 60 s confirm-or-rollback,
the settings registry validators, the database-first rule (`docs/development-plan.md`).

## 2. Current state

| Area | Where it lives | Identity today | Write path the applier will call |
|---|---|---|---|
| Sites | `sites`, `site_aliases`, `site_proxy_routes` (0007, 0019, 0020) | `domain` PK, punycode | `sites.Manager.Create`, `Update(UpdateInput)`, `SwitchPHP`, `Delete` |
| PHP pool per site | `php_pools` (0008) JSON | `domain` PK | `sites.Manager.SetPool` |
| Site WAF | `site_waf` (0017) JSON `waf.Config` | `domain` PK | `sites.Manager.SetWAF` |
| Global WAF | `waf_global` (0021) singleton | `id = 1` | `sites.Manager.SetWAFGlobal` |
| nginx http settings | `nginx_settings` row `http` (0007) | `name` | `nginx.Global.SetSettings` |
| PHP versions | `php_versions`, `image_builds.extensions`, `image_pointers` | `version` | `php.Manager.StartPull` / `StartRebuild` (jobs) |
| PHP ini form | `php_settings` (0008) JSON | `version` | `php.Manager.SetSettings` |
| Settings | `settings` (0002), 30 registry keys | key name | `settings.Registry.Set` (validates) |
| Firewall rules | `fw_rules` (0010) | **autoincrement `id` only, no unique key** | `firewall.Service.AddRule/UpdateRule/DeleteRule`, then `Apply` |
| Block / allow list | `fw_blocklist`, `fw_allowlist` (0010, 0016) | `addr` UNIQUE | `AddBlock/DeleteBlock`, `AddAllow/DeleteAllow` |
| Firewall on/off | `fw_state.enabled` | singleton | `Enable` (needs operator confirm) |
| Cron | `cron_jobs` (0014, 0018) | `name` UNIQUE (`id` used by the API) | `cron.Service.Create/Update(id)/SetEnabled/Delete(id)` |
| Apps | `apps` (0011) | `name` UNIQUE; `port` unique when set | `apps.Manager.Create/Update/Delete(name)` |
| Certificates | `certs` (0012, 0013) + PEM on disk | `domain` UNIQUE | issue job (`POST /api/sites/{domain}/tls/issue`), upload, `tls/redirect` |
| DNS providers | `dns_providers`, credentials sealed by secretbox | `name` PK | `tls.Store.PutProvider/DeleteProvider` |
| ACME accounts | `acme_accounts`, key sealed | `directory_url` | not exported (a new host registers its own) |

Facts that shape the design:

- **Hand edits live only on disk.** `sites.Manager.WriteFile` / `WriteInclude`,
  `nginx.Global.WriteConf` and `php.Manager.WriteIni` write operator text through confedit but store
  nothing in SQLite. A DB-derived export cannot carry them; it can only detect them (render vs disk).
- **Secrets in the data:** settings with `Secret: true` (`tls.acme.eab_hmac_key`,
  `waf.turnstile.secret`, `internal/settings/keys.go`); DNS provider credentials; uploaded
  certificate private keys; `apps.env` is stored in clear JSON but routinely holds tokens.
- **Host identity settings:** `panel.port`, `panel.bind`, `panel.domain` describe this host, not
  the fleet. Copying them to host B can lock the operator out.
- **No local control channel.** The CLI opens the store directly (`openStore` in
  `cmd/panel/recovery.go`, `firewall.go`); confedit's apply locks are in-process `sync.Mutex`es
  (`confedit.go` `lock`), and `sites.Manager` has its own `mu`. A CLI apply in a second process
  while `panel.service` runs would race the daemon.
- **Audit is written by HTTP handlers**, not services (`audit.Writer` is wired in `serve.go`), so the
  applier writes its own audit entries.
- `prepareRoot` (`manager.go`) creates a web root and placeholder page **only if the root is
  missing**; `Delete` keeps the web root. Restoring web roots before or after apply is therefore safe.
- `sites.UpdateInput` cannot change `Kind` or `Root`: changing either is a delete and re-create.
- YAML: no YAML library in `go.mod` today (only lego, x/crypto, x/net, x/sys, modernc sqlite).

## 3. Decisions (unratified recommendations)

| # | Decision | Recommendation |
|---|---|---|
| D1 | Document format and library | **YAML, `go.yaml.in/yaml/v3`** (maintained fork of `gopkg.in/yaml.v3`), strict decode (`KnownFields(true)`). JSON is valid YAML, so the API also accepts JSON |
| D2 | Secret encryption | **age with an scrypt recipient (`filippo.io/age`)**: one armored blob in a top-level `secrets:` block; objects carry `secretRef` keys. Any operator can decrypt with the stock `age -d` tool; no home-made KDF/AEAD envelope. Per-value encryption rejected: one scrypt per value is seconds each, and identical plaintexts would still need care |
| D3 | Default secret handling | **Excluded.** Export writes `secretRef` placeholders and lists them under `status.secretsOmitted`; plan marks those objects `blocked: secret missing` unless the target already holds a value (then the value is kept) |
| D4 | Host-identity settings | `panel.port`, `panel.bind`, `panel.domain` **not exported** unless `--include-host`; never pruned |
| D5 | Firewall rule identity | **Natural key `(protocol, port_start, port_end, source)`**; `comment`/`enabled` are attributes. `AddRule`/`UpdateRule` start refusing a duplicate key (422). Existing duplicates make plan report `blocked: ambiguous` for that key; no migration |
| D6 | Firewall enable and confirm | Apply never enables a disabled firewall. If enabled and rules changed, apply calls `Service.Apply` and the job ends `waiting confirm` with the existing pending token; the UI banner / CLI prompt confirms. Unconfirmed: iptables rolls back, rows stay, the next plan shows them as applied-but-not-live **(assumption: `Revert` touches only the live ruleset)** |
| D7 | Failure semantics | **Stop at the first failed object.** Each object's confedit apply is atomic and rolls back; earlier objects stay. Re-applying converges. No cross-object transaction (that is what a backup snapshot is for) |
| D8 | Plan drift guard | Plan returns a **fingerprint** = SHA-256 of (normalised document, current-state snapshot of every object in scope). Apply re-plans and refuses on mismatch. CLI `apply` without a fingerprint prints the plan and asks |
| D9 | CLI apply transport | **In-process, daemon stopped.** `export` and `plan` are read-only and run any time. `apply` refuses while `systemctl is-active panel.service` succeeds, telling the operator to use the UI or stop the unit. Alternative (later): root-only Unix socket to the daemon |
| D10 | Hand-edited files | **Detected, not carried.** Export compares each managed file with its render and lists differences in `status.handEdited`; plan repeats the warning. Carrying raw nginx text would reference cert paths that do not exist yet on host B |
| D11 | `replace` (site kind/root change) | Refused unless `--allow-replace`; even then the old web root is kept (as `Delete` does) |
| D12 | App environment values | Treated **as secrets** (D3) by value: keys exported in clear, values through `secretRef` |
| D13 | Built-in cron jobs | Exported by name with only the operator-owned attributes (schedule, enabled, retention); never created or deleted by apply (`cron.EnsureDefaults` owns that) |
| D14 | JSON Schema source | **Hand-written** `docs/schema/pbpanel-v1.schema.json`, embedded and served; a reflection test fails when a document struct field has no schema property or vice versa. No schema-generator dependency |

## 4. Design

### 4.1 Document

```yaml
apiVersion: pbpanel/v1
kind: PanelConfig
metadata:
  exportedAt: "2026-11-25T10:00:00Z"
  panelVersion: v0.14.0          # informational; never gates import
  scope: { sections: [sites, waf, nginx, php, settings, apps, cron, firewall, tls] }  # or sites: [a.test]
spec:
  settings:                       # registry keys, operator-changed and defaults omitted
    tls.acme.email: ops@example.org
    waf.turnstile.secret: { secretRef: settings/waf.turnstile.secret }
  nginx: { http: { client_max_body_size: 64m, gzip: true } }   # nginx.HTTPSettings JSON shape
  waf:   { global: { bot_block: { enabled: true, scanners: true }, sensitive_files: true } }
  php:
    versions:
      - version: "8.3"
        extensions: [redis, intl, imagick]
        settings: { memory_limit: 256M, upload_max_filesize: 64M }
  sites:
    - domain: shop.example.org
      aliases: [www.shop.example.org]
      kind: php
      # root omitted = default /www/services/sites/<domain>
      php: { version: "8.3", rewrite: laravel, pool: { pm: dynamic, max_children: 20 } }
      proxyRoutes: [{ path: /api, target: "http://127.0.0.1:8081", stripPrefix: true }]
      waf: { engine: modsecurity, mode: block, paranoia: 1, rate_limit: { enabled: true, requests_per_second: 20, burst: 40 } }
      tls: { source: http01, redirectHttp: true }             # dns01: add provider: cf-main
  apps:
    - name: shop-api
      type: go
      command: /www/apps/shop-api/bin/server
      args: [--port, "8081"]
      workdir: /www/apps/shop-api
      user: www
      restart: always
      port: 8081
      env: { LOG_LEVEL: info, DB_PASSWORD: { secretRef: apps/shop-api/DB_PASSWORD } }
  cron:
    - { name: nightly-report, schedule: "0 2 * * *", kind: argv, user: www, argv: [/usr/bin/php, /www/x/report.php], timeoutSeconds: 600, overlap: skip, retention: 20, enabled: true }
    - { name: ssl-renew, builtin: ssl-renew, schedule: "17 3 * * *", enabled: true, retention: 30 }
  firewall:
    rules:     [{ protocol: tcp, ports: "8000-8010", source: 10.0.0.0/8, comment: internal, enabled: true }]
    blocklist: [{ addr: 203.0.113.9, comment: scraper }]
    allowlist: [{ addr: 198.51.100.0/24 }]
  tls:
    dnsProviders: [{ name: cf-main, kind: cloudflare, credentials: { secretRef: tls/dns/cf-main } }]
secrets: |                        # present only with --secrets encrypted
  -----BEGIN AGE ENCRYPTED FILE-----
  ...
  -----END AGE ENCRYPTED FILE-----
status:                           # written by export, ignored by plan/apply
  secretsOmitted: [settings/waf.turnstile.secret, apps/shop-api/DB_PASSWORD, tls/dns/cf-main]
  handEdited: [{ site: shop.example.org, file: site, note: "differs from render; not carried" }]
  notDeclared: [web roots, ACME account, issued private keys]
```

Field names inside an object reuse the existing JSON tags of the Go type where one exists
(`waf.Config`, `waf.Global`, `nginx.HTTPSettings`, `php.Settings`, `php.PoolSettings`), so a WAF
block is the same text the API takes. Document-level keys are camelCase. Lists are written in a
stable order (sites by domain, rules by key) so two exports of one state are byte-identical.

Decrypted, `secrets:` is a YAML map `secretRef -> value`. Uploaded certificates travel only in it:
`tls/cert/<domain>` holds `{cert_pem, key_pem}`.

### 4.2 Object kinds and identity

| Kind | Identity key | Notes |
|---|---|---|
| `setting` | key name | only registry keys; host keys per D4; `RestartRequired` keys reported as "restart needed" |
| `nginx.http` | singleton | |
| `waf.global` | singleton | |
| `php.version` | `version` | extensions are an attribute; a change is a runtime action, not a confedit write |
| `php.settings` | `version` | |
| `site` | `domain` (punycode on write, IDN accepted on read) | aliases, proxy routes (ordered), PHP version, rewrite are attributes |
| `site.pool`, `site.waf`, `site.tls` | `domain` | children: deleted with the site, never pruned alone except `site.waf` (set to off) |
| `app` | `name` | `port` collision with another app is a blocker |
| `cron` | `name` | the API addresses by `id`; the applier resolves name to id |
| `firewall.rule` | `(protocol, port_start, port_end, source)` (D5) | `ports: "80"` or `"8000-8010"` |
| `firewall.block`, `firewall.allow` | `addr` (normalised prefix) | |
| `tls.dnsProvider` | `name` | |

### 4.3 Plan semantics

- Input: document (decrypted secrets if a passphrase was given), flags `prune`, `allowReplace`.
- Snapshot the current state of every kind **in scope** through the same read methods export uses.
- Match by identity key; compare normalised values (run each object through its package's own
  normaliser, e.g. `firewall.normalizeRule`, `waf` normalize, so `/api/` and `/api` are equal).
- Actions: `create`, `update` (with `[{path, from, to}]`, secret values shown as `(secret)`),
  `noop`, `replace` (D11), `delete` (only with `prune`, only for kinds in `metadata.scope`; a
  per-site document never prunes other sites), `runtime` (queue a PHP build/rebuild), `issue`
  (certificate), `confirm` (firewall pending, D6).
- Blockers stop the whole apply: unknown field, unsupported `apiVersion`, missing secret with no
  existing value, ambiguous firewall key, PHP version absent and builds disabled, open-appsec engine
  with the agent missing (`sites.requireAppsec`), nginx not installed, app port taken, alias claimed
  by another site (`claimed` in `manager.go`).
- Warnings do not block: hand-edited files (D10), restart-class settings, certificate issuance that
  needs DNS pointing at this host.
- Output: `{fingerprint, changes[], blockers[], warnings[], followUps[]}`.

### 4.4 Apply order (one job, kind `config.apply`)

1. `settings` (web-root allow-list and ACME directory must exist before sites use them).
2. `tls.dnsProvider`. 3. `php.version` runtime actions: start builds, **wait** for their jobs,
   print their progress lines. 4. `php.settings`, `nginx.http`, `waf.global`.
5. Sites: create, then update, then `SwitchPHP`, `SetPool`, `SetWAF`; `replace`; `delete` last.
6. `app`, then `cron`. 7. Firewall rows, then `Service.Apply` if enabled (D6).
8. `site.tls`: upload carried certs; queue issue jobs for ACME certs; set redirect once a cert exists.

Every change writes one `audit.Entry` (actor, `config.apply`, object, result) and one job line.
On success emit `config.applied`, on failure `config.apply_failed`, through `notify.Emitter`
(`docs/design/notifications-plan.md`); a nil emitter is allowed until that package lands.
The passphrase and decrypted secrets are never written to job rows, audit rows or logs.

### 4.5 Not declarable, and what import does

| Thing | Why | Import does |
|---|---|---|
| Web-root content, app binaries | bytes, not config | creates an empty root with the placeholder page only if missing; points to the backups plan |
| ACME-issued certs and keys | bound to the account and CA | queues an issue job per site with `tls.source` http01/dns01; failure is a follow-up, the site serves HTTP |
| ACME account | per CA, per host | registered on first issue on the new host |
| PHP builds | runtime artefacts | `php.version` missing or extension set differs: queue `StartPull` / `StartRebuild`, wait |
| nginx runtime, ModSecurity, open-appsec agent, ClamAV | Store installs | blocker with the Store page as the fix |
| Hand-edited site/include/nginx.conf/php.ini text | disk only | warning (D10) |
| Admin user, sessions, SSH keys, audit, jobs, cron run history, Docker containers | identity or history | not touched |

### 4.6 HTTP API (all behind login and CSRF, documented in `docs/API.md`)

- `POST /api/config/export` body `{scope:{sections,sites}, secrets:"none"|"encrypted", passphrase?, includeHost?}`
  answers `application/yaml` with `Content-Disposition: attachment; filename=pbpanel-<date>.yaml`
  (the AbuseIPDB CSV handler's pattern). POST, so a passphrase never sits in a URL.
- `POST /api/config/plan` body `{document, passphrase?, prune?, allowReplace?}` answers the plan JSON.
- `POST /api/config/apply` same body plus `fingerprint`; 202 with the job; 409 on fingerprint mismatch.
- `GET /api/config/schema` answers the embedded JSON Schema.

### 4.7 CLI

```
pbp config export [--section s]... [--site d]... [--secrets none|encrypted] [--include-host] [-o file]
pbp config plan   -f file [--prune] [--allow-replace] [--json]
pbp config apply  -f file [--prune] [--allow-replace] [--fingerprint h] [--yes]
```

Passphrase from the TTY (`password_unix.go` reader) or `--passphrase-file`; never argv. Root only.
`apply` follows D9. Exit codes: 0 applied/no changes, 2 blockers, 1 failure.

### 4.8 Document versions

`internal/configdoc/migrate.go` holds an ordered list of `func(map[string]any) error` upgraders,
`v1 -> v2 -> ...`, run on the generic tree before strict decode. Export always writes the newest
version. A document newer than the binary is a blocker naming the version. Each schema version has
its own schema file; old ones stay published.

## 5. Steps

Each step ends green on the gate (`go test ./cmd/... ./internal/...` in WSL, CI golangci-lint on
changed code, vitest for UI steps) and is committed on its own.

### Step 1: document types, codec, versions, schema

- What: Go types for `PanelConfig`, strict YAML/JSON decode, deterministic encode, the upgrader
  chain (empty for v1), the JSON Schema and its embed. Add `go.yaml.in/yaml/v3` (D1).
- Files: `internal/configdoc/{doc.go,types.go,codec.go,migrate.go,schema.go}`,
  `docs/schema/pbpanel-v1.schema.json`.
- Tests: round-trip of the §4.1 example; unknown field rejected with its path; newer `apiVersion`
  rejected; reflection test for schema/struct drift (D14); encode is byte-stable.
- Done when: the example decodes, re-encodes identically and validates against the schema.

### Step 2: secrets envelope

- What: collect `secretRef`s, seal/open the `secrets:` block with age scrypt (D2); add `filippo.io/age`.
- Files: `internal/configdoc/secrets.go`.
- Tests: seal/open round trip; wrong passphrase is a clear error; a ref with no value is reported;
  plaintext never appears in the encoded document; decryptable with the `age` package reader alone.
- Done when: an encrypted export opens with only the passphrase.

### Step 3: export

- What: one collector per kind reading through the existing read methods (`sites.Manager.List`,
  `WAFConfig`, `Pool`, `php.Manager.List/Settings/CurrentExtensions`, `firewall.Service.Rules`,
  `cron.Service.List`, `apps.Manager.List`, `tls.Store.ListCerts/GetProvider`,
  `settings.Registry.Snapshot`); scope filters; default-root and default-value omission; D4, D12,
  D13; `status.handEdited` by comparing disk to render (`sites.Manager.managedFiles`,
  `nginx.Global.SettingsFiles`).
- Files: `internal/configplan/{export.go,collect_*.go}`.
- Tests: golden YAML from a seeded test store (one of each kind); `--site` export holds one site
  and no other section; secrets omitted vs encrypted; a hand-edited site file appears in status.
- Done when: the golden export matches and two exports of one state are identical.

### Step 4: plan

- What: snapshot, identity matching, normalised comparison, actions, blockers, warnings,
  follow-ups, fingerprint (D8). Pure over `(doc, snapshot)`; the snapshot comes from step 3's
  collectors.
- Files: `internal/configplan/{plan.go,identity.go,diff.go}`.
- Tests: table tests per kind for create/update/noop/delete-with-prune/no-delete-without-prune;
  re-plan after export is all `noop`; per-site doc never deletes other sites; ambiguous firewall key;
  site kind change is `replace` and blocked without `allowReplace`; fingerprint changes when one row
  changes.
- Done when: export-then-plan on the same store yields zero changes for every kind.

### Step 5: apply engine

- What: the ordered applier (§4.4) over the services, as a `jobs.Func`; per-object audit; stop at
  first failure (D7); notify emits; firewall duplicate refusal in `AddRule`/`UpdateRule` (D5) and
  the pending-confirm result (D6).
- Files: `internal/configplan/apply.go`, `internal/firewall/service.go` (duplicate check).
- Tests: apply on an empty store then re-plan is all `noop`; an injected `nginx -t` rejection on
  site 2 leaves site 1 applied, site 2 absent and the confedit rollback done; second apply is a no-op
  (no confedit writes, no reloads); firewall change ends with a pending token; audit rows written;
  no secret in job lines.
- Done when: the idempotency and failure tests pass against fake services and real confedit.

### Step 6: runtime and certificate follow-ups

- What: PHP build queuing and waiting inside the job; certificate upload from `secrets`; ACME issue
  jobs queued per site with result lines; redirect set after issuance.
- Files: `internal/configplan/followups.go`.
- Tests: missing version queues one build and sites wait for it; build failure fails the job before
  sites are touched; issue failure is a follow-up warning, not a job failure.
- Done when: a document naming PHP 8.4 on a store without it produces a build job then the sites.

### Step 7: HTTP API

- What: the four routes (§4.6); body size cap; passphrase handling.
- Files: `internal/httpapi/handlers_config.go`, `handlers_config_test.go`, `docs/API.md`.
- Tests: 401 without login; export attachment headers; plan JSON shape; apply 409 on stale
  fingerprint; 202 job; passphrase absent from job rows.
- Done when: the route tests pass and `docs/API.md` lists them.

### Step 8: CLI

- What: `config export`, `config plan`, `config apply` entries in `commandSet`; D9 guard; TTY
  passphrase; interactive firewall confirm.
- Files: `cmd/panel/config.go`, `cmd/panel/config_test.go`, `cmd/panel/commands.go`.
- Tests: argument validation; apply refuses with an active unit (fake runner); plan exit code 2 on
  blockers; export to stdout.
- Done when: `pbp config plan -f x.yaml` prints the same plan as the API.

### Step 9: UI page

- What: `/config` page: export form (sections, sites, secrets mode, passphrase), import box (paste or
  upload), plan table grouped by kind with expandable diffs, blockers and warnings, prune and
  replace toggles (off), apply button bound to the fingerprint, job progress via the existing jobs
  view, firewall confirm banner.
- Files: `web/src/pages/ConfigPage.tsx`, `ConfigPage.test.tsx`, `web/src/lib/config.ts`, router/nav.
- Tests: vitest for plan rendering, disabled apply on blockers, passphrase never stored.
- Done when: vitest green and `web/dist` rebuilt (e2e drives the committed dist).

### Step 10: host gate script

- `scripts/smoke/15-config-as-code.sh` running §6. Done when it passes on the sandbox.

## 6. Host gate (Ubuntu sandbox)

Host A (current Ubuntu sandbox) and host B (a fresh Ubuntu install with the panel and nginx from
the Store; re-confirm IPs before running).

1. On A, create: a static site, a PHP 8.3 site with Laravel rewrite and a pool tweak, a proxy site
   with two routes, WAF on one site, global WAF bot block, two firewall rules, one block entry, one
   app, one cron job, one DNS provider, a Turnstile secret.
2. `pbp config export --secrets encrypted -o a.yaml` on A; copy web roots with the backups tool.
3. On B: `pbp config plan -f a.yaml` lists creates only and a PHP 8.3 build follow-up; no blockers.
4. Apply from the B UI with the passphrase; confirm the firewall within 60 s.
5. Every site answers on B with `curl --resolve` (static page, `index.php`, both proxy routes); the
   WAF site blocks a CRS test request; `nginx -t` clean; app running; cron listed.
6. Re-plan on B: zero changes. Apply again: no confedit write, no reload (journal and nginx PID).
7. Edit one site in `a.yaml` (add an alias), plan shows one `update`, apply, alias serves.
8. Remove one site from the document: plan without `--prune` shows no delete; with it, one delete,
   web root kept.
9. Break a proxy target into an invalid value: apply fails at that site, earlier objects applied,
   nginx still serving, job and audit name the object.
10. Reboot B: boot reconcile keeps everything serving.

## 7. Risks

- **Partial apply** (D7): an operator may read a failed job as "nothing happened". The job summary
  lists applied, failed and not-attempted objects; re-plan shows the rest.
- **Firewall lock-out** on a remote host: mitigated by D6 (never enables, always confirm-or-rollback)
  and by never exporting host-identity settings (D4).
- **Hand edits silently lost** on the target: D10 warns at export and plan; nothing more in v1.
- **Secrets at rest**: the encrypted document is only as strong as the passphrase; scrypt work
  factor is age's default. Unencrypted exports with `secretRef`s are safe to commit.
- **Schema drift** as packages grow fields: the reflection test (D14) and the golden export fail
  first; every new field needs a schema entry and, if renamed, an upgrader.
- **Long PHP builds** inside one job: minutes per version; prebuilt runtimes setting shortens it.
- **Service methods built for the UI** may do more than the applier wants (e.g. `Create` writes a
  placeholder page). Accepted: calling them is what keeps validation honest.
- **CLI apply needs the daemon stopped** (D9): awkward for a fresh host; the UI path is the
  intended one there.

## 8. Out of scope

- Byte-exact backup and restore of `panel.db` and web roots (`backups-plan.md`).
- Continuous reconciliation / GitOps pull from a repository; drift alerts.
- Multi-host fleet management, templating, variables or overlays inside the document.
- Carrying raw hand-edited nginx or php.ini text (D10), Docker containers, SSH keys, users.
- EL hosts (M9).
