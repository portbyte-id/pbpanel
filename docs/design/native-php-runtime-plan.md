# Native PHP-FPM runtime: plan

Status: **plan only, nothing built.** Written 2026-11-19 after reading the native nginx runtime
(`docs/design/native-nginx-runtime.md`, `internal/nginx/*`) and the current Docker PHP runtime
(`internal/php/*`, `internal/sites/php.go`, `cmd/panel/nginx.go`).

Scope: PHP **8.3, 8.4 and 8.5**, starting with **8.3 on Ubuntu**. EL (Rocky/RHEL) stays in **M9**
with nginx. The database runtime (Docker) is untouched.

## 1. Goal

PHP-FPM runs **natively under systemd**, one unit per version, exactly as nginx does now. Docker
stays as the **builder only**: it compiles PHP in a throwaway container that matches the host
distro, the finished prefix is copied to the host, and the container and image are thrown away.

Why it is worth doing:

- The derived image is ~680 MB because the `-dev` packages and `${PHPIZE_DEPS}` stay in a layer
  ([extensions.go:133](../../internal/php/extensions.go)). A native prefix should be roughly
  50 MB lean and 120-180 MB with ICU and ImageMagick (**estimate, to be measured in step 0**).
- PHP stops depending on the Docker daemon at runtime: no reboot reconcile for `unless-stopped`
  containers, no "Docker is down so PHP is down".
- A real `php` binary exists on the host (composer, cron, one-off scripts), with no container
  round trip.

What does **not** change: the config tree (`/www/panel/php/<ver>/{php-fpm.conf,php.ini,conf.d,pool.d}`),
the pool files, the socket paths in `/www/panel/run`, the `www` account (1500:1500), the confedit
apply pipeline, the HTTP routes and JSON shapes. nginx already talks to the pool sockets by path,
so it does not care what runs behind them. The PHP containers already use host networking
(`internal/docker/unit.go`), so native adds no network change.

## 2. Decisions to ratify before step 1

| # | Decision | Recommendation |
|---|---|---|
| D1 | Replace the Docker PHP runtime or keep both | **Replace**, as nginx did. Build on a branch with the Docker path intact until step 8 passes, then delete it in step 9 |
| D2 | How extensions are built | **PHP core once, every extension as a shared `.so` built with `phpize`**, which is what `docker-php-ext-install` does. Keeps a rebuild cheap (core layer cached by version) and treats bundled and PECL extensions the same. Alternative (static, everything in `./configure`) means a 3-6 min full recompile for every extension change. **Step 0 correction: the extension list must not be a Docker build argument**, or BuildKit re-runs every later layer and the core recompiles each time (see step 0) |
| D3 | How much confinement the unit gives | **Parity with the container's mounts**: `ProtectSystem=strict` with `ReadWritePaths=` = run dir, site log dir, sites root, allowed web roots; plus `PrivateTmp`, `InaccessiblePaths=` for the panel's own data. Reads outside those stay possible for the `www` user, as for any local account. Changing the allowed roots rewrites the unit and restarts the running PHP units, as it recreates the containers today. **Step 0 correction: `ProtectHome=read-only`, not `true`** (a `true` hides `/home` from `ReadWritePaths` too), and FPM's own log must be a real file, not fd 2 (see step 0) |
| D4 | PECL pinning | **Parity first**: `pecl install <name>` as today (latest, unpinned). Pinning version + sha256 per extension per PHP series is a supply-chain improvement worth a later step, not part of this one |
| D5 | Hosts still on 8.2 | The builder takes **any `php.versions` entry**, so a host that still lists 8.2 gets it built natively during the upgrade like the others. Only `DefaultVersions` changes to `8.3,8.4,8.5`. Nothing keeps running in Docker |
| D6 | Host `php` on PATH | **No.** The binary is at `/www/panel/php/<ver>/current/bin/php` and the Apps/cron features may use it later. A `php` on PATH would fight a distro package. There is no PHP CLI shim in the repo today, so nothing is lost |

## 3. Design

### 3.1 On disk

```
/www/panel/php/<ver>/                 config tree, unchanged
  php-fpm.conf  php.ini  conf.d/zz-panel.ini  pool.d/<pool>.conf
  versions/<build-id>/                one directory per build, never written in place
    sbin/php-fpm
    bin/php
    lib/                              bundled shared libraries (not the glibc core), RUNPATH=$ORIGIN/../lib
    lib/php/extensions/<api>/*.so     shared extensions, RUNPATH=$ORIGIN/../../..
    etc/php.ini-production            what php.ini is first copied from
    etc/conf.d/*.ini                  one line per shared extension, scanned first
  current -> versions/<build-id>      relative symlink; the only thing a swap changes
/etc/systemd/system/panel-php-<ver>.service
/www/panel/run/php-<ver>-<pool>.sock  unchanged
```

`<build-id>` is `images.NewID`, compiled in as the build's `--prefix` (same trick as nginx), so
`extension_dir` and `--with-config-file-scan-dir` point at the build that is loading them and a
candidate never loads the serving build's modules.

`PHP_INI_SCAN_DIR=:/www/panel/php/<ver>/conf.d` (leading colon keeps the compiled-in dir) in the
unit and in every validator call, exactly as `scanDirEnv` does today.

### 3.2 Build recipe

`internal/php/builder.Dockerfile` (embedded, `@NAME@` placeholders as nginx does):

1. Builder image matches the host: `ubuntu:<VERSION_ID>` / `debian:<VERSION_ID>`, anything else is
   `ErrUnsupportedHost` before Docker is touched.
2. Download `php-<release>.tar.xz` from php.net and **verify its sha256** against the value
   php.net publishes. A series (`8.3`) takes the newest patch at build time; a full version pins.
   *Where the checksum is read from must be confirmed in step 0.*
3. `./configure --prefix=<build dir> --enable-fpm --with-fpm-user=www --with-fpm-group=www` with
   the same core set the official image ships (so a version behaves as before): curl, openssl,
   zlib, libxml/dom/simplexml/xml*, mbstring, mysqlnd, pdo + pdo_sqlite + sqlite3, readline, sodium,
   iconv, fileinfo, ctype, tokenizer, posix, phar. Then `make -j$(nproc)` and `make install`.
4. Extensions, one layer each, in catalogue order: `phpize && ./configure <flags> && make install`,
   from `ext/<name>` for bundled ones and `pecl install` for PECL ones; a line in `etc/conf.d/` enables each.
   `opcache` is a `zend_extension`. *PHP 8.5 reportedly builds OPcache into the core; step 7
   confirms and skips the separate build there.*
5. Bundle the libraries with `ldd` + `patchelf`, as nginx does, except the glibc core.
6. Strip, delete headers/man/docs, and end with checks: `php -v` matches the asked release,
   `php -m` lists every requested extension, `php-fpm -t` accepts a minimal config, and `ldd`
   shows no unresolved library.

The catalogue (`Extension`) gains a `ConfigureFlags` field (what `docker-php-ext-configure` took:
`gd` needs `--with-freetype --with-jpeg --with-webp`, `mysqli` needs `--with-mysqli=mysqlnd`, and so
on). `Packages` stays: they are the builder's `-dev` packages, now installed in a stage that is
discarded.

### 3.3 Service, unit, runtime

Per version, mirroring `nginx.Service` / `nginx.Runtime`:

- `Validate`: `current/sbin/php-fpm -t -y php-fpm.conf -c php.ini`, then `current/bin/php -c php.ini
  -r ''`, with the existing `iniTrouble` match. Both still exit 0 on a broken ini, so the output
  match stays. Runs on the host, with or without the unit active.
- `Reload`: `systemctl reload panel-php-<ver>`, which is `kill -USR2 $MAINPID`. **Refuses when the
  unit is not active** unless the service was built with `start` (a first site brings the version up).
- `Healthy`: same `MainPID` and `ActiveEnterTimestampMonotonic` across the settle window. USR2
  re-execs the master in place, so a changed PID or start time means the master died on the new config.
- Unit text:
  - `Type=notify` (step 0: `--with-fpm-systemd` works and FPM sends `READY=1`),
    `ExecStartPre` = the `-t` check, `ExecStart` = `php-fpm -F -y … -c …`
  - `Environment=PHP_INI_SCAN_DIR=:…`, `KillSignal=SIGQUIT`, `Restart=on-failure`, `LimitNOFILE`
  - the D3 sandbox. Not `NoNewPrivileges`: it would break a `sendmail` wrapper that relies on a
    setgid helper. The FPM master stays root and each pool drops to `www`
  - `[global] error_log` becomes a **file** the unit can open (for example
    `/www/panel/logs/php-<ver>-fpm.log`), listed in `ReadWritePaths=` and created before the
    unit starts. The container's `/proc/self/fd/2` cannot be opened under systemd: FPM exits 78
    before it reads a pool (step 0)
- `Runtime`: `EnsureRunning`, `Start`, `Stop` (disable + stop, remembered per version as
  `php.<ver>.stopped`), `Restart`, `Status`; writes the unit and `daemon-reload` only when the text changed.

### 3.4 What was the container's job and where it goes

| Container did | Native does |
|---|---|
| Mount only run dir, site logs, sites root, allowed roots | `ReadWritePaths=` in the unit (D3) |
| Recreate on web-root change (`ApplyMounts`) | `ApplyUnits`: rewrite units whose text changed, restart those that are running |
| `RunOnce` to read `php.ini-production` | read `versions/<id>/etc/php.ini-production` from the file |
| Boot reconcile starts `unless-stopped` containers | `systemctl enable`; the reconcile only writes the unit and starts versions that have sites |
| Container log source `docker:panel-php-<ver>` | journal source for the unit |
| Appears on the Docker page as a panel container | Appears on the Services page (`panel-php-<ver>.service` on the sysinfo allowlist) |

## 4. Steps

Each step ends with a gate. Run Go tests in WSL; run host checks on the Ubuntu sandbox (re-confirm
its IP first, it changes).

### Step 0: spike by hand (no product code)

Build PHP 8.3 with the recipe in a builder image on the sandbox, extract it, run it under a
hand-written unit behind the native nginx, serve a `phpinfo()` site.

Answer, with numbers:
- the checksum source and whether the release JSON gives a stable "newest 8.3.x" lookup
- build time for core, and for one shared extension on top of a cached core
- prefix size lean, and with `intl` + `gd` + `imagick`
- `Type=notify` vs `simple`
- `ProtectSystem=strict` with an allowed root under `/home` (does `ReadWritePaths=` win over `ProtectHome`?)
- `InaccessiblePaths=` on the panel's data directory (confirm its path from `install.Layout`)
- relative RUNPATH on a shared extension (`$ORIGIN/../../..`)
- `opcache` as `zend_extension=opcache`

**Gate:** a one-page result and a go/no-go on D2 and D3. Delete the spike afterwards.

#### Step 0 result (2026-11-19, Ubuntu 26.04.1 sandbox, 4 vCPU)

Spike scripts: `scripts/spikes/native-php/{Dockerfile,run.sh}` (untracked, throwaway). The
sandbox had been wiped by a reboot, so the box was rebuilt from nothing; Docker 29.9.3 was
already there.

| Question | Answer |
|---|---|
| Checksum source | `https://www.php.net/releases/index.php?json&version=8.3` returns the newest 8.3.x (`version`) and a `sha256` per tarball in `source[]`. `?version=8.3.28` pins one release. Stable enough to build on; the `?json` parameter is required |
| Core build | **300 s** (`configure` + `make -j4` + `make install`) on 4 vCPU |
| One extension, core cached | **3–5 s** for bundled `opcache`/`mysqli`/`pdo_mysql`; **11 s** `gd`, **28 s** `intl`, **26 s** `imagick` (PECL) |
| Prefix, lean | **74 MB** (core + opcache, mysqli, pdo_mysql), of which **30 MB** is bundled libraries |
| Prefix, + intl + gd + imagick | **131 MB**, of which **87 MB** is bundled libraries (ICU, ImageMagick, their dependencies) |
| `Type=notify` vs `simple` | **`notify` works.** `--with-fpm-systemd` links libsystemd, FPM sends `READY=1`, and systemd shows `Type=notify NotifyAccess=main` with the master as `MainPID`. Use it: a pool that fails to start is then a failed unit, not a started one |
| `ReadWritePaths` under `/home` | **`ProtectHome=true` wins over `ReadWritePaths=`** — FPM fails with `the chdir path '/home/testsite' does not exist or is not a directory`. `ProtectHome=read-only` and `ProtectHome=false` both work. D3 must use `read-only`, not `true` |
| `InaccessiblePaths=` on panel data | **Works.** With `/www/panel/data` listed, a world-readable `panel.db` is unreadable from the pool (`panel-data DENIED`), and the path is hidden even when it exists |
| Relative RUNPATH | **Works.** `opcache.so` carries `$ORIGIN/../../..`, `php-fpm` carries `$ORIGIN/../lib`, and `ldd` finds no unresolved library |
| `opcache` as `zend_extension` | **Works**; `php -m` lists it under `[Zend Modules]` |
| `PHP_INI_SCAN_DIR=:<conf.d>` | Works: a `memory_limit` in `conf.d/zz-panel.ini` is seen with the env var and not without |

Serving end to end: the unit starts under systemd, native nginx on `:8080` proxies to the
pool socket, `GET /` returns the `phpinfo()` page, a write into the pool's own root succeeds,
a read of panel data is denied, and `systemctl reload` (USR2) keeps the same `MainPID`.

**D2: go, with one correction.** Extensions as shared `.so` files on a core built once is
right — a single extension is seconds, not a recompile. But **the "core layer stays cached
when the extension list changes" premise is wrong**: BuildKit invalidates every layer after an
`ARG` when that argument's value changes, whether or not the layer reads it. Measured on a
minimal Dockerfile: changing an unused `ARG` re-ran both later `RUN` layers. Since the
extension list is a build argument, **every extension change recompiles the core (~5 min)**.
To get the caching D2 assumes, the extension list must not be a build argument: either the
Dockerfile is generated with the list baked into the extension layer (the plan's existing
`DockerfileFor` pattern, and how `internal/php` works today), or one `.so` per layer with a
fixed extension sequence and the core in a separate, argument-free stage.

**D3: go, with two corrections.** `ProtectSystem=strict` + `ReadWritePaths=` + `PrivateTmp` +
`InaccessiblePaths=` gives the container's confinement. Two things the plan did not expect:

1. `ProtectHome=true` breaks any allowed web root under `/home`; use `ProtectHome=read-only`.
2. **`[global] error_log = /proc/self/fd/2` does not work under systemd.** Neither does
   `/dev/stderr`: FPM exits 78 with `failed to open error_log (...): No such device or address`
   before it reads a pool, because systemd hands the unit a journal socket, not a tty. The
   native runtime must log to a **file** under the panel's log directory, and that file must be
   in `ReadWritePaths=` **and exist before the unit starts** (systemd refuses the unit with
   `226/NAMESPACE` otherwise). This changes the plan's claim that the existing
   `error_log = /proc/self/fd/2` "goes to the journal under systemd": it does not, and
   `php-fpm.conf` has to be rendered differently for the native runtime.

Other findings worth carrying into `internal/php/builder.go`:

- PHP has no `--with-ld-opt` (that is nginx's flag); pass `LDFLAGS=-Wl,-rpath,...`.
- The builder needs the `file` package (`phpize` calls `/usr/bin/file`).
- The build's own `php-fpm -t` resolves `user = www`, so the **builder image must carry the
  `www` account** (1500:1500), as the official image carries `www-data`.
- `--with-sodium=shared` builds `sodium.so` but **nothing enables it**; the official image has a
  `conf.d` file for it. Without one, `php -m` silently differs from the container's list, which
  is exactly what step 2's gate compares.
- Bundling must glob only regular files (`find "$P/lib" -maxdepth 1 -type f`); a bare `lib/*`
  hits the `lib/php` directory and `patchelf` fails.
- The official `php:8.3-fpm` image is **704 MB**; the plan's "~680 MB" was close.

### Step 1: lift the shared native pieces out of `internal/nginx` (behaviour-preserving)

Move into a new package (working name `internal/native`): `Runner`/`ExecRunner`, `Status` +
`status()` + `stable()`, host matching (`ReadHost`, `BuilderImage`, `ErrUnsupportedHost`),
`ExtractBuild`, `dirStore` (parametrised by versions dir and the marker file that says a build exists),
`readLink`/`setLink`, and a generic `DockerBuilder` (name, Dockerfile function, prefix dir). nginx
becomes a client of it.

**Gate:** every existing nginx test passes unchanged, and smoke 1, 7 and 8 pass on the sandbox as
before. No PHP change in this step.

#### Step 1 result (2026-11-19, commit `3c3e2b9`)

`internal/native` holds `Runner`/`ExecRunner`/`ExitCode`, `Unit` + `StatusOf` + `Stable` +
`Running`, `Host`/`ReadHost`/`BuilderImage`/`ErrUnsupportedHost`, `ExtractBuild`/`BuildVersion`,
`Store` + `ReadLink`/`SetLink`, and a generic `DockerBuilder`. `internal/nginx` keeps its exported
surface as thin wrappers and type aliases, so no caller and no existing test changed. Gate: `go
build`, `go vet`, `go test ./cmd/... ./internal/...` all green; `internal/php` untouched.

Three things the step description missed, all now parameters rather than nginx constants: the
product label is needed in `BuilderImage`'s `ErrUnsupportedHost` text and in the store's link
errors, not only in the unit name; `BuildVersion` takes the version-file path rather than `Paths`;
and `ExtractBuild`'s "is this a real build" check is a list of required paths, not a hardcoded
`sbin/nginx`.

The smoke 1/7/8 clause was not run: the sandbox was in step 0 spike state (distro nginx, no panel
install, no `panel-*` units), so there was no panel to smoke. It moves to step 8, where a real
install happens anyway.

### Step 2: PHP builder and recipe, 8.3 only, minimal extension set

`internal/php/builder.go` + `builder.Dockerfile`; `DockerfileFor` golden-file tests; `Paths` gains
`VersionsDir`, `BuildDir(id)`, `CurrentLink`, `Binary`, `CLIBinary`. Extensions in this step:
the official core set plus `opcache`, `mysqli`, `pdo_mysql`.

**Gate:** a build on the sandbox produces a prefix that passes its own end-of-recipe checks, and
`php -m` matches the official `php:8.3-fpm` module list for the same set.

#### Step 2 result (2026-11-20, commit `15caa31`)

Built on the sandbox with the real builder: **prefix 74 MB** (30 MB of it bundled libraries), core
compile **344 s**, whole build **6m19 s**, PHP **8.3.37**, `ldd` reports **0** unresolved
libraries, and `php -m` is **36 modules against the official image's 34** — nothing missing, and
exactly `mysqli` and `pdo_mysql` extra (`opcache` is already in the official list).

Four corrections to the plan:

1. **D2's caching premise is wrong a second time, and the step 0 correction did not catch it.**
   The build ID is compiled into `--prefix` inside the *core* configure line, because
   `extension_dir` and the scan dir must point at the build that loads them. So every build
   recompiles the core, even with an identical extension list. Step 0's "3-5 s with the core
   cached" came from reusing `BUILD_ID=spike`, which is not what a real build does. Buying the
   cache back needs a fixed staging prefix plus a runtime `extension_dir` override, and that
   trades away candidate isolation. Isolation was kept: a candidate that silently loads the
   serving build's `.so` files is the failure the risk table warns about. **Every build is ~6
   minutes, not seconds.** Step 7 must budget for that three times over.
2. **`mysqli` and `pdo_mysql` had no `Configure` entries**, contrary to the plan's assumption that
   they were already there.
3. **The catalogue field is `Configure`, not `ConfigureFlags`.** It already held the
   `docker-php-ext-configure` arguments and now also drives the native `./configure` line. No new
   field.
4. **A `Paths` covers every version**, so id-bearing paths take both: `BuildPrefix(ver, id)`, not
   the plan's `BuildDir(id)`. The name `BuildPrefix` avoids colliding with the existing
   `BuildDir(ver, id)`, which is the Docker build context and stays until step 4 retires it.

Two defects the worker hit and fixed, both now pinned by tests: a `FROM` without `AS build` breaks
`COPY --from` on the classic builder the panel uses (BuildKit is not guaranteed on a host), and
per-extension `-dev` packages must be installed in that extension's own layer or they invalidate
the core layer.

### Step 3: PHP `Service`, `Runtime` and unit

`internal/php/service.go` (rewritten), `runtime.go`, unit text with the D3 sandbox, the
`php.<ver>.stopped` flag. The Docker `Spec` stays until step 9.

**Gate:** unit tests with a fake `Runner` (start, stop, reload refused when inactive, unit rewritten
only on change, `Healthy` catches a restarted master), plus one real start of the 8.3 unit on the
sandbox behind native nginx.

#### Step 3 result (2026-11-20, commit `7c862f5`)

`internal/php/runtime.go` is new (`EnsureRunning`/`Start`/`Stop`/`Restart`/`Status`/`BuildPresent`,
the `php.<ver>.stopped` flag, `UnitText` with the D3 sandbox); `service.go` is rewritten as the
native confedit service over `native.Runner`/`Unit`/`Stable`; `native` gained `RunEnv` so both
validators carry `PHP_INI_SCAN_DIR`; `fpmConfTemplate` and its golden now write `error_log` to the
real FPM log file instead of `/proc/self/fd/2`.

Three things the step description did not anticipate: the manager's `swap` and `ApplyMounts` had to
keep asking Docker directly (they are the container path until step 4, and rewriting them twice
would have been waste); the FPM log file's `chown www:www` must be skipped when the panel does not
run as root; and `Healthy` reads the unit three times, so a fake runner for the restart test has to
answer three times.

Sandbox evidence: `Type=notify` active with `StatusText=Ready to handle connections`, both pool
sockets present, HTTP 200 with PHP 8.3.37, the FPM log growing, panel data denied and hidden from
the pool, a reload keeping the same `MainPID`, and a probe unit re-confirming that
`ProtectHome=true` breaks a `/home` web root while `read-only` works.

### Step 4: `Manager` and its callers

- `Manager`: family hooks become build (native builder), check (host binaries, as
  `service.Validate`, plus the `php -m` list), swap (flip link, restart if active, put back on
  failure, as `nginx.Images.swap`). `productionIni` reads the file. `Remove` stops, disables,
  removes the unit and the builds, keeps config and settings (as today). `Reconcile` also fixes the link.
- `sites.PHPRuntime`: `EnsureContainer` becomes `EnsureRunning`, with its fakes.
- `ApplyMounts` becomes `ApplyUnits`, called from the web-roots subscriber in `cmd/panel/nginx.go`.
- `httpapi/handlers_php.go`: `container` is filled from systemd state and keeps its JSON name,
  as nginx's did.
- `cmd/panel/m7.go`: `panelContainers` stops listing PHP; a journal source per version replaces it.
- `internal/sysinfo`: `panel-php-<ver>.service` on the systemd allowlist. The list is a static
  table today ([services.go:160](../../internal/sysinfo/services.go) holds `panel-nginx`), and PHP
  versions come and go with the `php.versions` setting, so this needs a provider that is asked on
  each call, as the container names are.
- `php_versions.image`/`digest` are reused (release string and tarball sha256). **No schema change.**

**Gate:** `go test ./cmd/... ./internal/...` passes in WSL.

#### Step 4 result (2026-11-20, commit `cedee93`)

The manager's build hook calls the native builder, the check runs the candidate's own
`php-fpm`/`php` on the host against the live configuration, the swap flips `CurrentLink` and
restarts a running unit with nginx's revert-on-failed-restart, `productionIni` reads the serving
build from disk, `Remove` retires the unit and deletes builds while keeping config and settings,
`Reconcile` repairs the link. Callers moved with it: `sites.PHPRuntime.EnsureRunning`, `ApplyUnits`
on the `sites.web_roots` job, `/api/php`'s `container` row filled from `native.Status` under its
old JSON name, `panelContainers` lists nothing, each version's log is a journalctl source, and the
sysinfo allowlist takes `panel-php-<ver>.service` from a per-call provider with `noStop`.
`php_versions.image`/`digest` now hold the compiled release and the tarball sha256. No schema
change.

Proven on the sandbox through the manager's hooks: build A (6 min), then build B with `intl`
(6m37s), B swapped under the running unit (link moved, new MainPID, `intl` in `php -m`), then
Activate rolled back to A (link moved back, new MainPID, `intl` gone).

Two notes for later steps: the probe ran against a fresh catalogue because the sandbox's `panel.db`
was a placeholder from reprovisioning — **step 5's upgrade path is untested against a real
Docker-era database row**; and the integration-tagged tests still exercise the old container path
until step 8 rewrites them.

### Step 5: upgrade a host that ran PHP in containers

Like `retireNginxContainer`: right before the first native start of a version, stop and remove
`panel-php-<ver>` **only if it carries the `panel.managed` label**, else fail and say so. The
container keeps serving while the native build compiles. Config files and pool sockets are the
same paths, so nothing is converted; the new master recreates the sockets. Expect one short gap per
version (nginx answers 502 for that moment). Old `panel/php:*` images are left on disk; the
catalogue pruner or `docker rmi` removes them.

**Gate:** upgrade the sandbox, which has PHP sites on Docker. The sites keep answering after the
swap; no `panel-php-*` container remains.

#### Step 5 result (2026-11-20, commit `c3130c3`)

`php.Runtime.WithRetire` fires right before the first `systemctl start` of a version, wired through
`Manager.runtime()`, both confedit services, and `cmd/panel`'s `retirePHPContainer` over the Docker
client. A container with the managed label is stopped and removed; one without it is refused with
"a container the panel did not make"; no container and a dead Docker are no-ops. Covered by fakes in
`cmd/panel/retire_test.go` and `internal/php`.

The sandbox gate ran as a **real upgrade, nothing faked**: `main@66dd38c` was built from source and
installed, PHP 8.3 was pulled the container way, site `up5.test` answered 200 from the
`panel-php-8.3` container, then this branch was installed with `panel upgrade`. After: the site
still answered 200 with PHP 8.3.37, `docker ps -a` shows no PHP container, the unit is active
`Type=notify`, `current` names a real build, `journal:panel-php-8.3.service` appears in
`GET /api/logs`, and the old `panel/php:*` image is left on disk. The retire log line and the unit
start are about one second apart — that is the 502 gap, and it is smaller than feared.

Two findings for steps 8 and 9:

1. **The first native start happens at the panel restart after the build finishes, not during it.**
   The boot reconcile runs only at daemon start: an upgrade marks the version absent by design, the
   ~6-minute native build runs while the container keeps serving, and the next boot reconcile
   retires the container and starts the unit. Any wording that implies the swap is instantaneous at
   upgrade time is wrong.
2. A placeholder `panel.db` left by a sandbox reprovision must be removed before `panel install`,
   or the install fails with "file is not a database". Sandbox hygiene, not product behaviour.

### Step 6: full extension catalogue

Fill `Configure` for all entries; build each alone and the whole set together; check
`php -m`. The libraries that need care are `intl` (ICU), `imagick` (ImageMagick), `ldap`, `gd`
(freetype, jpeg, png, webp) and `memcached`.

**Gate:** each extension loads from a native 8.3 build. Sizes recorded in this document.

#### Step 6 result (2026-11-20)

The catalogue is **26 entries**, not the 25 this step text said: `mbstring` is compiled into the
core and `sodium` is built shared by the core and enabled by its conf.d file, so neither gets a
layer. The full-set build on the sandbox compiled all 26 with **no PECL casualty** — every entry,
PECL included, builds on 8.3 today, so no official-container comparison was needed.

Recipe and catalogue corrections, all pinned by tests and goldens:

1. **`xdebug` loads as a `zend_extension`**, like opcache; `extension=xdebug` does not load
   xdebug 3, and the build's own `php -m` check would have failed the whole build.
2. **PECL installs run under `yes ''`**, not `printf '\n'`: memcached alone asks five configure
   prompts, and one newline only answered the first.
3. Catalogue `Packages` corrections: `ldap` needs `libldap-common` beside `libldap2-dev`;
   `mongodb` needs `pkg-config` beside `libssl-dev`; `xsl` needs `libxml2-dev` beside
   `libxslt1-dev`. All names verified present on ubuntu:26.04. ldap's
   `--with-libdir="lib/$(uname -m)-linux-gnu"` flag is a deliberate shell expansion and works
   as-is natively.
4. Each extension layer now emits a `TIMING <name> <seconds>` line, matching the core's.

Evidence, on the native 8.3 recipe (sandbox, ubuntu:26.04 builder, 4 vCPU):

- **Full set, all 26 together:** `php -m` lists every catalogue module (`Zend OPcache` under
  `[Zend Modules]` included), `ldd` finds no unresolved library, `php-fpm -t` passes, 62 modules
  total. Wall **11m41s**, of which the core is ~344 s. Prefix **142 MB**, of which **98 MB** is
  bundled libraries (lean build: **74 MB**, 29 MB of libraries — ICU and ImageMagick dominate
  the difference).
- **Per extension, from the recipe's TIMING lines, full set (seconds):** mongodb 175, xdebug 32,
  redis 23, intl 21, imagick 16, apcu 13, gd 10, igbinary 9, memcached 9, soap 7, ldap 6,
  pgsql 6, bcmath 6, zip 6, mysqli 5, pcntl 5, pdo_pgsql 5, calendar 5, sockets 5, bz2 4,
  exif 4, opcache 4, pdo_mysql 4, xsl 4.
- **Alone:** every entry also built in its own small group (nine groups, the core recompiling in
  each per the step 2 caveat, 6-7 min per group): apcu 21, igbinary 9, redis 33, memcached 19,
  mongodb 182, imagick 17, xdebug 47, gd 10, intl 22, ldap 6, soap 8, xsl 5, bz2 4, calendar 5,
  exif 4, mysqli 5, opcache 4, pcntl 5, pdo_mysql 4, pdo_pgsql 5, pgsql 6, sockets 6, zip 6 —
  every group passing the recipe's own end-of-recipe checks, which include `php -m` naming every
  requested module. Alone-times run a little longer than full-set ones (builder-cache warm from
  the shared base layers in the full run).

Probe builds went to a scratch prefix and were deleted after measuring; the serving panel on the
sandbox was untouched throughout (site on :80 answered 200 during the builds). Sandbox note:
the classic builder leaves every finished layer as a dangling image under the containerd
snapshotter — `docker image prune -f` reclaimed 19 GB after nine builds.

### Step 7: PHP 8.4 and 8.5, default versions

`DefaultVersions` becomes `8.3,8.4,8.5`. Run steps 2-6 checks for 8.4 and 8.5. Record what differs
(for example PECL packages that do not compile on 8.5, and the OPcache point in section 3.2).

**Gate:** all three versions install, serve a site, switch a site between versions, and roll back a build.

#### Step 7 result (2026-11-20)

`DefaultVersions` and the `php.versions` setting default are now `8.3,8.4,8.5`. An 8.2 entry a host
still lists is built natively by the same recipe (D5); nothing keeps running in Docker.

**OPcache in the 8.5 core: confirmed.** The official `php:8.5-fpm` image ships **no
`opcache.so`** — its extension dir holds only `sodium.so` and its `conf.d` holds no opcache line —
yet `php -m` lists `Zend OPcache` and `opcache_get_status()` exists. The 8.4 image still ships
`opcache.so`. So the recipe builds the opcache layer for 8.3 and 8.4 and **skips it for 8.5**
(`opcacheInCore`, series ≥ 8.05), pinned by a golden file and a series test. The end-of-recipe
module check still asks for `Zend OPcache`, so a build whose core did not carry it fails loudly
instead of shipping without a bytecode cache.

**The three versions on the native recipe** (sandbox, ubuntu:26.04 builder, 4 vCPU):

| Version | Release | Build | Prefix | Modules | vs official |
|---|---|---|---|---|---|
| 8.3 (step 6) | 8.3.37 | 11m41s full | 142 MB (74 MB lean) | 62 | +`mysqli`, `pdo_mysql` |
| 8.4 minimal | 8.4.28 | 6m29s | 74 MB | 36 | +`mysqli`, `pdo_mysql` |
| 8.5 full catalogue | 8.5.13 | 30m6s | 157 MB | 60 | +the 24 catalogue modules |

`php -m` compared against `docker run php:8.4-fpm php -m` and `php:8.5-fpm php -m` for the same
set: nothing missing on either, and exactly `mysqli` and `pdo_mysql` extra on 8.4. On 8.5 the
extra modules are the whole catalogue, which the official image does not carry.

**PECL on 8.5: no casualty.** All six PECL entries (`apcu`, `igbinary`, `imagick`, `memcached`,
`mongodb`, `redis`, `xdebug`) compiled and load on 8.5. One **transient** failure is worth
recording: the first 8.4 full-set build died at `pecl install redis` with
`No releases available for package "pecl.php.net/redis"` — a channel lookup failure, not a
compile error, and not version-specific. The same command on the official `php:8.4-fpm`
container succeeded minutes later (redis 6.3.0), and so did the retried 8.4 build. No recipe
change was made for it.

**Gate:** 8.4 and 8.5 installed through the panel API, the site `up5.test` switched
8.3 → 8.4 → 8.5 → 8.3 with HTTP 200 and the phpinfo version string at each step, both units
`active`, and a second 8.4 build rolled back to the one that was serving.

### Step 8: tests and smoke

- `scripts/smoke/2-php.sh` (261 lines): container assertions become systemd and file assertions
- `scripts/smoke/8-docker-mgmt.sh`: the PHP image parts
- `internal/php/images_integration_test.go`, `internal/sites/php_integration_test.go`: they build a
  version; adapt them (the nginx integration tests are in the same state, see the nginx doc, section 4)
- **A reboot test on a real VM**, which nginx never had: both units `enabled`, sites answer after boot

**Gate:** smoke 1, 2, 7 and 8 pass on the sandbox. Reboot passes.

#### Step 8 result (2026-11-20)

`scripts/smoke/2-php.sh` asserts the native reality: `systemctl is-active panel-php-<ver>.service`
for the running version, `test -x .../current/sbin/php-fpm` for the build (a directory, not an
image), `getent passwd www` for the account, `ps` for the workers (as `www`) and the master (as
root), and `systemctl stop` for the "removed by hand" step, which the daemon restart restores.
The `docker inspect` network-mode check is gone: a native process has no network namespace. The
script installs 8.3 and 8.4 itself (a build compiles the core: ~6 min each) and leaves both builds
and units in place.

`scripts/smoke/8-docker-mgmt.sh` keeps its build/swap/rollback checks but reads the serving build
natively: `php -m` from `current/bin/php`, the master's build from `/proc/<MainPID>/exe`, and the
catalogue's current id against the master's. The `panel/php:*` image-count assertion is removed:
native builds are directories, so an image count says nothing. Both headers now say PHP is not a
Docker workload; Docker stays the compiler.

The two `-tags integration` tests were rewritten against the native builder and the systemd
runtime. `TestRealBuildLifecycle` (`internal/php`) compiles build A, starts the unit, rebuilds with
`intl` and `redis` and checks the master restarted onto the new build, that a bogus build leaves the
unit alone, and that an activation puts A back; `TestRealPHPEndToEnd` (`internal/sites`) runs the M2
gate end to end and now proves a hand-stopped unit comes back on the boot reconcile. Both skip,
with a reason, off a host that cannot run them (no root, no systemd, or the unit already installed),
so they compile and vet everywhere but only run on a sandbox-like host.

**Sandbox evidence (10.10.11.106, Ubuntu 26.04, native PHP 8.3/8.4/8.5 installed):** all four
smokes pass — **1: 68/68**, **2: 72/72**, **7: 150/150**, **8: 37/37**. Smoke 2 includes the reboot
clause this step adds: after the reboot `panel.service` and `panel-php-8.4.service` are both
`enabled` and `active` and the site answers PHP 8.4; the VM then came back a second time (smoke 1's
reboot) with `panel`, `panel-nginx` and all three PHP units active and enabled. Smoke 8's swap of
the PHP build under load lost 0 of 1385 requests, and the nginx update plus rollback 0 of 1337.

Two smoke corrections, both the check being wrong about the product, not the product:

1. Smoke 2 read `nginx/includes/<domain>/php.conf` for the pool socket. A site created today keeps
   it in the site file's `# >>> pb:php` block; `php.conf` is only the pre-block layout. The check
   now reads the block.
2. Smoke 2's reboot step asked `systemctl is-active panel` before waiting for the panel to answer,
   so it could catch the unit mid-start. It now waits for `/api/health` first.

Smoke 1 carried older staleness (not from this step): step 5 removed an include *line*, but the
layout is `# >>> pb:<name>` blocks now, so nothing detached; and step 9 expected a deleted host to
drop the connection, but it gets the landing page (HTTP 200). Both updated; the product's
behaviour (refuse to edit a detached feature; landing page for an unclaimed host) is unchanged.
Smoke 1's run after those fixes is **68/68**, its reboot included.

The integration tests do not run on WSL: it is a non-root shell without sudo, so both skip with
their reason before touching anything. They need a sandbox-like host (root, systemd, the `www`
account, Docker) and take ~12 minutes each (two core compiles). That is the same shape as the
nginx integration tests, which still do not compile under `-tags integration` — a separate, older
gap this step did not touch (the nginx doc, section 4, open work 1).

### Step 9: remove the Docker PHP runtime, UI, docs

Delete `Spec`, the PHP use of `docker.Ensure`, the container-only `Engine` methods, and
`LegacyImage` adoption once no host needs it. PHP page wording: "Pull" becomes "Install", "image"
becomes "build". Write `docs/design/native-php-runtime.md` as the handover, like nginx's.

**Gate:** `go vet`, lint (`golangci-lint --max-same-issues=0`), web tests, and a UI check in a browser.

## 5. Risks

| Risk | Handling |
|---|---|
| Step 1 touches proven nginx code | Behaviour-preserving, nginx tests unchanged, smoke 1/7/8 re-run before anything PHP lands |
| An extension needs a library that is not on the host and not bundled | The recipe ends with an unresolved-library check; every extension is tested alone in step 6 |
| `php.ini` (operator's copy) names a build path (`extension_dir`) | The candidate check runs the new binary against the live `php.ini`; a hard-coded path fails the check and the swap does not happen |
| A confinement setting breaks an allowed root (`/home`, odd mounts) | Step 0 tests it; the list in `ReadWritePaths=` is the same allowlist the container mounted |
| Sessions and uploads in `/tmp` vanish on a restart | `PrivateTmp` behaves like the container's `/tmp`, which also died on recreate. Parity, noted in the UI text |
| Gap while swapping container to native | One short gap per version, documented; same as nginx |
| EL hosts | `ErrUnsupportedHost` as for nginx; M9 adds the `dnf` builder, SELinux labels for `versions/*`, and the socket context nginx needs to connect |
| PECL unpinned | D4; unchanged from today |

## 6. Out of scope

- Database runtime (stays Docker)
- EL support (M9)
- A PHP CLI on PATH, composer integration, per-site CLI version (D6)
- Per-site FPM users (pools still run as `www`)
- PECL version pinning (D4)

## 7. Open questions for the operator

1. D1-D6 above: any you want changed?
2. Is a 5-8 minute first build per version acceptable, given three versions (the same gap nginx has)?
3. Should the first native build also be offered for 8.2 on hosts that list it (D5), or should those hosts be told to move sites first?
