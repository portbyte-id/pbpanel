# Runbook

<!-- AUTO-GENERATED from install.sh, cmd/panel/commands.go, internal/install (unit names), handlers_health.go and the design docs. Regenerate with /ecc:update-docs rather than editing by hand. -->

Operations for a PB Panel host. The panel is a single static Go binary running as root under a
hardened systemd unit; nginx and PHP-FPM run natively under their own units, and Docker is used only
to compile them.

## Deploy

### Install (first time)

```sh
export GITHUB_TOKEN=<token with read access>
sudo -E sh install.sh
```

The installer copies the `pbp` binary into `/www/panel/bin/` and runs `pbp install`, which creates
the `/www` tree, the `www` user, `secret.key`, the SQLite database, a self-signed panel certificate,
and the systemd units, then starts the panel. Re-running it repairs an existing install and never
overwrites the database or password.

| Command | Effect |
|---------|--------|
| `sudo -E sh install.sh` | Install or upgrade to the latest release |
| `sudo -E sh install.sh --version v0.2.0` | Install a specific release |
| `sudo -E sh install.sh --source --ref main` | Build a branch, tag or commit on the host |
| `sudo -E sh install.sh --force` | Reinstall even when already current |

### Upgrade

```sh
sudo -E sh install.sh              # fetches the new release and runs `pbp upgrade`
```

`pbp upgrade` copies the new binary beside the old, checks migrations, atomically renames, restarts
the unit, and rolls the binary back if the health check does not pass within 30 s.

### Release

Pushing a `v*` tag runs the release workflow, which builds the binaries and the `SHA256SUMS` that
`install.sh` downloads.

## Health checks and monitoring

| Check | How |
|-------|-----|
| Panel serving | `GET /api/health` → `{"status":"ok","version":"<build>"}`. No session needed; this is what `pbp upgrade` polls. |
| Daemon state | `systemctl status panel` |
| Unit names | `panel.service`, `panel-supervisord.service` (apps), `panel-firewall.service` (ruleset at boot), and `panel-nginx.service` / `panel-php-<ver>.service` for the native runtimes. |
| nginx | `/www/panel/nginx/current/sbin/nginx -t -c /www/panel/nginx/nginx.conf`; log `/www/panel/logs/nginx-error.log`; `readlink /www/panel/nginx/current`. |
| PHP-FPM | `PHP_INI_SCAN_DIR=:/www/panel/php/<ver>/conf.d /www/panel/php/<ver>/current/sbin/php-fpm -t -y /www/panel/php/<ver>/php-fpm.conf -c /www/panel/php/<ver>/php.ini`; log `/www/panel/logs/php-<ver>-fpm.log`; unit `panel-php-<ver>.service`. |
| Dashboard | The panel's own Dashboard page: CPU, load, memory, disk, service grid with controls, alerts (cert renewal failures, detached features, firewall pending/disabled, failed jobs). |
| Logs | The panel's Log viewer (panel, audit, per-site, per-app, per-cron). On disk under `/www/panel/logs/`. |
| Audit | `/www/panel/logs/audit.log`, and `GET /api/audit`. |

## Recovery

The same binary is the console tool. It works while the daemon runs (SQLite WAL) and when stopped.

| Task | Command |
|------|---------|
| Show port, URL, version, DB path | `pbp info` |
| Reset the admin password (revokes all sessions) | `pbp reset-password` |
| Clear failed-login lockouts on the running panel | `pbp unlock-login` |
| Change one setting through the registry validators | `pbp set <key> <value>` |
| Restore the saved firewall ruleset at boot | `pbp firewall restore` |
| Unhook and flush the panel chains from a console (firewall lock-out) | `pbp firewall disable` |
| Print the build version | `pbp version` |

### Roll back a runtime build by hand

nginx:

```sh
ln -sfn versions/<older-id> /www/panel/nginx/current && systemctl restart panel-nginx
```

PHP 8.3:

```sh
ln -sfn versions/<older-id> /www/panel/php/8.3/current && systemctl restart panel-php-8.3
```

Rolling back the whole panel binary (from a native-runtime upgrade): restore the previous binary and
restart the daemon.

### Restore panel state

`panel.db` is SQLite. Back it up with the SQLite backup API (or stop the daemon and copy it, with
`secret.key`) to `/www/panel/data/backups/`.

## Rollback

1. **Binary.** `pbp upgrade` rolls back automatically when the health check fails. To roll back by
   hand, put the previous `pbp` back in `/www/panel/bin/` and `systemctl restart panel`.
2. **Config.** Every managed write goes through `confedit`, which snapshots first and restores on a
   failed validate/reload/health. History is under `/www/panel/history/` and in the database; a
   restore is just another apply. The Config editor shows a diff before a restore.
3. **Firewall.** Every apply has a 60 s confirm-or-rollback timer. Never confirming reverts it. From
   a console with no network, `pbp firewall disable` flushes the panel chains.
4. **Runtime images.** Activate the previous build from the PHP page or the nginx Image card.

## Common issues

| Symptom | Likely cause and fix |
|---------|----------------------|
| Locked out of the panel port | You changed `panel.port`. The listener binds the new port before closing the old after a grace period; a bind that fails is refused and the old port keeps serving. Use `pbp info` to see the live port. |
| Firewall locked you out | SSH/panel port rules are built in and undeletable. From a console, `pbp firewall disable`. A daemon restart before a pending apply is confirmed reverts the ruleset. |
| Panel up but sites do not serve | Check `systemctl status panel-nginx` and `nginx -t`. The boot reconcile re-renders panel-owned files, ensures the container/service, and reloads once. |
| A site feature is greyed out | The include line it owns was removed from the raw site file; the feature is "detached". Confirm "Regenerate site file" to reattach. |
| ACME order fails | The default CA is Let's Encrypt **staging** (untrusted certs). Choose a production directory in Settings → TLS. HTTP-01 needs port 80 reachable; a pre-flight never calls the CA on failure. |
| PHP version has no image / is `absent` | Pull or build it from the PHP page. A version with no site stays stopped until a site uses it again. |
| `php.ini` change has no effect | `php-fpm -t` exits 0 on a broken ini; the panel runs a start-up check too. Check the form and restart the version's FPM unit. |
| Test fails on Windows only | Mode/owner assertions are vacuous on Windows. Use a `*_unix_test.go` test and run it on Linux. |

## Escalation

Open an issue at https://github.com/portbyte-id/server-panel/issues. Include `pbp info`, the output
of the failing validator (`nginx -t`, `php-fpm -t`, or the API `detail`/`output` field), and the
relevant lines from `/www/panel/logs/`.
