# Contributing

<!-- AUTO-GENERATED setup/commands from Makefile, go.mod, web/package.json and .github/workflows/ci.yml. Regenerate with /ecc:update-docs rather than editing by hand. -->

## Development setup

- **Go 1.27.3** or newer (the version in `go.mod`).
- **Node.js 22** and **npm** for the web UI. CI pins `node-version: "22"`; keep them in step.
- **golangci-lint v2.15.0** to run the full lint locally. `make lint` skips it when it is not on
  `PATH` and runs only the convention greps.
- A Linux host or WSL for the packages that touch file modes, ownership, `systemd` or Docker.
  Windows is fine for most unit tests, but mode and owner assertions are vacuous there — they are
  in `*_unix_test.go` for that reason.

No `.env` file is needed. See [ENV.md](ENV.md) for the few environment variables the tooling reads.

## Commands

The `Makefile` is the source of truth. Run these from the repository root.

| Command | Description |
|---------|-------------|
| `make build` | Static `CGO_ENABLED=0` binary at `bin/pbp` (version-stamped from `git describe`). |
| `make web` | `npm ci`, build the SPA, run its unit tests. Build this before a release build; the binary embeds `web/dist`. |
| `make test` | `go test ./...`. |
| `make vet` | `go vet ./...`. |
| `make lint` | Convention greps (`scripts/lint-conventions.sh`) plus `golangci-lint run` when installed. |
| `make fmt` | `go fmt ./...`. |
| `make clean` | Remove `bin/` and `web/dist`. |
| `make all` | `lint test web build`, in that order. |

### Web scripts (`web/package.json`)

| Command | Description |
|---------|-------------|
| `npm run dev` | Vite dev server, proxying `/api` to `PB_DEV_PANEL`. |
| `npm run build` | `tsc --noEmit` then `vite build`. |
| `npm run typecheck` | `tsc --noEmit` only. |
| `npm test` | `vitest run`. |
| `npm run test:watch` | `vitest` in watch mode. |
| `npm run e2e` | `playwright test`. |
| `npm run preview` | `vite preview`. |

## Testing

- **Unit tests.** `go test ./...` and `npm test`. CI runs both on every push to `main` and every
  pull request, plus `go build`, `go vet`, `tsc --noEmit`, and the static-binary check.
- **Integration tests** (Docker, Pebble ACME, PHP image builds) are behind `-tags integration` and
  are not run by CI. They need Docker and, for some, a free port 80. Run them by hand on Linux.
- **Smoke scripts** (`scripts/smoke/*.sh`) are the exit gate for each milestone. Each runs against
  a fresh Ubuntu VM over SSH and checks the panel end to end with an independent oracle. They are
  not run in CI. `SMOKE_DRY_RUN=1` prints the plan. See [ENV.md](ENV.md) for their variables.
- **When you fix a bug, add a regression test.** When you touch a unix-only path, put the test in a
  `*_unix_test.go` file so it does not silently pass on Windows.

## Code style

Conventions that a lint check enforces (`scripts/lint-conventions.sh`, `golangci-lint`):

- **No shell strings.** `exec.Command` takes argument vectors. `sh -c` and `bash -c` are refused
  outside an explicit allowlist (`scripts/convention-allowlist.txt`).
- **Distro logic only in `internal/osdep`.** CI greps for `apt`, `dnf`, `ufw`, `firewalld`,
  `semanage` outside that package.
- **Database first, then render.** Every feature that writes to disk has an idempotent `Render(ctx)`
  called at boot.
- **All managed-config writes go through `confedit`**, which validates, reloads and rolls back.
- **Errors reach the UI.** Validator output is returned verbatim in the API error body; no silent
  fallbacks.
- **Comments explain why, not what.** Do not narrate the code.
- **gofmt is enforced by CI.** A CRLF checkout on Windows can make `gofmt -l` report files that are
  actually clean; check with LF before assuming a real diff.

## Commits and pull requests

- Commit in small, single-concern units with a conventional prefix: `feat(sites): …`,
  `fix(ci): …`, `docs(php): …`. Match the existing history (`git log`).
- Run `make all` (or at least `go test ./...`, `go vet ./...`, `make lint`, and the web build)
  before opening a PR.
- PR checklist:
  - [ ] `go build ./...`, `go vet ./...`, `go test ./...` green.
  - [ ] `gofmt -l .` clean (checked with LF line endings).
  - [ ] `scripts/lint-conventions.sh` green and `golangci-lint run` clean.
  - [ ] Web: `npm run build`, `npx tsc --noEmit`, `npm test` green.
  - [ ] If a setting was added, it is in the settings registry with default, validation and restart flag.
  - [ ] If a state-changing route was added, it writes an audit entry and is in the 401/CSRF test matrix.
  - [ ] If behaviour visible to the operator changed, the relevant doc under `docs/` is updated.

## Where things live

| Path | Contents |
|------|----------|
| `cmd/panel/` | CLI entry point, subcommands, boot wiring. |
| `internal/` | One package per feature (`sites`, `nginx`, `php`, `fm`, `firewall`, `tls`, `apps`, `jobs`, `httpapi`, …). |
| `web/` | React + TypeScript + Vite UI. |
| `scripts/smoke/` | Per-milestone smoke tests. |
| `scripts/e2e/` | Playwright stand-in server. |
| `docs/` | Plan, development plan, design notes, and the generated references. |

Architecture and decisions: [plan.md](plan.md). Milestone status: [development-plan.md](development-plan.md).
