# open-appsec as a second WAF engine: plan

Status: **plan + step 0 spike complete (2026-11-25); D5 decided (accept, marked as tracking);
step 4 complete (rendering and applying); steps 1-3, 5-8 in progress.** Step 4's host findings, which
correct §0.5 and §6.1, are in `open-appsec-step4-findings.md` beside this file.

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

## 2. How open-appsec fits nginx (confirmed in step 0; see §6)

Two pieces:

1. **The attachment**: an nginx dynamic module. The vendor ships it as `libngx_module.so`, renamed
   to `ngx_cp_attachment_module.so` on install, **plus six** shared libraries
   (`libshmem_ipc_2.so`, `libosrc_shmem_ipc.so`, `libosrc_compression_utils.so`,
   `libosrc_nginx_attachment_util.so`, `libnano_attachment.so`, `libnano_attachment_util.so`), not
   three as this plan first said. Its source lives in the **separate** `openappsec/attachment`
   repository, which has **no tags and no releases** — only `main` and dated dev branches. It is
   CMake-built against a configured nginx tree (it reads the tree's `include_paths.mk` and
   `cc_flags.mk`), so it belongs in **our build recipe**, not in the vendor installer.
2. **The agent**: `cp-nano-agent` (installed by `install-cp-nano-agent.sh`) plus
   `install-cp-nano-service-http-transaction-handler.sh` and
   `install-cp-nano-attachment-registration-manager.sh`. There is **no `cp-nano-http-transaction-handler`
   package**; that is the *binary* the second script installs. The one systemd unit is
   `nano_agent.service`, which starts `/etc/cp/watchdog/cp-nano-watchdog`; the four real services run
   **as root**, not as a dedicated user. nginx workers talk to them over **POSIX shared-memory files
   in `/dev/shm`** (`__cp_nano_{rx,tx}_shared_memory_<n>__`, plus Unix sockets under
   `/dev/shm/check-point/`). If the agent is down the attachment **fails open**; failure is logged as
   `IPC is uninitialized. Skipping inspection of current request.` The vendor's `open-appsec-install`
   wrapper manages it, as does `/etc/cp/scripts/open-appsec-ctl.sh`.

The agent reads a declarative **local policy** (`/etc/cp/conf/local_policy.yaml`), schema
`v1beta2`: a default policy, per-host `specificRules`, mode (`detect-learn` / `prevent-learn` /
`inactive`), `minimumConfidence` (`medium|high|critical`), named `exceptions`, trusted sources and
log triggers. Applied with `open-appsec-ctl --apply-policy`. The panel writes this file; it **never**
connects the agent to Check Point's cloud management — but see D5: the agent connects to the cloud
anyway unless something blocks it.

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

## 6. Step 0 findings

Spike on `root@10.10.11.106` (Ubuntu 26.04.1 LTS, kernel 7.0.0-43, 4 vCPU, 3.3 GB RAM, Docker
29.9.3), 2026-11-24/25. Serving nginx **1.30.7**, built by gcc 15.2.0 with OpenSSL 3.5.9, prefix
`/www/panel/nginx/versions/20261119214848-e0757d`. `panel.service` and `panel-nginx.service` were
active throughout. **No product code was changed.** The throwaway build script lived at
`/tmp/oa/build-attachment*.sh` on the host; `/tmp` was wiped by the reboot in 0.9, so the recipe is
transcribed in full below instead of being kept as a file.

> **D5 is a hard stop. Read 0.7 before step 2.** In local-policy mode the agent still opens and
> holds **TLS connections to Check Point's cloud** (`inext-agents.cloud.ngen.checkpoint.com`, AWS
> Global Accelerator, `75.2.123.205` / `99.83.172.252` on 443) from three of its four services.
> No supported flag turns this off: `--offline_mode` does not, and blocking it makes
> `--apply-policy` fail. Step 2 must not be built until the operator decides (see D5).

### 0.1 The plan's open-appsec facts — confirmed or corrected

**The attachment is `libngx_module.so` in the vendor's own packaging; `ngx_cp_attachment_module.so`
is the name the installer renames it to.** The prebuilt package for this host's nginx release:

```
$ V=ngx_module_1.30.7-1-resolute
$ curl -s -o pkg.tar.gz -w "%{http_code}" \
    https://downloads.openappsec.io/packages/nginx-attachment/x86_64/ubuntu/resolute/$V.tar.gz
200
$ sha256sum pkg.tar.gz
82ac82b2b490e6eaa3172c11c4e0f6c1c491740829459ab8eedc8e3430553519  pkg.tar.gz
$ tar -tzvf pkg.tar.gz
ngx_module_1.30.7-1-resolute/libosrc_compression_utils.so
ngx_module_1.30.7-1-resolute/libosrc_nginx_attachment_util.so
ngx_module_1.30.7-1-resolute/libngx_module.so
ngx_module_1.30.7-1-resolute/libnano_attachment.so
ngx_module_1.30.7-1-resolute/libshmem_ipc_2.so
ngx_module_1.30.7-1-resolute/libnano_attachment_util.so
ngx_module_1.30.7-1-resolute/libosrc_shmem_ipc.so
```

**Seven files, not four**: one module and **six** libraries. The plan's §2 named three libraries and
missed `libshmem_ipc_2.so`, `libnano_attachment.so` and `libnano_attachment_util.so`. The installer
renames the module at copy time:

```
$ grep -n 'libngx_module.so' open-appsec-install
  tar -xzf .../${NGX_FULL_VERSION}.tar.gz -C ${OPEN_APPSEC_DOWNLOAD_PATH}
  mv .../${NGX_FULL_VERSION}/libngx_module.so .../${NGX_FULL_VERSION}/ngx_cp_attachment_module.so
```

**Source repo: `openappsec/attachment`, NOT `openappsec/openappsec`, and it has no tags and no
releases.**

```
$ curl -s https://api.github.com/repos/openappsec/attachment/tags     | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))'
0
$ curl -s https://api.github.com/repos/openappsec/attachment/releases | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))'
0
$ git clone --depth 1 --branch 1.1.36 https://github.com/openappsec/attachment.git
fatal: Remote branch 1.1.36 not found in upstream origin
```

The `1.1.36` tag exists only in `openappsec/openappsec`; the attachment repo tracks it through
`main` (built at `4c88f6389e301fd1958b0448f0e0ce01888a20c5`, 2026-08-20) and dated dev branches.
**This breaks D3's "pinned to the attachment tag that matches the pinned agent release": there is no
such tag.** Step 1 must pin a **commit SHA**, not a tag.

**The attachment must be compiled against a configured nginx tree.** Its
`attachments/nginx/ngx_module/CMakeLists.txt` does
`file(READ ${CMAKE_INSTALL_PREFIX}/nginx-src/include_paths.mk ...)` and `.../cc_flags.mk`, which the
vendor generates by re-running our `./configure` line against an nginx tarball it downloads by
release number. So the module is shaped by **our release and our flag set**, but what it actually
consumes is the `-I` include list and `-O2` — not our binary. The pair that matters is
**module commit ↔ agent release**, because the IPC protocol is versioned.

**Agent package names, unit, and `open-appsec-ctl` subcommands** (all observed on the host):

```
$ ls /tmp/oa/agent/openappsec/
install-cp-agent-intelligence-service.sh        install-cp-nano-agent.sh
install-cp-crowdsec-aux.sh                      install-cp-nano-attachment-registration-manager.sh
install-cp-nano-agent-cache.sh                  install-cp-nano-service-http-transaction-handler.sh
install-cp-nano-central-nginx-manager.sh        install-cp-nano-service-prometheus.sh
open-appsec-nginx-conf-collector

$ systemctl list-unit-files | grep -i nano
nano_agent.service   enabled   enabled
$ systemctl cat nano_agent.service | head -12
[Unit]
Description=CP_Nano_Linux_Agent
Before=network-pre.target
[Service]
ExecStart=/etc/cp/watchdog/cp-nano-watchdog
ExecStartPost=/etc/cp/watchdog/wait-for-networking-inspection-modules.sh
Environment="FILESYSTEM_PATH=/etc/cp"

$ /etc/cp/scripts/open-appsec-ctl.sh --help | grep -oE '\-[a-z]+,'
-d -s -r -q -rs -qs -u -vp -ep -ap -lp -vl
```

Corrections to the plan: there is **no `cp-nano-http-transaction-handler` package** (that is the
binary name); there **is** a third component, `cp-nano-attachment-registration-manager`, which the
README requires and which the attachment cannot register without; the unit is `nano_agent.service`
(exact); `open-appsec-ctl` is `/etc/cp/scripts/open-appsec-ctl.sh` with the flags above.

### 0.2 Building the attachment against OUR nginx build

**Inputs.** `nginx -V` from `/www/panel/nginx/current/sbin/nginx` is the exact configure line (it is
the panel's `builder.Dockerfile` line with its placeholders resolved). The flags that matter to the
attachment are `--with-compat` and the OpenSSL source build, both already in the recipe.

**Source.** `https://github.com/openappsec/attachment` at commit
`4c88f6389e301fd1958b0448f0e0ce01888a20c5`.

**Build dependencies** (Ubuntu 26.04 base, beyond the panel's existing list):

```
build-essential cmake git libssl-dev libpcre2-dev zlib1g-dev libbrotli-dev \
libxml2-dev libmaxminddb-dev libhiredis-dev curl ca-certificates pkg-config wget file perl \
dos2unix python3
```

`dos2unix` and `python3` are **vendor assumptions the builder image lacks**: its packaging step calls
`python3 core/version/build_version_vars_h.py`, and CMake fails with
`CMake Error at nodes/packaging.cmake:2 (string): string sub-command STRIP requires two arguments`
when `python3` is missing. Both must go into `builder.Dockerfile` for step 1.

**Two real traps for step 1:**

1. **CMake 4 rejects the project's `cmake_minimum_required(VERSION 2.8.4)`.** Ubuntu 26.04 ships
   CMake 4.2.3:
   ```
   CMake Error at CMakeLists.txt:1 (cmake_minimum_required):
     Compatibility with CMake < 3.5 has been removed from CMake.
   ```
   Fix: `-DCMAKE_POLICY_VERSION_MINIMUM=3.5`.
2. **The vendor's `nginx_version_configuration.sh` re-downloads nginx itself** and re-runs
   `./configure`, which in our case needs `/build/modsecurity-nginx` and `/build/openssl` to exist.
   In `builder.Dockerfile` both do, so step 1 can use the vendor script as-is **after** nginx is
   configured — or, cheaper and more controlled, write the two `.mk` files directly from the
   configured tree, as this spike did.

**Recipe that worked (diff-shaped, for `builder.Dockerfile`):**

```dockerfile
# --- after the existing nginx configure+make, before the bundling step ---
RUN set -eux; \
    curl -fsSL https://github.com/openappsec/attachment/archive/@OPENAPPSEC_ATTACHMENT_SHA@.tar.gz \
      | tar -xz; \
    mv attachment-@OPENAPPSEC_ATTACHMENT_SHA@ /build/attachment
RUN set -eux; \
    cd /build/nginx && \
    awk '/^ALL_INCS = /{f=1} f{print} /^$/ && f{exit}' objs/Makefile \
      | tr '\\' ' ' | sed 's/ALL_INCS =//; s/-I//g' | tr -s ' ' '\n' | sed '/^$/d' \
      > include_paths.mk; \
    echo "-O2" > cc_flags.mk; \
    mkdir -p /build/attach-install; cp -a /build/nginx /build/attach-install/nginx-src; \
    cp include_paths.mk cc_flags.mk /build/attach-install/nginx-src/
RUN set -eux; \
    mkdir /build/attach-build && cd /build/attach-build && \
    cmake -DCMAKE_INSTALL_PREFIX=/build/attach-install -DATTACHMENT_TYPE=nginx \
          -DCMAKE_BUILD_TYPE=Release -DCMAKE_POLICY_VERSION_MINIMUM=3.5 /build/attachment && \
    make -j"$(nproc)" && make install
RUN set -eux; P="@PREFIX@"; \
    L=/build/attach-install/nginx_attachment/lib; \
    install -m 0755 $L/libngx_module.so "$P/modules/ngx_cp_attachment_module.so"; \
    install -m 0644 $L/libshmem_ipc_2.so $L/libosrc_shmem_ipc.so \
        $L/libosrc_compression_utils.so $L/libosrc_nginx_attachment_util.so "$P/lib/"
```

**Files it produces and where they belong** (sizes from this build):

| produced | size | destination in a build prefix |
|---|---|---|
| `libngx_module.so` | 478 312 | `modules/ngx_cp_attachment_module.so` (rename) |
| `libshmem_ipc_2.so` | 39 448 | `lib/` |
| `libosrc_shmem_ipc.so` | 48 136 | `lib/` |
| `libosrc_compression_utils.so` | 63 176 | `lib/` |
| `libosrc_nginx_attachment_util.so` | 157 696 | `lib/` |
| `libnano_attachment.so` | 403 352 | **not needed by nginx** (agent side) |
| `libnano_attachment_util.so` | 152 880 | **not needed by nginx** (agent side) |

The module's `DT_NEEDED` names exactly **three**: `libshmem_ipc_2.so`,
`libosrc_nginx_attachment_util.so`, `libosrc_compression_utils.so`. `libosrc_shmem_ipc.so` is the
older IPC implementation the vendor still ships; it is not pulled in. Harmless to bundle, but step 1
should decide deliberately rather than by accident.

**`ldd` and RUNPATH, bundled the builder's way.** The existing recipe's `ldd`-walk already picks up
`libstdc++`, `libgcc_s`, the brotli trio and the attachment's own three; the module then gets
`patchelf --set-rpath '$ORIGIN/../lib'`:

```
$ readelf -d .../modules/ngx_cp_attachment_module.so | grep RUNPATH
 0x000000000000001d (RUNPATH)  Library runpath: [$ORIGIN/../lib]
$ env -u LD_LIBRARY_PATH ldd .../modules/ngx_cp_attachment_module.so
	libshmem_ipc_2.so                => .../modules/../lib/libshmem_ipc_2.so
	libosrc_nginx_attachment_util.so => .../modules/../lib/libosrc_nginx_attachment_util.so
	libosrc_compression_utils.so     => .../modules/../lib/libosrc_compression_utils.so
	libstdc++.so.6                   => .../modules/../lib/libstdc++.so.6
	libgcc_s.so.1                    => .../modules/../lib/libgcc_s.so.1
	libbrotlidec.so.1                => .../modules/../lib/libbrotlidec.so.1
	libbrotlienc.so.1                => .../modules/../lib/libbrotlienc.so.1
	libbrotlicommon.so.1             => .../modules/../lib/libbrotlicommon.so.1
	libm.so.6 / libc.so.6            => /usr/lib/x86_64-linux-gnu/...
$ env -u LD_LIBRARY_PATH ldd .../modules/*.so | grep -c "not found"
0
```

**Clean once bundled — confirmed.** Caveat for step 1: `libosrc_compression_utils.so` and
`libosrc_nginx_attachment_util.so` need `libstdc++`/`libgcc_s` as *their own* `DT_NEEDED`, and
RUNPATH is not inherited, so both must get `$ORIGIN` (the existing
`patchelf --set-rpath '$ORIGIN' "$P"/lib/*` line covers it). The self-check
`! ldd ... | grep "not found"` will catch a miss.

**Loads with the agent absent — confirmed.** A throwaway instance with `load_module` and no agent:

```
$ <prefix>/sbin/nginx -t -c /etc/nginx-spike/nginx.conf
nginx: the configuration file /etc/nginx-spike/nginx.conf syntax is ok
nginx: configuration file /etc/nginx-spike/nginx.conf test is successful
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/
200
```

The error log explains the pass-through: `IPC is uninitialized. Skipping inspection of current
request.` A **`getpwnam("www") failed`** in a bare container is the container's missing user, not the
module; step 1's test must run as the real host user or create it.

### 0.3 The vendor installer in local-policy / no-cloud mode

**The official installer cannot install the agent on this host at all**, and — importantly — **it
cannot touch our nginx**, because it targets the *distro's* nginx:

```
$ sha256sum open-appsec-install
f624957050d133171f25197f94e495ef34322e883b8609080d5b67b41b7322ed  open-appsec-install   (28 310 B, installer v1.2245.1)

$ ./open-appsec-install --auto --no-email
open-appsec for NGINX, Kong and APISIX Installer v1.2245.1
Searching local NGINX…
nginx version found: 1.28.3-2ubuntu1.12
Unsupported nginx version: 1.28.3-2ubuntu1.12. Please see docs.openappsec.io
INSTALLER EXIT=1
```

Mechanism, from the script: `get_nginx_full_version` runs `apt list -a nginx | grep now` and takes
the **apt package version**, not `nginx -V`. The support list for this codename has
`1.28.3-2ubuntu1.14`, the host has `1.28.3-2ubuntu1.12`, so it refuses. `nginx` on PATH is
`/usr/sbin/nginx` (distro 1.28.3) and `/etc/nginx/nginx.conf` exists, so had the version matched it
would have `sed -i '1s/^/load_module .../'` **the distro's** config and `service nginx restart` —
never `/www/panel/nginx`. The right failure: the installer is not the tool for our build.

**The agent's own packages install cleanly and touch no nginx file.** The README's manual path works:

```
$ curl -fsSL -o openappsec-resolute.tar.gz \
    https://downloads.openappsec.io/packages/agent/x86_64/ubuntu/resolute/openappsec-resolute.tar.gz
$ sha256sum openappsec-resolute.tar.gz
ea86695100582c1ca0b538eed7ba45a109929195be588a468d2f92bc94d263ba  openappsec-resolute.tar.gz   (59 454 211 B)

$ ./install-cp-nano-agent.sh --install --hybrid_mode
… open-appsec Orchestration Nano Service installation completed successfully
$ ./install-cp-nano-service-http-transaction-handler.sh --install
… Installation completed successfully.
$ ./install-cp-nano-attachment-registration-manager.sh --install
… Installation completed successfully.
```

`--hybrid_mode` is the local-policy, no-token mode (without `--token` the wrapper passes
`--hybrid_mode` itself). **Flags rejected and why:** `--token`/`--email` (SaaS registration),
`--fog` (a specific fog), `--kong-plugin`/`--central-nginx-manager` (different products),
`--proxy`/`--external_addresses`/`--certs-dir`/`--public-key` (not needed here), `--prevent` (a
policy choice the panel owns), `--offline_mode` (see 0.7 — it does not do what it says).

**What it writes.** `/etc/cp/` (bin, certs, conf, data, scripts, orchestration,
attachmentRegistrator, HttpTransactionHandler, watchdog), the unit
`/etc/systemd/system/nano_agent.service`, `/var/log/nano_agent/`, and shared memory in `/dev/shm`.
It runs **as root** — no dedicated user or group is created:

```
$ ps -eo user,args | grep -E 'cp-nano|http-transaction' | awk '{print $1}' | sort -u
root
$ getent passwd | awk -F: '$3>=999'
nobody … srv:x:1000 … www:x:1500
```

**No dpkg/apt action:** `dpkg -l | wc -l` was `545` before and `545` after. Everything is tarballs.

**It touches no nginx file.** SHA-256 over every file under `/www/panel/nginx/current/`, plus
`/www/panel/nginx/includes`, `/www/panel/nginx/sites` and the top-level `nginx.conf` /
`panel-http.conf` / `panel-main.conf` (69 files) before and after, `diff` empty:

```
$ diff /tmp/oa/sum-nginx-current-before.txt /tmp/oa/a1.txt && echo "OUR NGINX TREE: UNCHANGED"
OUR NGINX TREE: UNCHANGED
$ diff /tmp/oa/sum-distro-nginx-before.txt /tmp/oa/d1.txt && echo "DISTRO NGINX TREE: UNCHANGED"
DISTRO NGINX TREE: UNCHANGED
```

**One destructive interaction found.** Installing the agent drops
`/etc/systemd/system/nano_agent.service`, and on its first start the **distro** `nginx.service`
restarted at 17:54:22. `nginx.service` and `panel-nginx.service` share `PIDFile=/run/nginx.pid`; the
distro unit's `ExecStop=-/sbin/start-stop-daemon … --pidfile /run/nginx.pid` sent SIGQUIT to **our**
panel nginx and took it down (exit 0, no error in the journal). `panel.service` restarted it ~2.5
minutes later. On a host where both nginx units exist this is a real hazard for step 2: the Store
job must `start`/`reload` `panel-nginx.service` after the agent install, or assert it active.

**Supply chain — D1 needs a second look.** *The agent download is not versioned at the URL.*
`https://downloads.openappsec.io/packages/agent/x86_64/ubuntu/resolute/openappsec-resolute.tar.gz`
serves whatever is current; requests labelled `1.1.36`, `1.1.37`, `1.1.38` all answer `200` for the
*same* URL, and versioned paths a pinned install would use are `403`. The tarball fetched is
**1.1.37** (`install-cp-nano-agent.sh`: `label="Check Point Nano Agent Version 1.1.37 Install
Package"`), while GitHub's latest **release** is **1.1.36** — the agent is published independently
of the repo releases, so "pin the version and its SHA-256" (D1) can only mean pin the **SHA-256 of a
stored copy**; the download URL alone pins nothing, and re-downloading later will silently fetch a
newer agent than the module was built against.

### 0.4 Loading under `panel-nginx.service`, and the sandbox

**The unit has no sandbox.** `systemctl cat panel-nginx.service` shows `Type=simple`,
`ExecStartPre=… -t`, `KillMode=mixed`, `Restart=on-failure`, `LimitNOFILE=65536` — **no
`ProtectHome`, `ProtectSystem`, `PrivateTmp`, `NoNewPrivileges`, `PrivateDevices`, `ReadWritePaths`.**
Nothing in the unit can block `/dev/shm`. Workers run as `www` (uid 1500):

```
$ ps -o pid,user,args -C nginx | grep 'nginx: worker'
 34771 www   nginx: worker process
```

**What the agent creates in `/dev/shm`** (the plan said "shared memory and socket"; the real set is
larger and defines the permission contract):

```
$ stat -c '%n mode=%a owner=%U:%G size=%s' /dev/shm/__cp_nano_rx_shared_memory_1__ \
    /dev/shm/attachment-metadata /dev/shm/check-point /dev/shm/check-point/*
/dev/shm/__cp_nano_rx_shared_memory_1__                     mode=666 owner=root:root size=823379
/dev/shm/attachment-metadata                                mode=644 owner=www:www  size=32
/dev/shm/cp_nano_http_attachment_conf                       mode=644 owner=root:root size=1399
/dev/shm/check-point                                        mode=777 owner=root:root
/dev/shm/check-point/cp-nano-http-transaction-handler-1     mode=666 owner=root:root
/dev/shm/check-point/cp-nano-attachment-registration        mode=666 owner=root:root
```

`__cp_nano_{rx,tx}_shared_memory_<1..4>__` are the per-worker ring buffers (one pair per worker,
`cp_worker_processes`), world read-write so `www` can write them — **823 KB per segment, eight
segments = ~6.6 MB of tmpfs** on this 1.7 GB `/dev/shm`. `attachment-metadata` is written by **our**
worker (`www:www`) and read by the agent. Everything else under `check-point/` is a Unix socket,
`666`.

**The load worked, and a request still served.** With `load_module` in a new
`/www/panel/nginx/waf/main/open-appsec.conf` (the glob `panel-main.conf` already includes) and the
site-level `cp-nano-nginx-attachment on;` in `sites/up5.test.conf`:

```
$ /www/panel/nginx/current/sbin/nginx -t -c /www/panel/nginx/nginx.conf
nginx: the configuration file /www/panel/nginx/nginx.conf syntax is ok
nginx: configuration file /www/panel/nginx/nginx.conf test is successful
$ systemctl reload panel-nginx.service; curl -H 'Host: up5.test' -o /dev/null -w '%{http_code}\n' http://127.0.0.1/
200
$ grep 'registered on client' /www/panel/logs/nginx-error.log | tail -1
… Successfully registered on client. socket: 4, instance ID: 1, instances amount: 4,
  received path: /dev/shm/check-point/cp-nano-http-transaction-handler-1
```

Confirmed placement: `load_module` is **main context** (a file under `waf/main/`, beside the
ModSecurity loader), and `cp-nano-nginx-attachment` is **`server` or `location` context**, never
`http` (see 0.5).

**Resilience defect found: nginx registers with the agent only at start/reload.** When the agent is
restarted while nginx keeps running, the agent recreates its `/dev/shm/check-point/` sockets with
new inodes; nginx still holds the old ones and logs `IPC is uninitialized`, so **enforcement stops
silently (requests pass, 200) until nginx is reloaded**. Reproduced:

```
$ systemctl restart nano_agent.service; systemctl is-active panel-nginx.service
active
$ curl … sqli → 200          # expected 403
$ systemctl reload panel-nginx.service
$ curl … sqli → 403
```

Step 2's install job and step 4's apply order must reload `panel-nginx` **after** the agent is up.
The module's keep-alive timer (10 s) re-checks the agent but does not re-register.

### 0.5 The policy schema

**Schema version `v1beta2`**, from the installed file's header and from the repo's
`config/linux/v1beta2/schema/schema_v1beta2.yaml`:

```
$ head -3 /etc/cp/conf/local_policy.yaml
# open-appsec default declarative configuration file
# based on schema version: "v1beta2"
```

**Mode names, exact:** `detect-learn`, `prevent-learn`, `inactive` — and the schema adds `prevent`,
`detect` and `inherited`. A `specificRules` entry's `mode` accepts
`prevent-learn|detect-learn|prevent|detect|inactive` (`default: detect-learn`).

**Confidence:** the field is **`webAttacks.minimumConfidence`**, values **`medium|high|critical`**
(`default: high`). It is not a `specificRules` field; it lives on a *practice*, and the rule points at
the practice. The plan's step-3 wording matches except for the field name: `minimumConfidence`, not
`confidence`.

**Exceptions are named objects, not inline patterns.** The plan's step 3 assumed
`kind: path|source_ip|param` with a note; the schema is:

```yaml
exceptions:
  - name: <id>
    action: accept        # skip | accept | drop | suppressLog
    condition:
      - key: <key>        # schema says free string; only some keys compile
        value: <value>
```

and a policy or a `specificRules` entry refers to them by **name**: `exceptions: [<id>]`.

**Which keys actually work — proven, and one silent trap.** The schema does **not** enumerate the
keys; an unknown key compiles to an **empty match**
(`"match": {"op":"and","type":"operator","items":[]}`) while `--apply-policy` still says *"New policy
applied"*, so a wrong key is a silent no-op. Tested by inspecting
`/etc/cp/conf/exceptions/exceptions.policy` and by attack:

```
key: hostName  → {"key":"hostName","op":"equals","type":"condition","value":["up5.test"]}
                 same SQLi on that host → 200 (accepted)
key: paramName → {"key":"paramName","op":"equals","type":"condition","value":["id"]}
                 ?id=<sqli> → 200,  ?cat=<sqli> → 403
key: host, urlPath, method → compiled to an EMPTY match, did nothing (still 403)
```

The keys to use are the ones in the agent binary
(`strings … | grep -xE 'sourceIP|paramName|hostName|…'`): `sourceIP`, `sourceIdentifier`,
`hostName`, `httpMethod`, `paramName`, `parameterName`, `location`, `countryCode`, `sourcePort`,
`destinationPort`. The plan's three kinds map to `paramName` (param) and `hostName` (host); **a path
exception has no proven key yet** — `urlPath` silently does nothing and the right key is still
**unknown**. Step 3 must not ship path exceptions until one is found, or it ships a control that
lies.

**Per-server / per-location on-off directive — yes, this settles D4.** The attachment defines
`cp-nano-nginx-attachment on|off`, **`NGX_HTTP_MAIN_CONF|NGX_HTTP_SRV_CONF|NGX_HTTP_LOC_CONF|NGX_CONF_FLAG`**
(`ngx_http_cp_attachment_module.c`, command table). `enable` defaults to `NGX_CONF_UNSET` and
`is_ngx_cp_attachment_disabled` returns `!conf->enable`, i.e. **inspected unless turned off**; it
inherits server → location. Proven in a config `nginx -t` accepts:

```nginx
server { listen 18080; cp-nano-nginx-attachment on;  location / { return 200 "inspected\n"; } }
server { listen 18081; cp-nano-nginx-attachment off; location / { return 200 "exempt\n"; } }
server {
  listen 18082;
  location /health { cp-nano-nginx-attachment off; return 200 "health-ok\n"; }
  location /       { cp-nano-nginx-attachment on;  return 200 "inspected2\n"; }
}
```

All four requests served; `nginx -t` passed. **But:** the directive is known only **while the module
is loaded** — with `load_module` removed, the same site makes `nginx -t` fail with
`[emerg] unknown directive "cp-nano-nginx-attachment"`. Step 4 must add and remove the module file
and the site directive **together, in one change**, never one without the other.

### 0.6 Security events: where and in what format

**By default the events go to the cloud and nowhere local.** The stock policy has
`logDestination: {cloud: true, logToAgent: false}`. With it, three blocked attacks produced **no**
event anywhere the panel could read, and `open-appsec-ctl --view-logs` showed only policy-load and
service-start records. Switching the trigger to
`cloud: false, logToAgent: true, stdout: {format: json}` made them appear; they are **JSON Lines**,
one object per line. Three real ones from deliberate attacks (trimmed):

```
{"eventTime":"2026-11-24T18:01:47.380","eventName":"Web Request","eventSeverity":"Critical",
 "eventSource":{"agentId":"db34e33b-…","serviceName":"HTTP Transaction Handler","serviceId":"1","assetId":"Any","assetName":"Any"},
 "eventData":{"eventReferenceId":"2ff09af1-…","eventConfidence":"Very High","sourceIP":"127.0.0.1",
   "httpHostName":"up5.test","httpMethod":"GET","httpUriPath":"/","httpUriQuery":"id=1' OR '1'='1' UNION SELECT …",
   "securityAction":"Prevent","practiceType":"Threat Prevention","waapIncidentType":"SQL Injection",
   "matchedSample":"1' or '1'='1' union select username,password from users--",
   "matchedLocation":"url parameter","matchedParameter":"id","waapFinalScore":1000}}

{"eventTime":"2026-11-24T18:01:47.394","eventName":"Web Request","eventSeverity":"Critical",
 "eventData":{"sourceIP":"127.0.0.1","httpHostName":"up5.test",
   "httpUriQuery":"q=<script>alert(document.cookie)</script>","securityAction":"Prevent",
   "waapIncidentType":"Cross Site Scripting, Remote Code Execution",
   "matchedSample":"<script>alert(document.cookie)</script>","matchedLocation":"url parameter","matchedParameter":"q"}}

{"eventTime":"2026-11-24T18:01:47.405","eventName":"Web Request","eventSeverity":"Critical",
 "eventData":{"sourceIP":"127.0.0.1","httpHostName":"up5.test",
   "httpUriQuery":"cat=1 AND (SELECT 5 FROM (SELECT SLEEP(5))a)","securityAction":"Prevent",
   "waapIncidentType":"SQL Injection","matchedParameter":"cat"}}
```

**Each event names the host**: `eventData.httpHostName` is the `Host` header, so an event routes to
the site that owns that `server_name`. The UI fields map cleanly: `securityAction`
(Prevent/Detect) → action, `eventConfidence` → confidence, `waapIncidentType` → attack type,
`matchedParameter`/`matchedLocation` → matched parameter, `sourceIP`, `httpUriPath` +
`httpUriQuery`, `eventReferenceId` → request id.

**Path, rotation, permission.** Where the events land depends on the destination:
- `logToAgent: true` → the agent's own store; read with `open-appsec-ctl --view-logs` (root) or the
  files under `/var/log/nano_agent/` (`cp-nano-http-transaction-handler.log<N>`, mode `644
  root:root`; the directory is `700 root:root`).
- `stdout` → the service's stdout; under `nano_agent.service` the services are children of the
  watchdog, and in this spike stdout produced nothing in the journal — `logToAgent` and the log files
  are the observable path.

**There is no logrotate config for `/var/log/nano_agent`** and the vendor scripts rotate by their own
numbering (`…handler.log1..4`, `…handler.dbg1..4`). Step 5 reads `…handler.log*`; the directory is
root-only, and the panel's reader already runs as root.

### 0.7 Outbound traffic — D5

**Hard stop. In hybrid "local management" mode the agent holds TLS connections to Check Point's
cloud continuously; no supported flag turns them off, and they are on the policy-apply path.**

Destination, from `ss -tnp` sampled every 10 s:

```
$ ss -tnp state established | grep cp-nano | grep -v 127.0.0.1
10.10.11.106:40692  75.2.123.205:443   users:(("cp-nano-orchest",pid=…))
10.10.11.106:42722  99.83.172.252:443  users:(("cp-nano-http-tr",pid=…))
10.10.11.106:42922  75.2.123.205:443   users:(("cp-nano-attachm",pid=…))
… (across all samples: 38 sockets for cp-nano-http-tr, 76 for cp-nano-orchest, 28 for cp-nano-attachm)

$ getent hosts 75.2.123.205
75.2.123.205  aa8b7fe11ba87807e.awsglobalaccelerator.com
$ getent hosts 99.83.172.252
99.83.172.252  aa8b7fe11ba87807e.awsglobalaccelerator.com
$ echo | openssl s_client -connect 75.2.123.205:443 -servername inext-agents.cloud.ngen.checkpoint.com \
   | openssl x509 -noout -subject -issuer
subject=CN=*.cloud.ngen.checkpoint.com
issuer=C=US, O=Amazon, CN=Amazon RSA 2048 M04
```

Configured: the agent writes `"Fog address": "https://inext-agents.cloud.ngen.checkpoint.com"` into
`/etc/cp/conf/`; the installer prints *"in order for the agent to remain active and effective it must
connect to the Fog/Cloud at least every 45 days"*.

**Three of the four services connect:** `cp-nano-orchestration`, `cp-nano-http-transaction-handler`
(all four workers) and `cp-nano-attachment-registrator`. Only the watchdog does not.

**`--offline_mode` does not stop it.** Re-running
`install-cp-nano-agent.sh --install --hybrid_mode --offline_mode` upgraded in place and the sockets
came straight back.

**Blocking it breaks policy management.** With
`iptables -I OUTPUT -m set --match-set cpsink dst -j REJECT` over both addresses:

```
$ /etc/cp/scripts/open-appsec-ctl.sh --apply-policy /etc/cp/conf/local_policy.yaml
Applying new policy. …
Policy didn't change please verify that you have a legal new policy
Policy load status: Error

$ grep -E 'fog|Connection' /var/log/nano_agent/cp-nano-orchestration.dbg | tail -3
 Connection to: inext-agents.cloud.ngen.checkpoint.com:443 failed and will be suspended for 60 seconds
 Failed to receive access token. Error: Failed to get access token.
 Failed to send periodic health check patch to the fog
```

Removing the block, the same command succeeds immediately (`New policy applied.`). The "local"
management mode is local in *where the policy comes from*, not in *whether it phones home*.

**What was watched, honestly.** `ss -tnp` every 10 s plus `tcpdump` was the intent. **`tcpdump` is
not installed on this host** (`command -v tcpdump` empty; `/tmp/oa/netwatch/tcpdump.log` stayed at
0 lines), so packets themselves were **not** captured — only sockets. Continuous observation spans
**18:11:37 → 18:27:10 (15 min 33 s, ~94 samples)**, then the reboot for 0.9, then a resumed watch
**18:29:01 → ~18:54 (25 min, 150 samples)** on the same agent. **Total ≈ 40 minutes, short of the
45–60 asked for.** Across every sample the *only* external destinations the agent ever had were the
two Check Point addresses above — 379 and 340 socket observations, no other host and no other port.
The per-process split over all samples: `cp-nano-attachment-registrator` 142, `cp-nano-orchestration`
277, `cp-nano-http-transaction-handler` 300.

**Recommendation: stop before step 2** and bring this to the operator. A panel whose PRD says "no
telemetry" cannot install an agent that holds a cloud connection by design. The honest options are
(a) accept it and document it loudly on the Store card (it is the vendor's only supported mode);
(b) firewall egress from the agent hosts and accept that policy *apply* then fails, i.e. policy is
pushed only while the window is open; or (c) do not ship open-appsec.

### 0.8 Footprint

Agent, measured on this 4-vCPU host (RSS from `ps`, cgroup from systemd):

| state | process | RSS | CPU |
|---|---|---|---|
| idle | `cp-nano-watchdog` (sh) | 2.1 MB | 0.3 % |
| idle | `cp-nano-orchestration` (hybrid) | 27.7 MB | 1.9 % |
| idle | `cp-nano-attachment-registrator` | 18.7 MB | 1.6 % |
| idle | `cp-nano-http-transaction-handler` ×4 | 40.2–43.5 MB each | 1.6–1.8 % each |
| **idle total** | 7 processes | **218.8 MB RSS / 145.5 MB cgroup** | **~10 % of one core** |
| load (3000 req in 36 s, ~83 req/s) | same 7 | **228.4 MB RSS / 152.3 MB cgroup** | 0.3–5.2 % each |

The agent is far heavier than ModSecurity, and its *idle* CPU (~10 % of a core) is not zero — the
four transaction-handler workers each burn ~1.7 % with no traffic.

**Added latency**, same URI, same host, 200 requests per run, 3 runs each, paired:

| | p50 | p95 | max |
|---|---|---|---|
| WAF off (module not loaded) | 1.2 ms | 1.6 ms | 22.5 ms |
| WAF on (prevent, inspecting, agent local) | 4.0 ms | 6.0 ms | 30–80 ms |

**≈ +2.8 ms p50, ≈ +4.4 ms p95.** The first WAF-on run's max (80 ms) is registration/learning
warm-up; later runs settle near 30 ms. The baseline is a static page — a real application's own time
would dilute the ratio.

### 0.9 Fail-open and reboot

**Fail-open — confirmed.** Module loaded and site inspected, 20 requests, stop the agent, 20 more:

```
BEFORE stop: 200s: 20/20
$ systemctl stop nano_agent.service ; systemctl is-active nano_agent.service
inactive
AFTER stop:  200s: 20/20
panel-nginx.service: active
$ nginx -t -c /www/panel/nginx/nginx.conf
… test is successful   EXIT=0
```

**Reboot — everything came back with no panel action.** Rebooted at 18:27:10:

```
$ uptime -s
2026-11-24 18:27:25
$ for s in panel.service panel-nginx.service nano_agent.service nginx.service; do \
    printf '%-24s active=%s enabled=%s\n' $s "$(systemctl is-active $s)" "$(systemctl is-enabled $s)"; done
panel.service            active=active enabled=enabled
panel-nginx.service      active=active enabled=enabled
nano_agent.service       active=active enabled=enabled
nginx.service            active=active enabled=enabled

$ /etc/cp/scripts/open-appsec-ctl.sh --status | sed -n '7,11p'
Policy files:
    /etc/cp/conf/local_policy.yaml
Policy load status: Success

$ curl … sqli → 403 ; xss → 403 ; benign → 200
```

The agent's unit is `enabled`, the module's `load_module` is in the config, and the policy on disk
reloaded and re-applied itself. **Caveat:** `/dev/shm` sockets are recreated with new inodes by the
fresh agent, and the fresh nginx registers against them because both start together — the 0.4 defect
(agent restart without an nginx reload) does **not** appear across a reboot, only across a lone
agent restart.

### D4 and D5, settled

**D4 — a site not using open-appsec once the module is loaded.**

Settled **two ways, use both**. (1) The attachment's own directive **`cp-nano-nginx-attachment off;`**
in a `server` or `location` block (main context too) exempts that site/location; `nginx -t` accepts it
and it inherits server → location. Because `enable` defaults to `NGX_CONF_UNSET` ⇒ inspected, a site
with the module loaded but **no** directive *is* inspected, so the panel must emit `off` explicitly
for every non-open-appsec site — or load the module only while at least one site uses it, which is
simpler. (2) The policy's default `mode: inactive` means only hosts listed in `specificRules` are
acted on; keep it as the belt to the directive's braces. The trade-off to respect: the directive
without the module makes `nginx -t` fail, so they are added and removed in one change. This refines
the plan's D4, which guessed `inactive` might be the only lever.

**D5 — outbound traffic from the agent.**

**Not settled in the plan's favour: the agent does talk to the cloud in local mode, continuously, and
the traffic cannot be switched off with a supported flag.** Three of four services open TLS to
`inext-agents.cloud.ngen.checkpoint.com` (AWS Global Accelerator) and keep it; `--offline_mode` does
not stop it; blocking the addresses makes `open-appsec-ctl --apply-policy` fail, so it is not just
telemetry — it is on the policy path. **This is the D5 hard stop: bring it back to the operator before
step 2.** Three honest options are listed at the end of 0.7; none is a clean "no cloud".

**State of the sandbox after the spike.** The module and its libraries were removed from
`/www/panel/nginx/current/`, and the `load_module` file and the site directive were removed;
`nginx -t` passes and `up5.test` serves 200. The agent **was uninstalled** with
`echo Y | /etc/cp/scripts/open-appsec-ctl.sh --uninstall`: `/etc/cp` gone, `nano_agent.service`
gone, no agent process. It was then **reinstalled** only to keep the outbound watch running to its
final window, so it is **still installed at hand-off** — remove it again with the same command if the
host must be clean. Inert leftovers the uninstaller does not reap:
`/dev/shm/{check-point,static_resources,cp_nano_http_attachment_conf}`.
`panel.service` and `panel-nginx.service` are active and serving.

### 6.1 Follow-up: is there a standalone mode that avoids the cloud?

**No.** Checked on 2026-11-25 because D5 turned on whether the agent can run without egress.

**The vendor documents "standalone", but it is the mode 0.7 already measured.** The docs describe
*Declarative Management* vs *Central Management*, and the Helm/compose `standalone` profile as
"locally managed … with no connection to central management WebUI (SaaS)". The agents page is
unqualified by mode: "make sure to have outbound communication allowed from your agent", naming
`https://inext-agents.cloud.ngen.checkpoint.com`. "No cloud *console*" is not "no cloud *egress*".

**The vendor's own script rewrites `--standalone` into `hybrid_mode`** — it is a label, not a
separate code path (`open-appsec-ctl.sh:1494-1498`; same at `cp-nano-agent-ctl.sh:1371-1375`):

```
$ sed -n '1494,1498p' /etc/cp/scripts/open-appsec-ctl.sh
if [ "$current_var" = "--online_mode" ] || [ "$current_var" = "--offline_mode" ] || [ "$current_var" = "--standalone" ]; then
    mode=$(echo $current_var | sed 's/-//g')
    if [ "$mode" = "standalone" ]; then
        mode="hybrid_mode"
    fi
```

The installed agent confirms it: `/etc/cp/conf/agent_details.json` records
`"Orchestration mode": "hybrid_mode"` while `--status` prints `Management mode: Local management`.

**Blocking the fog is worse than 0.7 recorded: it stops enforcement, not just policy pushes.**
With `iptables -I OUTPUT -j <chain rejecting the two fog addresses>`, a `--apply-policy` of a
*modified* policy **hung past a 120 s timeout** (never returned), the agent fell to
`Policy load status: Error`, and an SQL-injection request returned **200** — the same fail-open
200 that a healthy agent gives only to benign traffic. Removing the block made the same command
finish immediately (`New policy applied.`, `Policy load status: Success`). So plan option (b)
("firewall egress, accept that policy apply fails") does not hold: an egress block degrades the
running agent into non-enforcement, because the fog is on the policy *and* health path.

**Consequence for D5.** The only honest options are (a) ship and document the cloud link loudly, or
(c) do not ship. A "standalone" install is not a third option.

**D5 decision — 2026-11-25: option (a), accept with a prominent tracking mark.** The operator
chose to ship open-appsec and mark clearly, wherever the engine is offered, that using this WAF
variant can subject the host and its traffic to Check Point's tracking. This is a product
requirement, not only a doc note, and it binds steps 2, 6 and 8:

- **Step 2 (Store):** the open-appsec card and the install confirmation must state, before the
  operator can install, that the agent holds a continuous TLS connection to Check Point's cloud and
  that using this engine can subject the host to tracking. Installation requires an explicit
  acknowledgement of that same text.
- **Step 6 (site UI):** the engine picker marks open-appsec with the same warning inline, so the
  choice is never made without seeing it.
- **Step 2/6 (firewall overview and events page):** a site running open-appsec is marked as such, so
  an operator reading the firewall page can see which sites carry the engine.
- **Step 8 (docs):** the warning appears in the firewall section of the operator docs and in the
  handover note, with the measured facts from 0.7 (hostnames, the 45-day reconnect requirement, and
  that the traffic is on the policy-apply path).

The warning text must be accurate rather than vague: name Check Point, name that the connection is
continuous and required, and say that enforcement degrades without it. Do not euphemise it as
"telemetry optional" — 0.7 and 6.1 show it is neither optional nor telemetry-only.

### 6.2 A host conflict that weakens part of 0.9's evidence

Found 2026-11-25 while restoring the box. **`nginx.service` — the distro nginx 1.28.3 installed on
the host on 2026-11-19 — was `enabled` and active, and also bound `:80`.** It serves no site, but
`SO_REUSEPORT` put both listeners on port 80, so requests were split between the panel's nginx
(with the attachment) and the bare distro one:

```
$ ss -tlnp | grep ':80 '
LISTEN … users:(("nginx",pid=87751,fd=7),("nginx",pid=87748,fd=7),…,("nginx",pid=1391,fd=7))
```

Any request → 200 could therefore have been the bare nginx, not the agent failing open. **0.9's
fail-open result stands** (it also checked `nginx -t`), but a smoke test that sends an attack and
expects 403 must first assert that only the panel's nginx owns the port. It is now
`inactive`/`disabled`.

**Boot-order hazard:** `systemctl stop nginx.service` also took down `panel-nginx.service`, so the
two share a listener in a way that is not safe across a reboot. Step 7 (host gate) and the installer
should assert the serving port has exactly one owner.

**One thing this follow-up could not settle.** After restoring the module from the surviving Docker
build layer and re-adding the site directive, all four workers registered
(`successfully registered to nano service`) and `/dev/shm` matched 0.4's contract, but the agent
wrote no `Web Request` event for a repeat SQL-injection — only `Nano-service started`. The module's
three `NEEDED` libraries all resolve, and the two files the install prefix lacked
(`libnano_attachment{,_util}.so`) are not among them. Whether this is a consequence of the
hand-restored prefix, of the policy rebuild under blocked egress, or of something else was not
established; **step 1's real build (not a hand-copied prefix) is the place to settle it.**
