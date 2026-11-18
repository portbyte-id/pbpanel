# Native nginx runtime — design and handover

Status: **built and proven on Ubuntu 26.04 (sandbox `10.10.11.106`, 2026-11-18); not committed.**
Branch: `native-nginx-runtime` (cut from `main` at `d5bbe61`). EL (Rocky/RHEL) support is deferred to **M9**.

## 1. What changed and why

nginx used to run in a Docker container (`panel-nginx`). It now runs **natively under systemd**.
Docker is still used, but only as a **builder**: it compiles nginx in a throwaway container that
matches the host's distro, and the finished files are copied out to the host. PHP-FPM still runs
in Docker, unchanged.

Decisions taken with the operator:

| Decision | Choice |
|---|---|
| Replace or sit beside the container runtime | **Replace** it |
| Flavors | **One source build** for every version: nginx.org source, OpenSSL 3.5, ModSecurity v3. The old plain/`pqc-` split is gone |
| Builder image | Matches the host distro and release, so the binary links the host's own glibc |
| EL family | Deferred to **M9** (needs a `dnf` builder and SELinux labels) |
| Supervisor | A **systemd unit**, not a panel child process |

## 2. How it works

### 2.1 On disk

```
/www/panel/nginx/
  nginx.conf, panel-main.conf, panel-http.conf, sites/, includes/, waf/   (config, unchanged)
  versions/<build-id>/            one directory per build, never written in place
    sbin/nginx
    modules/ngx_http_modsecurity_module.so
    lib/                          every shared library except the glibc core, RUNPATH=$ORIGIN
    conf/mime.types, conf/fastcgi_params
    modsec/                       unicode.mapping, modsecurity.conf-recommended, crs/rules (CRS 4.30.0)
  current -> versions/<build-id>  relative symlink; the only thing a swap changes
/etc/systemd/system/panel-nginx.service     written by the panel
/www/panel/logs/nginx-error.log             nginx's own errors (sites keep logs/sites/)
/www/panel/cache/nginx/                     nginx temp files
```

`<build-id>` is `images.NewID` (`YYYYMMDDHHMMSS-xxxxxx`). It is also compiled in as the build's
`--prefix`, which is what makes `load_module modules/...` (relative) resolve to the build that is
loading it, candidate or serving.

### 2.2 Build

`internal/nginx/builder.go` + `builder.Dockerfile` (embedded):

1. `ReadHost` reads `/etc/os-release`; `BuilderImage` maps `ubuntu`/`debian` to `<id>:<VERSION_ID>`.
   Any other ID returns `ErrUnsupportedHost` before Docker is touched.
2. `DockerfileFor` fills the `@NAME@` placeholders and Docker builds it. The recipe compiles
   libmodsecurity (no libcurl) and the nginx connector, downloads the pinned CRS, compiles nginx
   against OpenSSL 3.5, bundles the shared libraries with `ldd` + `patchelf`, and ends with checks
   (OpenSSL version, module present, CRS present, no unresolved libraries).
3. `DockerBuilder` creates a stopped container from that image, reads
   `<prefix>` out with the Engine archive API (`docker.Client.ContainerArchive`), and removes the
   container and image.
4. `ExtractBuild` unpacks into `versions/.extract-<id>` and renames it into place. It refuses
   absolute or climbing names, links that leave the build, special files, and an existing build.

A build takes roughly 5–8 minutes on the sandbox (4 vCPU). The unchanged early layers (apt,
ModSecurity, CRS) are cached by Docker between builds.

### 2.3 Catalogue, swap, rollback

`internal/images` (the shared PHP/nginx catalogue: build rows, `current`/`previous`, prune) is
unchanged. `internal/nginx/store.go` adapts it: `dirStore` implements `images.Engine` over
directories (a build "exists" while `versions/<id>/sbin/nginx` does).

`Images.swap` (called by `Family`): flip `current` atomically (symlink + rename), and if the unit
is active, `systemctl restart` and require the master to hold for the settle window. If it does
not hold, the link is put back and nginx restarted on the old build. A stopped nginx is not
touched; the boot reconcile starts it on the link. Rollback is `Activate` of the previous build,
which goes through the same path.

### 2.4 Service, runtime, unit

`internal/nginx/service.go`:

- `Service` (a `confedit.Service`): `Validate` runs `current/sbin/nginx -t -c nginx.conf`;
  `Reload` is `systemctl reload`; `Healthy` compares `MainPID` and
  `ActiveEnterTimestampMonotonic` across a settle window, which catches a master that exited and
  was restarted. `State` feeds `GET /api/services/nginx`.
- `Runtime`: `EnsureRunning` (writes the unit if it differs, creates cache/log/run dirs, `enable`,
  `start` if inactive), `Start`, `Stop` (`disable` + `stop`, remembered in the `nginx.stopped`
  setting), `Restart`.
- `UnitText`: master runs as root (binds 80/443, reads keys), workers drop to `www`;
  `ExecStartPre` runs `nginx -t`; `ExecReload` is `kill -HUP $MAINPID`; `KillSignal=SIGQUIT`;
  `Restart=on-failure`.

### 2.5 Upgrading a host that ran the container

`Runtime.WithRetire` → `cmd/panel/nginx.go: retireNginxContainer`. Right before the **first**
native start it stops and removes `panel-nginx`, **only if it carries the `panel.managed` label**
(otherwise it fails and says so). The old container keeps serving while the native build compiles,
so downtime is one short gap at the swap. The old `panel/nginx:*` Docker images are left on disk.

`UpgradeMainConf` rewrites an operator-owned `nginx.conf` generated by the container runtime:
`include /etc/nginx/mime.types` → the build's `conf/mime.types`, `error_log /dev/stderr warn` →
`/www/panel/logs/nginx-error.log`, and (older) the `return 444` catch-all → the landing page. A
line the operator edited is left alone. Panel-owned includes (`php.conf`, the WAF module loader)
are re-rendered by the reconcile.

### 2.6 Other places that changed

| Area | Change |
|---|---|
| `internal/waf` | `ModuleFile` is relative (`modules/ngx_http_modsecurity_module.so`); the CRS and `unicode.mapping` paths come from `nginx.Paths.CRSDir()/ModSecDir()` |
| `internal/sites/reconcile.go` | `Runtime` is now `BuildPresent` + `EnsureRunning`; step names are `build` and `service` (were `image`, `container`) |
| `internal/httpapi` | Routes and JSON shapes unchanged. `GET /api/services/nginx` still answers under `container` (legacy name) from systemd state; `GET /api/nginx/image` reports `base` as `nginx.BuildSource` |
| `internal/sysinfo` | `panel-nginx.service` is on the systemd allowlist and shows on the Services page |
| `cmd/panel/m7.go` | nginx is no longer a "panel container"; its error log is a file source `nginx:error`; log rotation signals it with `systemctl kill -s USR1` |
| Allowed web roots | no longer bind-mounted for nginx; only the PHP containers are recreated on change |
| Removed | the Alpine/official-image Dockerfiles, the container `Spec`, the container `Service`/`Runtime`/`Images`, `ErrNoImage`, `BaseImageFor`, `pqc.go` |

## 3. Verification state

Unit tests: `go test ./cmd/... ./internal/...` passes on Linux (WSL). Symlink-based tests are
`//go:build unix` (`internal/nginx/images_test.go`).

Sandbox `10.10.11.106` (Ubuntu 26.04.1, systemd 259, Docker 29.9.3), 2026-11-18:

| Check | Result |
|---|---|
| Upgrade from the container (old container retired, config paths rewritten, nginx active under systemd) | pass |
| `scripts/smoke/1-nginx-sites.sh` (reboot skipped) | **61 of 65**. The 4 failures are the "detached include line" step, which tests the pre-`2b1329d` site-file layout and is unrelated to this work |
| Firewall: block mode, normal request 200, XSS and SQLi probes 403 (CRS rules 942100, 949110), audit events via `/api/waf/events`, off removes the module loader | pass |
| `kill -9` on the master → systemd restarts it, sites serve | pass |
| `systemctl stop` → panel restart restores it | pass |
| Candidate check with its own binary, rollback by link flip | pass (earlier hand test and unit tests) |
| **Reboot persistence** | **not tried** — this sandbox has lost `/www` on reboot before. The unit is `enabled` |

## 4. Open work

1. **Integration tests.** `internal/sites/{boot,php,tls,waf}_integration_test.go` assume the
   container and no longer compile with `-tags integration`. WSL has systemd and Docker, so they
   can be adapted to run there (they would build nginx once, ~5–8 min).
2. **Smoke 7 and 8: done (2026-11-18).** Both run on the native runtime and pass on the sandbox:
   smoke 7 150 of 150 (nginx stopped and started through `systemctl`, `nginx:error` log source),
   smoke 8 35 of 35 (nginx build update and rollback under load, checked against the serving
   link and the master's `/proc` path). Fixes found on the way, none in the product: the
   shell-job refusal assertion expected a 403 where the run is accepted and recorded as failed
   (pinned by `TestShellJobsRequireAllowlist`); Git Bash rewrote the `q=/req-…` search argument
   into a Windows path; smoke 8 passed its module wait on a VM that already had the modules and
   moved on before the swap. Smoke 1 and 2 are updated.
3. **Smoke 1, steps 5 and the "history" check** test the old include-line layout; update them to
   the `# >>> pb:<name>` block layout (separate from this work).
4. **UI wording: done (2026-11-18).** The Nginx page's **Build** tab (was Image) shows the builds
   and their source, with no fake Docker `repo` row, no extension column for nginx builds, and a
   note that a rebuild takes 5 to 8 minutes. Status reads from systemd. Job lines say "nginx".
   API paths and field names are unchanged (`/api/nginx/container/*`, `container` in the service
   status, `repo` still sent). `docs/development-plan.md` is a historical plan and was left as
   written.
5. **Old Docker images.** `panel/nginx:*` and `nginx:1.29` stay on upgraded hosts (≈240 MB each).
   Nothing removes them; `docker rmi` by hand, or add a prune to `retireNginxContainer`.
6. **Spike leftovers.** `scripts/spikes/native-nginx/` (untracked) is superseded by
   `internal/nginx/builder.go` and can be deleted.
7. **M9: EL (Rocky/RHEL).** Needs a `dnf` builder Dockerfile, glibc matching per release,
   `semanage fcontext` for `/www/panel/nginx/versions/*/{sbin,lib,modules}`
   (`bin_t`/`lib_t`), and `httpd_can_network_connect` for PHP-FPM sockets. `BuilderImage` currently
   returns `ErrUnsupportedHost` for these hosts.
8. **Not decided:** build size is about 79 MB per build, and one previous build is kept. If disk
   matters, the catalogue's prune keeps only current and previous.

## 5. Operating notes and traps

- **Where things are:** `systemctl status panel-nginx`, `journalctl -u panel-nginx`,
  `/www/panel/logs/nginx-error.log`, `readlink /www/panel/nginx/current`,
  `/www/panel/nginx/current/sbin/nginx -V`.
- **Check a config by hand:** `/www/panel/nginx/current/sbin/nginx -t -c /www/panel/nginx/nginx.conf`.
- **Roll back a build by hand:** `ln -sfn versions/<older-id> /www/panel/nginx/current && systemctl restart panel-nginx`
  (the panel's own rollback is the Nginx page's activate action).
- **Roll back the whole feature on the sandbox:** `mv /www/panel/bin/panel.prev /www/panel/bin/panel && systemctl restart panel`
  (the previous binary was kept there; the old container image is still on disk, but the panel would
  need to be the old one to run it).
- **The first native build must not be checked against a container-era config** (it names
  `/etc/nginx/mime.types`). `Images.family` skips the check until a native build serves
  (`anyServes`); the validation after the reconcile's render is the first real test.
- **`load_module` must stay relative.** An absolute path through `current` would make a candidate
  load the serving build's module and fail with a binary-compatibility error.
- **glibc core is never bundled.** The host's own is used, which is why the builder must match the
  host's distro and release.
- **systemd and `Restart=on-failure`:** an operator stop via the panel also disables the unit so a
  reboot does not undo it; a stop via `systemctl stop` alone leaves it enabled and the next panel
  start brings it back.
- **Test harness:** run Go tests in WSL (`MSYS_NO_PATHCONV=1 wsl -e bash -lc 'export PATH=/usr/local/go/bin:/usr/bin:/bin ...'`);
  wait loops that grep a journal window can match an earlier failure, so use an absolute `--since`.
- **Sandbox state right now:** panel binary replaced (`panel.prev` kept), native nginx active,
  build `20261118122719-f5e473`, my test unit `panel-nginx-native` removed, old container retired,
  7 old `panel/nginx:*` images still present.

## 6. File map

| File | Role |
|---|---|
| `internal/nginx/builder.go`, `builder.Dockerfile` | host matching, recipe, `DockerBuilder` |
| `internal/nginx/extract.go` | safe unpack of a build archive |
| `internal/nginx/store.go` | `dirStore`, `readLink`/`setLink`, `idOfTag` |
| `internal/nginx/images.go` | `Images`: build, check, swap, rollback, reconcile, `SetVersion` |
| `internal/nginx/service.go` | `Service`, `Runtime`, `UnitText`, `Runner` |
| `internal/nginx/paths.go` | `VersionsDir`, `BuildDir`, `CurrentLink`, `Binary`, `ModSecDir`, … |
| `internal/nginx/upgrade.go` | `UpgradeMainConf` |
| `internal/docker/archive.go` | `ContainerArchive` |
| `cmd/panel/nginx.go` | wiring and `retireNginxContainer` |
| `internal/nginx/{service,images,extract,builder,upgrade}_test.go`, `cmd/panel/retire_test.go` | tests |
| `scripts/smoke/1-nginx-sites.sh`, `2-php.sh` | updated smoke scripts |
