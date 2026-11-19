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
| D2 | How extensions are built | **PHP core once, every extension as a shared `.so` built with `phpize`**, which is what `docker-php-ext-install` does. Keeps a rebuild cheap (core layer cached by version) and treats bundled and PECL extensions the same. Alternative (static, everything in `./configure`) means a 3-6 min full recompile for every extension change |
| D3 | How much confinement the unit gives | **Parity with the container's mounts**: `ProtectSystem=strict` with `ReadWritePaths=` = run dir, site log dir, sites root, allowed web roots; plus `PrivateTmp`, `InaccessiblePaths=` for the panel's own data. Reads outside those stay possible for the `www` user, as for any local account. Changing the allowed roots rewrites the unit and restarts the running PHP units, as it recreates the containers today |
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
  - `Type=simple` (or `notify` if the build uses `--with-fpm-systemd`; decided in step 0),
    `ExecStartPre` = the `-t` check, `ExecStart` = `php-fpm -F -y … -c …`
  - `Environment=PHP_INI_SCAN_DIR=:…`, `KillSignal=SIGQUIT`, `Restart=on-failure`, `LimitNOFILE`
  - the D3 sandbox. Not `NoNewPrivileges`: it would break a `sendmail` wrapper that relies on a
    setgid helper. The FPM master stays root and each pool drops to `www`
  - `[global] error_log = /proc/self/fd/2` already in `php-fpm.conf` goes to the journal under systemd
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

### Step 1: lift the shared native pieces out of `internal/nginx` (behaviour-preserving)

Move into a new package (working name `internal/native`): `Runner`/`ExecRunner`, `Status` +
`status()` + `stable()`, host matching (`ReadHost`, `BuilderImage`, `ErrUnsupportedHost`),
`ExtractBuild`, `dirStore` (parametrised by versions dir and the marker file that says a build exists),
`readLink`/`setLink`, and a generic `DockerBuilder` (name, Dockerfile function, prefix dir). nginx
becomes a client of it.

**Gate:** every existing nginx test passes unchanged, and smoke 1, 7 and 8 pass on the sandbox as
before. No PHP change in this step.

### Step 2: PHP builder and recipe, 8.3 only, minimal extension set

`internal/php/builder.go` + `builder.Dockerfile`; `DockerfileFor` golden-file tests; `Paths` gains
`VersionsDir`, `BuildDir(id)`, `CurrentLink`, `Binary`, `CLIBinary`. Extensions in this step:
the official core set plus `opcache`, `mysqli`, `pdo_mysql`.

**Gate:** a build on the sandbox produces a prefix that passes its own end-of-recipe checks, and
`php -m` matches the official `php:8.3-fpm` module list for the same set.

### Step 3: PHP `Service`, `Runtime` and unit

`internal/php/service.go` (rewritten), `runtime.go`, unit text with the D3 sandbox, the
`php.<ver>.stopped` flag. The Docker `Spec` stays until step 9.

**Gate:** unit tests with a fake `Runner` (start, stop, reload refused when inactive, unit rewritten
only on change, `Healthy` catches a restarted master), plus one real start of the 8.3 unit on the
sandbox behind native nginx.

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

### Step 5: upgrade a host that ran PHP in containers

Like `retireNginxContainer`: right before the first native start of a version, stop and remove
`panel-php-<ver>` **only if it carries the `panel.managed` label**, else fail and say so. The
container keeps serving while the native build compiles. Config files and pool sockets are the
same paths, so nothing is converted; the new master recreates the sockets. Expect one short gap per
version (nginx answers 502 for that moment). Old `panel/php:*` images are left on disk; the
catalogue pruner or `docker rmi` removes them.

**Gate:** upgrade the sandbox, which has PHP sites on Docker. The sites keep answering after the
swap; no `panel-php-*` container remains.

### Step 6: full extension catalogue

Fill `ConfigureFlags` for all 25 entries; build each alone and the whole set together; check
`php -m`. The libraries that need care are `intl` (ICU), `imagick` (ImageMagick), `ldap`, `gd`
(freetype, jpeg, png, webp) and `memcached`.

**Gate:** each extension loads from a native 8.3 build. Sizes recorded in this document.

### Step 7: PHP 8.4 and 8.5, default versions

`DefaultVersions` becomes `8.3,8.4,8.5`. Run steps 2-6 checks for 8.4 and 8.5. Record what differs
(for example PECL packages that do not compile on 8.5, and the OPcache point in section 3.2).

**Gate:** all three versions install, serve a site, switch a site between versions, and roll back a build.

### Step 8: tests and smoke

- `scripts/smoke/2-php.sh` (261 lines): container assertions become systemd and file assertions
- `scripts/smoke/8-docker-mgmt.sh`: the PHP image parts
- `internal/php/images_integration_test.go`, `internal/sites/php_integration_test.go`: they build a
  version; adapt them (the nginx integration tests are in the same state, see the nginx doc, section 4)
- **A reboot test on a real VM**, which nginx never had: both units `enabled`, sites answer after boot

**Gate:** smoke 1, 2, 7 and 8 pass on the sandbox. Reboot passes.

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
