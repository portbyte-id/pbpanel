# PB Panel

A single-server control panel for Linux. It manages files, websites (static, reverse proxy and PHP), nginx and PHP configuration, the iptables firewall, and Go and Node.js apps, from one browser UI.

It ships as one static Go binary with the web UI embedded in it.

> [!WARNING]
> The panel runs as root under a hardened systemd unit, because it has to manage system services, firewall rules and files under `/www`. Expose its HTTPS port only to networks you trust, and set a strong admin password during install.

## Features

- **Websites:** static, reverse-proxy and PHP sites, with generated nginx config that you can then edit as raw text.
- **TLS:** certificate upload, ACME HTTP-01 and DNS-01 (including wildcards), with automatic renewal.
- **PHP:** one PHP-FPM pool per site, with `php.ini` and pool settings editable from the UI.
- **Firewall:** default-deny inbound rules on panel-owned iptables chains, with a 60-second confirm-or-rollback timer on every apply.
- **Apps:** run a command as a supervised app (Node.js via nvm, or a prebuilt binary).
- **File manager:** browse, edit, chunked upload, download, archive, permissions, and a recycle bin.
- **Operations:** dashboard, log viewer, audit log, cron jobs, and a jobs view for background work.

Out of scope: databases, a web terminal, multiple users or roles, Git deployment, build pipelines, and multi-server management.

## Requirements

- Ubuntu. Rocky Linux 9 is planned.
- `root` access, and outbound HTTPS to GitHub.
- A GitHub token with read access to the repository (the repository is private).

## Install

Run the installer as root. It installs the panel when it is absent and upgrades it when it is present.

```sh
export GITHUB_TOKEN=<token with read access>
sudo -E sh install.sh
```

Common options:

| Command | Effect |
| --- | --- |
| `sudo -E sh install.sh` | Install or upgrade to the latest release |
| `sudo -E sh install.sh --version v0.2.0` | Install a specific release |
| `sudo -E sh install.sh --source --ref main` | Build a branch, tag or commit on this host |
| `sudo -E sh install.sh --clone --ref main` | Same, but fetch the source with `git` (use when the GitHub API download is slow; needs git) |
| `sudo -E sh install.sh --force` | Reinstall even when already current |

The installer downloads the `pbp-linux-<arch>` release asset and checks it against `SHA256SUMS`. If the repository has no release yet, or you pass `--source`, it builds the source on the host. It uses a Go and a Node.js toolchain that it downloads into a temporary directory and removes afterwards.

> [!NOTE]
> Set `PB_PANEL_DATA_DIR` to change the install root from the default `/www/panel/data`.

## Command line

The same binary is the operator's recovery tool.

```sh
pbp version               # print the build version
pbp install               # first-time setup (called by install.sh)
pbp upgrade               # replace the binary with a new release
pbp serve                 # run the daemon (managed by systemd)
pbp reset-password        # set a new admin password from the console
```

Run `pbp <command> -h` for each command's options.

## Layout on the host

| Path | Contents |
| --- | --- |
| `/www/panel/bin/` | `pbp` and `supervisord` |
| `/www/panel/data/` | `panel.db` (SQLite) and `secret.key` |
| `/www/panel/nginx/` | nginx config, generated once and then owned by the operator |
| `/www/panel/php/<version>/` | PHP config and pool files |
| `/www/panel/logs/` | panel, audit, site, app and cron logs |
| `/www/services/sites/<domain>/` | Web root for each site |
| `/www/services/apps/<name>/` | Default directory for each app |

Panel-owned include files are rewritten by panel actions. Files the operator has edited are not overwritten.

## Development

Requirements: Go (version in `go.mod`), Node.js 22, and npm.

```sh
make build     # static binary at bin/pbp
make web       # npm ci, build the SPA, run its unit tests
make test      # go test ./...
make lint      # convention checks, plus golangci-lint when installed
make all       # lint, test, web, build
```

The binary embeds `web/dist`, so build the web UI before a release build.

### Project layout

| Path | Contents |
| --- | --- |
| `cmd/panel/` | CLI entry point and commands |
| `internal/` | Feature packages: `sites`, `nginx`, `php`, `firewall`, `tls`, `apps`, `fm`, `jobs`, `httpapi`, and others |
| `web/` | React, TypeScript, Vite and Tailwind UI |
| `scripts/smoke/` | Smoke tests, one per milestone, run on a real VM |
| `docs/` | Plan, development plan, design notes, and the [API](docs/API.md), [environment](docs/ENV.md), [runbook](docs/RUNBOOK.md) and [contributing](docs/CONTRIBUTING.md) references |

### Tests

- CI (`.github/workflows/ci.yml`) runs on pushes to `main` and on pull requests: `go build`, `go vet`, `go test ./...`, the web build, `tsc --noEmit`, and `npm test`.
- `scripts/smoke/*.sh` exercise the installed panel end to end on an Ubuntu VM. Each milestone has one smoke script.

## Status

The project is under active development. Current milestone status and open items are in [docs/development-plan.md](docs/development-plan.md). The architecture and decisions are in [docs/plan.md](docs/plan.md).

## Releases

Pushing a `v*` tag runs the release workflow. It builds the binaries and checksums that `install.sh` downloads.

```sh
git tag v0.2.0
git push origin v0.2.0
```
