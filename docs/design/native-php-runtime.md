# Native PHP-FPM runtime — design and handover

Status: **built and proven on Ubuntu 26.04 (sandbox `10.10.11.106`, 2026-11-20).**
Branch: `native-php-runtime`. EL (Rocky/RHEL) support is deferred to **M9**, as it is for nginx.

## 1. What changed and why

PHP-FPM used to run in Docker containers (`panel-php-<ver>`, on `panel/php:<ver>-<build-id>` images).
It now runs **natively under systemd**, one unit per version. Docker is still used, but only as the
**compiler**: it builds PHP in a throwaway container that matches the host's distro, the finished
prefix is copied out to the host, and the container and image are thrown away.

The container runtime is gone from the code: no `Spec`, no image reference outside the catalogue's
tag format, and no `docker.Ensure`/`Exec`/`RunOnce`/`ContainerKill` in the PHP path. What remains of
Docker in PHP is `native.BuildEngine`.

Decisions taken with the operator (plan §2):

| Decision | Choice |
|---|---|
| Replace or sit beside the container runtime | **Replace** it |
| How extensions are built | PHP core once, **every extension as a shared `.so`** built with `phpize`/`pecl` |
| Confinement | Container parity: `ProtectSystem=strict` + `ReadWritePaths=`, `PrivateTmp`, `InaccessiblePaths=` |
| PECL pinning | Unpinned, as before (D4) — a later supply-chain step |
| A `php` on PATH | **No** (D6): the binary is `/www/panel/php/<ver>/current/bin/php` |
| Versions | **8.3, 8.4, 8.5** by default; any version the `php.versions` setting lists is built natively |

## 2. How it works

### 2.1 On disk

```
/www/panel/php/<ver>/
  php-fpm.conf  php.ini  conf.d/zz-panel.ini  pool.d/<pool>.conf      (config, unchanged)
  versions/<build-id>/            one directory per build, never written in place
    sbin/php-fpm                  the FPM master
    bin/php                       the CLI, for composer and cron
    lib/                          bundled shared libraries (not the glibc core), RUNPATH=$ORIGIN/../lib
    lib/php/extensions/<api>/*.so shared extensions, RUNPATH=$ORIGIN/../../..
    etc/php.ini-production        what php.ini is first copied from
    etc/conf.d/*.ini              one line per shared extension, scanned first
    version                       the release compiled (8.3.37)
    source-sha256                 the tarball sha256 php.net published
  current -> versions/<build-id>  relative symlink; the only thing a swap changes
/etc/systemd/system/panel-php-<ver>.service
/www/panel/run/php-<ver>-<pool>.sock      pool sockets, unchanged paths
/www/panel/logs/php-<ver>-fpm.log         FPM's own log
```

`<build-id>` is `images.NewID` (`YYYYMMDDHHMMSS-xxxxxx`), compiled in as the build's `--prefix`, so
`extension_dir` and `--with-config-file-scan-dir` point at the build that is loading them. A
candidate therefore never loads the serving build's modules.

Nothing else moved: the config tree, the pool files, the socket paths in `/www/panel/run`, the `www`
account (1500:1500), the confedit apply pipeline, the HTTP routes and every JSON shape are the ones
the container runtime had. nginx talks to the pool sockets by path and does not care what runs
behind them.

### 2.2 Build

`internal/php/builder.go` + `builder.Dockerfile` (embedded, `@NAME@` placeholders):

1. `ReadHost` reads `/etc/os-release`; `BuilderImage` maps `ubuntu`/`debian` to `<id>:<VERSION_ID>`.
   Anything else is `ErrUnsupportedHost` **before Docker is touched**.
2. The recipe downloads `php-<release>.tar.xz` and **checks it against the sha256 in
   `https://www.php.net/releases/index.php?json&version=<series>`**, then compiles with the core set
   the official image ships (`--enable-fpm --with-fpm-systemd`, curl, openssl, mbstring, sodium,
   pdo_sqlite, …).
3. One layer per extension in catalogue order: `phpize && ./configure … && make install` for bundled
   ones, `yes '' | pecl install <name>` for PECL. Each layer writes the `conf.d` line that enables it
   (`zend_extension=` for opcache and xdebug) and a `TIMING <name> <seconds>` line.
4. Libraries are bundled with `ldd` + `patchelf`, the prefix is stripped of headers/man/docs, and the
   recipe ends with checks: `php -v` matches, `php -m` lists every requested module, `php-fpm -t`
   passes, `ldd` finds nothing unresolved.
5. `native.DockerBuilder` creates a stopped container from the image, reads `<prefix>` out with
   `ContainerArchive`, and removes both.
6. `native.ExtractBuild` unpacks into `versions/.extract-<id>` and renames it into place, refusing
   absolute or climbing names, links that leave the build, and special files.

**A build is ~6 minutes, every time.** The build ID is compiled into the core's `--prefix`, so the
core recompiles even when the extension list is unchanged. Step 2 measured this and kept isolation: a
fixed staging prefix plus a runtime `extension_dir` override would buy back the cache at the price of
a candidate loading the serving build's `.so` files. Full-catalogue builds are longer.

The extension list is **recipe text, not a build argument**: BuildKit invalidates every layer after
an `ARG` whose value changes, so an argument would recompile the core on every extension change.

### 2.3 Catalogue, swap, rollback

`internal/images` is the shared build catalogue (rows, `current`/`previous` pointers, prune).
`internal/php/store.go` adapts it: `dirStore` implements `images.Engine` over directories — a build
"exists" while `versions/<id>/sbin/php-fpm` does. There is **no legacy image adoption**: a build is a
directory, so an image from before builds had IDs is not one. A host upgrading from the container era
gets its container retired instead (§2.5).

`Manager.swap` flips `current` (symlink + rename) and, when the unit is active, restarts it and
requires the master to hold the settle window. If it does not, the link goes back and the unit is
restarted on the old build. A stopped version only needs the link: its next start uses the new build.
Rollback is `Activate` of the previous build, which goes through the same path.

Before a candidate serves, `checkCandidate` runs **the candidate's own binaries on the host** against
the live configuration (`php-fpm -t`, `php -c php.ini -m`), so a build that would not start is
rejected before anything serves, and a `php.ini` naming a stale `extension_dir` fails its check
rather than the site.

### 2.4 Service, runtime, unit

`internal/php/service.go`:

- `Service` (a `confedit.Service`): `Validate` runs `current/sbin/php-fpm -t` and `current/bin/php -c
  … -r ''` on the host, with `PHP_INI_SCAN_DIR=:<conf.d>`; both exit 0 on a broken ini, so the output
  match (`iniTrouble`) is what catches it. `Reload` is `systemctl reload` (USR2), and **refuses when
  the unit is not active** unless the service was built with `start`, which is what a first site
  does. `Healthy` compares `MainPID` and `ActiveEnterTimestampMonotonic` across the settle window, so
  a master that died on the new config is caught.
- `Runtime`: `EnsureRunning` (writes the unit if it differs, creates the log and run dirs, `enable`,
  `start` if inactive), `Start`, `Stop` (`disable` + `stop`, remembered in `php.<ver>.stopped`),
  `Restart`, `Status`, `Apply` (rewrite, restart a running unit when the text changed).
- `UnitText`:

```
Type=notify                        --with-fpm-systemd: FPM sends READY=1
Environment=PHP_INI_SCAN_DIR=:/www/panel/php/<ver>/conf.d
ExecStartPre=…/current/sbin/php-fpm -t -y php-fpm.conf -c php.ini
ExecStart=…/current/sbin/php-fpm -F -y php-fpm.conf -c php.ini
ExecReload=/bin/kill -USR2 $MAINPID
KillSignal=SIGQUIT  KillMode=mixed  TimeoutStopSec=25  Restart=on-failure  LimitNOFILE=65536
ProtectSystem=strict  ProtectHome=read-only  PrivateTmp=true
ReadWritePaths=<run dir> <site log dir> <FPM log> <sites root> <allowed web roots>
InaccessiblePaths=/www/panel/data
```

The master stays root and each pool drops to `www`, as the container did. `ProtectHome=read-only`,
**never `true`**: `true` hides `/home` from `ReadWritePaths` too (step 0 measured it). No
`NoNewPrivileges`: it would break a `sendmail` wrapper that relies on a setgid helper. FPM's own log is
a **file** — under systemd it cannot open `/proc/self/fd/2` or `/dev/stderr` and exits 78 before it
reads a pool — so `[global] error_log` names the file, the unit may write it, and `writeUnit` creates
it first (systemd refuses a `ReadWritePaths` entry that does not exist).

### 2.5 Upgrading a host that ran PHP in containers

`Runtime.WithRetire` → `cmd/panel/nginx.go: retirePHPContainer`. Right before the **first** native
start of a version it stops and removes `panel-php-<ver>`, **only if it carries the `panel.managed`
label** (otherwise it fails and says so). Config files and pool sockets are the same paths, so nothing
is converted; the new master recreates the sockets.

**The swap happens at the panel's next restart, not during the build**: the boot reconcile runs only
at daemon start, and an upgrade marks the version absent by design. So a host upgrading runs
`panel upgrade`, the ~6-minute native build runs while the container keeps serving, and the next panel
restart retires the container and starts the unit. The gap between the retire log line and the unit
start is about one second; nginx answers 502 for that moment. Old `panel/php:*` images are left on
disk for the operator to `docker rmi`.

### 2.6 Other places that changed

| Area | Change |
|---|---|
| `internal/sites` | `Runtime` is `BuildPresent` + `EnsureRunning`; step names are `build` and `service` |
| `internal/httpapi` | Routes and JSON shapes unchanged; `/api/php`'s `container` row is filled from `native.Status` under its old name, and `image` holds the compiled release (or the series before a build) |
| `internal/sysinfo` | `panel-php-<ver>.service` comes from a per-call provider, so a version added to the setting appears without a restart |
| `cmd/panel/m7.go` | PHP is no longer a "panel container"; each version's log is a `journal:` source, and the log rotator signals it with `systemctl kill` |
| Allowed web roots | no longer bind-mounted; `ApplyUnits` rewrites each unit and restarts the versions that run |
| UI | The PHP page's **Install** (was Pull) compiles a build; "image" became "build" in the user-facing text. API paths and field names are unchanged (`/api/php/{ver}/pull`, `pulling` state) |
| Removed | `php.Spec`, `php.ContainerName`, `php.BaseImage`, `php.LegacyImage` and its adoption path, `php.Paths.BuildDir`, `php.Dockerfile`, the PHP `Engine` interface, `images.Swap`/`SwapPlan` (nothing called it), and the `ImageTag` engine member |

## 3. Verification state

Unit tests: `go test ./cmd/... ./internal/...` passes on Linux (WSL). Symlink tests are
`//go:build unix`.

Integration tests (`-tags integration`; root + systemd + `www` + Docker; ~12 minutes each because
they compile the core twice): `internal/php:TestRealBuildLifecycle` and
`internal/sites:TestRealPHPEndToEnd`. They skip, with a reason, off a sandbox-like host.

Sandbox `10.10.11.106` (Ubuntu 26.04, native PHP 8.3/8.4/8.5), 2026-11-20:

| Check | Result |
|---|---|
| Real upgrade from the container runtime (main installed, PHP pulled the container way, site on it, then this branch) | pass: site still 200 with PHP 8.3.37, no `panel-php-*` container, unit active `Type=notify` |
| `scripts/smoke/1-nginx-sites.sh` | **68 of 68**, reboot included |
| `scripts/smoke/2-php.sh` | **72 of 72**, reboot included |
| `scripts/smoke/7` | **150 of 150** |
| `scripts/smoke/8-docker-mgmt.sh` | **37 of 37**; a build swap under load lost 0 of 1385 requests |
| Reboot (twice) | `panel`, `panel-nginx` and every PHP unit `enabled` and `active`; sites answer |
| Version switch 8.3 → 8.4 → 8.5 → 8.3 and a rollback | pass, HTTP 200 and the right phpinfo string at each step |
| The PHP page in a browser (step 9) | pass: the new wording, and an **Install** row whose dialog started a `php.pull` job |

## 4. Measured numbers

| Version | Release | Build | Prefix | Modules | vs the official image |
|---|---|---|---|---|---|
| 8.3 minimal | 8.3.37 | 6m19s | **74 MB** (30 MB bundled libraries) | 36 | nothing missing; +`mysqli`, `pdo_mysql` |
| 8.3 full (26 extensions) | 8.3.37 | 11m41s | **142 MB** (98 MB libraries) | 62 | every catalogue module extra |
| 8.4 minimal | 8.4.28 | 6m29s | 74 MB | 36 | +`mysqli`, `pdo_mysql` |
| 8.5 full catalogue | 8.5.13 | 30m6s | **157 MB** | 60 | every catalogue module extra |

Per-extension times (full set, seconds): mongodb 175, xdebug 32, redis 23, intl 21, imagick 16,
apcu 13, gd 10, igbinary 9, memcached 9, soap 7, ldap 6, pgsql 6, bcmath 6, zip 6, mysqli 5, pcntl 5,
pdo_pgsql 5, calendar 5, sockets 5, bz2 4, exif 4, opcache 4, pdo_mysql 4, xsl 4. `mbstring` is
compiled into the core and `sodium` is built shared by the core, so neither gets a layer; 8.5 builds
OPcache into the core and skips the layer.

The catalogue is **26 entries**; `MaxExtensions` is 32 per build.

## 5. Known limits

1. **No EL (Rocky/RHEL).** `BuilderImage` returns `ErrUnsupportedHost` there, as for nginx. M9 needs
   a `dnf` builder, glibc matching per release, and SELinux labelling for the build prefix and the
   socket nginx connects to.
2. **No `php` on PATH (D6).** The binary is `/www/panel/php/<ver>/current/bin/php`. Composer and cron
   must name it.
3. **PECL is unpinned (D4).** `pecl install <name>` takes the latest, as the container did. A PECL
   channel lookup can fail transiently (`No releases available for package "pecl.php.net/redis"`);
   retry, do not edit the recipe.
4. **A candidate costs the core cache.** Every build recompiles the core (~6 min) because the build ID
   is in `--prefix`. Isolation was chosen over speed (plan step 2).
5. **`/tmp` is private to the unit** (`PrivateTmp=true`), as the container's `/tmp` was. Sessions and
   uploads there do not survive a restart.
6. **No per-site FPM user**, and no per-site CLI version: pools run as `www`.
7. The integration tests do not run on WSL (non-root, no systemd) and take ~12 minutes on a host.
8. Disk: each build is 74–157 MB and the catalogue keeps the current and previous builds; the classic
   builder also leaves dangling layers (containerd snapshotter), so `docker image prune -f` is worth
   running after a run of builds (19 GB reclaimed after nine builds).

## 6. Operating notes and traps

- **Where things are:** `systemctl status panel-php-8.3`, `journalctl -u panel-php-8.3`,
  `/www/panel/logs/php-8.3-fpm.log`, `readlink /www/panel/php/8.3/current`,
  `/www/panel/php/8.3/current/bin/php -v`.
- **Check a config by hand:** `PHP_INI_SCAN_DIR=:/www/panel/php/8.3/conf.d /www/panel/php/8.3/current/sbin/php-fpm -t -y /www/panel/php/8.3/php-fpm.conf -c /www/panel/php/8.3/php.ini`.
- **Roll back a build by hand:** `ln -sfn versions/<older-id> /www/panel/php/8.3/current && systemctl restart panel-php-8.3`
  (the panel's own rollback is the build's Activate action).
- **`php-fpm -t` exits 0 on a broken ini**, and so does `php -c … -r ''`: only the output match
  catches a syntax error or a missing extension.
- **Paths inside a build must stay relative to it**: `extension_dir` and the scan dir are compiled in,
  which is why a candidate is checked with its own binary.
- **`ProtectHome=true` breaks a web root under `/home`**, even one in `ReadWritePaths`. Use `read-only`.
- **FPM cannot log to `/proc/self/fd/2` under systemd.** A file, named in `ReadWritePaths`, created
  before the unit starts.
- **The first native start happens at the panel restart after the build** (§2.5).
- **Test harness:** run Go tests in WSL with a script file (`MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-26.04
  -- bash /mnt/c/…/script.sh`); inline `bash -c` breaks on Windows PATH with parentheses. Node is
  under nvm and not on PATH in a non-interactive shell; `node_modules` installed on Windows cannot
  run the Linux rollup binary, so the web gate needs a Linux-side copy and `npm ci`.

## 7. File map

| File | Role |
|---|---|
| `internal/php/builder.go`, `builder.Dockerfile` | host matching, the recipe, `DockerfileFor`, the builder |
| `internal/php/extensions.go` | the 26-entry catalogue, `ParseExtensions`, module checks |
| `internal/php/store.go` | `dirStore`: the catalogue over build directories |
| `internal/php/manager.go` | `Manager`: catalogue, build/check/swap hooks, reconcile, settings, php.ini |
| `internal/php/service.go` | `Service`: validate, reload, healthy, status |
| `internal/php/runtime.go` | `Runtime`, `UnitText`, the `php.<ver>.stopped` flag, the retire hook |
| `internal/php/paths.go` | `VersionsDir`, `BuildPrefix`, `CurrentLink`, the binary and log paths |
| `internal/php/settings.go` | the form ini, the pool template, web-root checks |
| `internal/native/{builder,store,host,unit}.go` | the shared native pieces nginx and PHP both use |
| `internal/images/{family.go,catalog.go}` | the build catalogue and its hooks |
| `cmd/panel/nginx.go` | wiring, `retirePHPContainer`, `ApplyUnits` on the web-roots job |
| `cmd/panel/m7.go` | journal log sources, `panelUnits`, the rotator's signals |
| `internal/httpapi/handlers_php.go`, `handlers_images.go` | the routes (JSON shapes unchanged) |
| `web/src/pages/PHPPage.tsx`, `components/ImageBuilds.tsx` | the PHP page: install, builds, extensions |
| `internal/php/{builder,php,service,runtime,extensions,webroots,build}_test.go`, `images_integration_test.go` | tests |
| `scripts/smoke/{1-nginx-sites,2-php,7,8-docker-mgmt}.sh` | the smoke scripts |
