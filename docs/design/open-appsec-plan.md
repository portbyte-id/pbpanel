# open-appsec as a second WAF engine: plan

Status: **plan only (2026-11-24).** Nothing is built.

Written after reading the current firewall (`internal/waf/*`, `internal/sites/waf.go`), the nginx
build recipe (`internal/nginx/builder.Dockerfile`, `builder.go`, `paths.go`) and the monitor
(`internal/wafmon/*`). The open-appsec facts below come from prior knowledge, not from a check of
the current release: **step 0 confirms or corrects every one of them before any code is written.**

Scope: Ubuntu first, as with the native nginx and PHP runtimes. EL (Rocky/RHEL) stays in **M9**.

## 1. Goal

A site's firewall offers two inspection engines:

- **ModSecurity + OWASP CRS** (what exists today): rule-based, paranoia level, rule-ID exclusions.
- **open-appsec**: Check Point's machine-learning engine (Apache-2.0), contextual scoring instead
  of signatures, fewer false positives without tuning.

The operator picks one per site. Everything else on the firewall page stays engine-independent:
rate limit, path blocklist, IP rules, the challenge page, the global firewall, the block page.

What does **not** change: the confedit apply pipeline (`nginx -t` first, roll back on rejection),
the `waf` marked block in site files, the `site_waf` table (the new fields live in its JSON), the
existing ModSecurity behaviour for every site that does not opt in.

## 2. How open-appsec fits nginx (to be confirmed in step 0)

Two pieces:

1. **The attachment**: an nginx dynamic module, `ngx_cp_attachment_module.so`, plus three shared
   libraries (`libosrc_shmem_ipc.so`, `libosrc_compression_utils.so`,
   `libosrc_nginx_attachment_util.so`). Like the ModSecurity connector, it must be compiled against
   the exact nginx release and configure flags, so it belongs in **our build recipe**, not in the
   vendor installer (whose prebuilt modules only match distro and nginx.org packages).
2. **The agent**: `cp-nano-agent` plus `cp-nano-http-transaction-handler`, running under systemd
   (`nano_agent.service`). nginx workers hand each request to it over shared memory in `/dev/shm`.
   If the agent is down the attachment **fails open**: traffic passes uninspected. It is installed
   by the official `open-appsec-install` script and managed with `open-appsec-ctl`.

The agent reads a declarative **local policy** (`/etc/cp/conf/local_policy.yaml`): a default
practice, per-host `specificRules`, mode (`detect-learn` / `prevent-learn` / `inactive`), a minimum
confidence for blocking, exceptions, trusted sources and log triggers. Applied with
`open-appsec-ctl --apply-policy`. The panel writes this file; it **never** connects the agent to
Check Point's cloud management (PRD: no telemetry).

## 3. Decisions

| # | Decision | Recommendation |
|---|---|---|
| D1 | How the agent is installed | **Ratified 2026-11-24: the official installer.** Pinned version, SHA-256 checked, local-policy mode only |
| D2 | One engine per site, or both at once | **One per site.** Two inspectors on one request doubles latency and makes a block hard to explain. Both modules may be loaded at the same time for different sites |
| D3 | Where the attachment comes from | **Compiled in the nginx build recipe**, pinned to the attachment tag that matches the pinned agent release. The vendor's prebuilt `.so` cannot match our custom build |
| D4 | What a site not using open-appsec gets once the module is loaded | **Default practice `inactive`**, so only sites listed in `specificRules` are inspected. If step 0 finds a per-server nginx directive that turns the attachment off, use that too |
| D5 | Outbound traffic from the agent | **Must be none** in local mode (updates, telemetry, model download). If step 0 finds calls that cannot be switched off, stop and bring it back to the operator before step 2 |
| D6 | Agent upgrades | **Panel-driven only**: a new pinned version in a panel release, installed by the same Store job. The agent's self-update stays off |

## 4. Steps

Each step ends green on the gate (`go test ./cmd/... ./internal/...`, CI golangci-lint on changed
code, vitest for UI steps) and is committed on its own.

### Step 0: spike on the Ubuntu sandbox (no product code)

On the current Ubuntu sandbox host, by hand, and write the findings into this file:

- Build the attachment against our nginx build in the builder image: the exact recipe (repo, tag,
  CMake flags, build deps, `nginx -V` input), the files it produces, and whether `ldd` is clean
  once bundled into the build's `lib/` the way ModSecurity's libraries are.
- Run the installer: the exact flags for a local-policy, no-cloud install; what it puts where;
  which user the agent runs as; whether it touches nginx files (it must not touch ours).
- Load the module under `panel-nginx.service` and check the unit's sandboxing lets workers (user
  `www`, 1500) reach the agent's shared memory and socket in `/dev/shm`.
- Confirm the policy schema version, the mode names, the confidence field, the exception syntax,
  and whether there is a per-server or per-location on/off directive.
- Find where security events are written and their format (JSON file, syslog, stdout), and that
  each event names the host so it can be routed to a site.
- Watch outbound connections (`ss -tp`, `tcpdump`) for an hour with the agent idle and under load (D5).
- Measure agent RSS and CPU idle and under a simple load test; measure added request latency.
- Kill the agent and confirm fail-open; reboot and confirm everything comes back.

Done when every item above has a recorded answer and D4/D5 are settled.

### Step 1: the attachment in the nginx build

- `builder.Dockerfile`: fetch the attachment source at a pinned tag, checked against a pinned
  SHA-256 (like the Core Rule Set), build it against the nginx tree, install the module into
  `$P/modules/` and its libraries into `$P/lib/` with the same `patchelf` RUNPATH treatment.
- `builder.go`: `openAppsecAttachmentVersion`, its digest, and placeholders. Add the module to the
  build's self-check (`test -f ...ngx_cp_attachment_module.so`, `ldd` clean) and to `BuildSource`.
- The CI package workflow uses `BuildSpec`, so prebuilt nginx picks the change up with no extra work.
- Test: the existing real-Docker build test asserts the module exists and `nginx -t` passes with a
  config that loads it (agent absent; the module must still load).

### Step 2: the agent install job and availability

- New package `internal/appsec`: the pinned installer version and SHA-256, the install and
  uninstall commands, the policy path, `open-appsec-ctl` wrappers, and `Status()` (installed,
  version, service active).
- A Store job `appsec.install` / `appsec.uninstall` through `internal/jobs`, with progress lines,
  that downloads the installer to a private temp dir, checks the digest, runs it in local mode,
  writes an empty (all-`inactive`) policy and applies it. Uninstall refuses while a site uses the engine.
- `waf.Availability` grows a second part: `OpenAppsec{Available, Reason, AgentVersion}`. Available
  means the serving nginx build has the module **and** the agent is installed and active.
- HTTP: the install/uninstall routes and the availability in the existing WAF status and overview
  JSON. `docs/API.md` updated.

### Step 3: the per-site engine in the data model

- `waf.Config` gains `Engine` (`modsecurity` | `open-appsec`, default `modsecurity`, so every
  stored row reads as it does today) and `OpenAppsec{Confidence, Exceptions}`.
- `Mode` stays shared: `detect` maps to `detect-learn`, `block` to `prevent-learn`.
- `Confidence`: `critical` | `high` | `medium` (the minimum confidence that blocks), default `high`.
- Exceptions: kind `path` (prefix), `source_ip` (CIDR) or `param` (name), each with a note; the same
  limits and character rules as today's exclusions. `Paranoia` and the CRS `Exclusions` are kept
  but only apply to ModSecurity.
- `UsesModSecurity()` becomes engine-aware; add `UsesOpenAppsec()`. `Check` refuses open-appsec
  when it is unavailable with a field-named 422, as ModSecurity does now.
- Table tests for normalize, defaults, old rows without `engine`, and the limits.

### Step 4: rendering and applying

- Site block: for open-appsec, no `modsecurity` lines; whatever per-server directive step 0 found.
  The challenge locations and the ACME path are exempt, as they are for ModSecurity today (a flagged
  puzzle request would lock a visitor out).
- Shared files: `waf/main/open-appsec.conf` with the `load_module` line, present while any site uses
  the engine, beside the existing `modsecurity.conf`.
- Policy: one `local_policy.yaml` rendered from every open-appsec site (default practice
  `inactive`, one `specificRules` entry per site and its server names/aliases, its mode,
  confidence, exceptions, and a log trigger). Rendered deterministically so an unchanged set
  writes nothing.
- Apply order in `SetWAF` / `SetWAFGlobal` / boot reconcile / site delete: confedit writes the
  nginx files and the policy in one change, `nginx -t`, reload, then `open-appsec-ctl --apply-policy`.
  If the agent rejects the policy, the whole change rolls back like an `nginx -t` failure. This
  probably means teaching confedit a second service kind with its own validate and apply commands.
- Renaming a site, adding an alias or deleting a site re-renders the policy.
- Tests: golden files for the block and the policy, plus a real-nginx integration test that the
  rendered config passes `nginx -t` with the module loaded.

### Step 5: events in the monitor

- Read the agent's security log (the location and format from step 0), route each event to its
  site by host, and feed the same `ingest` path the ModSecurity audit log uses, so the events page,
  per-site counters and top sources work unchanged. Tag each event with its engine.
- Map open-appsec fields to what the UI shows today: action, confidence, attack type, matched
  parameter, source IP, URI, request ID.
- Parser tests from real event lines captured in step 0.

### Step 6: UI

- `SiteWAF.tsx`: an engine picker above the mode. An engine that is not available is disabled,
  with the reason and a link to the Store to install it. ModSecurity shows paranoia and rule
  exclusions; open-appsec shows confidence and its exceptions.
- `WafProtection.tsx` / overview: an engine column.
- Store page: an open-appsec card (install, uninstall, version, agent state).
- `WafEvents.tsx`: the engine on each event and open-appsec's own fields.
- vitest for the picker, availability states and form validation; Playwright against a rebuilt
  `web/dist` (e2e drives the committed bundle).

### Step 7: host gate on the Ubuntu sandbox

A smoke script, in the style of the existing ones, run against a real host:

1. Fresh nginx build with the attachment; install the agent from the Store.
2. Enable open-appsec on one site in `detect`: an SQL-injection and an XSS request pass and show up
   as events; normal traffic does not.
3. Switch to `block`: the same requests get 403 with the panel's block page; normal traffic passes.
4. An exception for a path lets the attack request through on that path only.
5. A second site on ModSecurity and a third with the firewall off behave exactly as before.
6. Stop the agent: every site still serves (fail-open), the UI shows the agent down.
7. Reboot: nginx, the agent and the policy come back with no panel action.
8. Uninstall is refused while a site uses the engine; it works once none does, and the module line
   is gone from the config.
9. No outbound connections from the agent during the whole run (D5).

Record RSS, CPU and latency numbers next to the step 0 ones.

### Step 8: docs and handover

`docs/API.md`, the firewall section of the operator docs, a handover note in `docs/design/`, and
this file's status line.

## 5. Risks

- **Attachment/agent version skew.** The IPC protocol between the two is versioned; a new agent with
  an old module may refuse traffic or fail open silently. The pinned pair moves together, and
  availability checks the pair.
- **Vendor installer behaviour changes.** Pinning the version and digest guards the install; a new
  release is a deliberate panel change.
- **Footprint.** The agent is far heavier than ModSecurity (to be measured in step 0). The Store card
  says so before install.
- **Learning period.** The ML engine scores better after it has seen traffic; `detect` first is the
  recommended path, and the UI says so.
- **EL hosts.** Not covered until M9 (dnf builder, SELinux labels for `/dev/shm` and the agent).
