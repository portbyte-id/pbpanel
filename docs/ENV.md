# Environment variables

<!-- AUTO-GENERATED from cmd/panel/recovery.go, cmd/panel/install.go, install.sh, web/vite.config.ts, and scripts/smoke/*.sh. Regenerate with /ecc:update-docs rather than editing by hand. -->

The panel takes **no configuration from the environment**. Every runtime setting lives in the
database and is edited on the Settings page in the UI or with `pbp set` from a console (plan.md
decision 7). These environment variables change how the tooling around the panel behaves — where it
installs, how a smoke run reaches its VM, and how the dev server proxies — not what the daemon does
once running.

## Installer (`install.sh`)

| Variable | Required | Description | Default |
|----------|----------|-------------|---------|
| `GITHUB_TOKEN` | Yes | Read token for the private repository. Used to fetch the release asset. | — |
| `PB_GITHUB_TOKEN` | No | Same token; takes precedence over `GITHUB_TOKEN`. | — |
| `PB_REPO` | No | Repository to fetch from, as `owner/name`. | `portbyte-id/pbpanel` |
| `PB_VERSION` | No | Release tag to install; `latest` or `--version` overrides it. | `latest` |
| `PB_REF` | No | Branch, tag or commit built on the host with `--source`. | `main` |
| `PB_PANEL_DATA_DIR` | No | Data directory; the install root is its parent's parent (`/www` for the default). Also read by every `pbp` command, so the recovery CLI finds the same database. | `/www/panel/data` |

`install.sh` also accepts `--source`, `--ref <ref>`, `--version <tag>` and `--force` as flags.

## Dev server (`web/vite.config.ts`)

| Variable | Required | Description | Default |
|----------|----------|-------------|---------|
| `PB_DEV_PANEL` | No | Target the Vite dev server proxies `/api` to, for example `https://10.10.11.106:8443`. | `https://127.0.0.1:18443` |

## Smoke scripts (`scripts/smoke/*.sh`)

All smoke scripts source `scripts/smoke/lib.sh`. Every one reads `SMOKE_HOST` and honours
`SMOKE_DRY_RUN=1` to print the plan without touching a host.

| Variable | Read by | Description |
|----------|---------|-------------|
| `SMOKE_HOST` | all | `user@host` of the target VM; the user must be root. |
| `SMOKE_DRY_RUN` | all | `1` prints the plan and exits without contacting the host. |
| `SMOKE_SSH_OPTS` | 0, 3, 4, 5, 7, 8 | Extra options passed to `ssh`. |
| `SMOKE_BINARY` | 0 | Path to the `linux/amd64` binary. Default `dist/panel`. |
| `SMOKE_NEW_PORT` | 0 | Port the panel is moved to during the run. Default `9443`. |
| `SMOKE_PASSWORD` | 0, 1, 2, 3 | Admin password. When unset the script runs `pbp reset-password`, which signs everyone out. |
| `SMOKE_USER` | 1, 2, 3 | Admin username. Default `admin`. |
| `SMOKE_REINSTALL` | 5 | `1` re-runs `pbp install` to assert it repairs an existing install. |
| `TLS_TEST_DOMAIN` | 6 | An A record pointing at the VM. Required for smoke 6. |
| `TLS_TEST_WILDCARD_ZONE` | 6 | A Cloudflare zone; the wildcard is `*.smoke6.<zone>`. Required for smoke 6. |
| `TLS_TEST_WILDCARD_SUB` | 6 | Overrides the `smoke6` wildcard label. |
| `TLS_ALLOW_PRODUCTION` | 6 | `1` lets smoke 6 run against production Let's Encrypt. Refused otherwise. |
| `CF_API_TOKEN` | 6 | Cloudflare token (`Zone:DNS:Edit`). Never printed. Required for smoke 6. |

## Integration tests

These are never set by users of the panel; they switch on tests that need real Docker or network.

| Variable | Test | Description |
|----------|------|-------------|
| `PANEL_TEST_IMAGE` | `internal/docker/*_integration_test.go` | Base image the Docker integration tests run against. |
| `PB_PHP_IT_VERSION` | `internal/php/images_integration_test.go` | PHP series the image build test uses. Default `8.3`. |
| `PEBBLE_DIRECTORY` | `internal/tls/issuer_integration_test.go` | Pebble ACME directory URL. |
| `PEBBLE_VA_ALWAYS_VALID` | same | `1` makes Pebble skip real challenge validation. |
| `PEBBLE_CHALLTESTSRV` | same | Pebble challtestsrv base URL for DNS-01 tests. |

## Runtime settings (not environment variables)

What the daemon reads at runtime is the settings registry in `internal/settings/keys.go`, stored in
the database. These are edited on the Settings page or with `pbp set <key> <value>`. There is no
`settings.json` and no environment override — plan.md decision 7 forbids both. The current keys, by
group:

| Group | Keys |
|-------|------|
| Panel | `panel.port`, `panel.bind` |
| Login | `login.max_attempts`, `login.lockout` |
| Session | `session.idle_timeout` |
| PHP | `php.versions` |
| Sites | `sites.web_roots` |
| Files | `fm.editor.max_bytes`, `fm.upload.chunk_max_bytes`, `fm.upload.sweep_age`, `fm.trash.max_bytes`, `fm.trash.retention_days`, `fm.archive.max_bytes`, `fm.archive.max_entries` |
| TLS | `tls.acme.directory`, `tls.acme.email`, `tls.acme.eab_kid`, `tls.acme.eab_hmac_key`, `tls.renew.days_before`, `tls.panel.auto_ip` |
| Firewall | `waf.turnstile.site_key`, `waf.turnstile.secret` |
| Cron | `cron.allow_shell` |
| Logs | `logs.rotate.max_bytes`, `logs.rotate.keep` |
| Runtimes | `runtimes.prebuilt`, `runtimes.url` |

Each key's default, validation and restart flag are defined next to it in `internal/settings/keys.go`.
<!-- END AUTO-GENERATED -->
