# Task: open-appsec STEP 2 — agent install job, availability, HTTP

Repo `D:\10_PBCORP\server_panel`, branch **`login-browser-check`**. Work in this worktree.

Read first: `docs/design/open-appsec-plan.md` §4 step 2, **§6 (step 0 findings, all sections)** and
**§6.1 — the D5 decision**. Then read the code you will mirror: `internal/jobs/`, an existing Store
job (the one-click build job), `internal/waf/engine.go` (`Availability`, `AvailabilityFunc`),
`internal/install/` (how a pinned download with a SHA-256 check and a private temp dir is done),
`internal/httpapi/handlers_waf.go`, and `docs/API.md`.

## The D5 decision is a product requirement in this step

The operator accepted open-appsec knowing it phones home. Your job includes making that **visible
before install**: the Store card and the install confirmation must state that the open-appsec agent
holds a **continuous TLS connection to Check Point's cloud**, that using this engine **can subject the
host to tracking**, and that enforcement degrades while that connection is unavailable. Installation
must require an **explicit acknowledgement of that text** — not a silent POST. Put the warning string
in one Go constant in `internal/appsec` with a doc comment saying why it exists, and expose it in the
Store/availability JSON so the UI renders exactly that text rather than inventing its own copy.

Requirements on the wording: name Check Point; say the connection is continuous and required; say it
can subject the host to tracking; do not euphemise as "telemetry optional". Facts to draw on: 0.7 and
6.1.

## Measured facts from step 0 that bind this step

- The agent **download URL is unversioned** — any path answers 200 for whatever is current. So the
  version is pinned only by a **stored SHA-256** of the archive. Do not invent a versioned URL.
- The installed agent writes `/etc/cp/`, unit **`nano_agent.service`** (exact), managed with
  `/etc/cp/scripts/open-appsec-ctl.sh` (flags `-ap` apply-policy, `-u` uninstall, `-s` status, `-r`,
  `-q`, `-lp`, `-vl`).
- The install needs the **attachment-registration-manager** too
  (`cp-nano-attachment-registration-manager`); there is **no `cp-nano-http-transaction-handler`
  package** (that is only a binary name).
- **`--offline_mode` does not stop the cloud connection** — do not offer it as a privacy switch.
- The vendor installer runs non-interactively with answers piped in; the uninstall is
  `echo Y | /etc/cp/scripts/open-appsec-ctl.sh --uninstall`.
- **`panel-nginx.service` must be reloaded after the agent is up**, or nginx keeps stale `/dev/shm`
  sockets and enforcement silently stops (0.4). Your install job must do agent-then-reload in that
  order.
- Policy file: `/etc/cp/conf/local_policy.yaml`, schema `v1beta2`. An all-`inactive` default policy is
  what an install with no sites should leave behind.
- The agent needs `/dev/shm` (`check-point/`, `attachment-metadata`, and per-worker ring buffers).

## What to build

**New package `internal/appsec`**
- The pinned archive version + SHA-256, and the download URL.
- The install and uninstall command builders, the policy path, and thin wrappers over
  `open-appsec-ctl` (apply-policy, status, uninstall).
- `Status()` reporting installed / version / service active — read the unit, do not assume.
- The warning constant (see above).
- Unit tests. Anything touching the filesystem must be injectable the way the rest of this codebase
  does it (look at how `internal/install` and `internal/nginx` seam their filesystem/exec so tests
  run on Windows). **No test may require root, systemd or a real agent.**

**A Store job** (`appsec.install` / `appsec.uninstall`) through `internal/jobs`
- Progress lines like the existing jobs.
- Downloads to a **private temp dir**, checks the digest, runs the installer in local mode, writes an
  all-`inactive` policy, applies it, then reloads nginx.
- Install must be refused without the acknowledgement flag.
- **Uninstall refuses while any site uses the engine.** Read the sites WAF store for that — but note
  step 3 owns `internal/waf`'s config types this wave, so depend only on what exists today
  (`waf.Config.Mode`) OR take a small injected function (e.g. `inUse func(ctx) (bool, error)`) that
  later wires to the engine field. Prefer the injected function: it keeps you out of step 3's files.

**Availability — do NOT add a field to `waf.Availability`**
- Step 3 has landed (`bdb4f06`) and already provides the seam: `waf.OpenAppsecAvailability`
  (`func(ctx) (available bool, reason string)`), `(*Engine).WithOpenAppsec(src)` and
  `(*Engine).OpenAppsecAvailable(ctx)`. `Check` already calls it for a config that uses the engine.
  **Read `internal/waf/engine.go` before you start.**
- What you build is the **implementation** of that seam in `internal/appsec`, wired in where the WAF
  engine is constructed (find the `waf.New(...)` call site). Available means: the serving nginx build
  has the module **and** the agent is installed and active.
- If the Store/overview JSON needs richer data (agent version, acknowledgement state, the warning
  text), put it in **your own** `internal/appsec` status type and expose it from the HTTP layer. Do
  not widen `waf.Availability` — that is how the availability seam stays a single boolean+reason.

**HTTP**
- Install/uninstall routes, and the availability (including the warning text and the
  acknowledgement requirement) in the existing WAF status and overview JSON.
- `docs/API.md` updated for the new routes and fields.

## Constraints

- Do **not** touch `internal/nginx` (step 1 owns it), `internal/wafmon` (step 5), or `web/` (step 6).
- `internal/waf` is now step 3's committed work: you may **read** it and implement `WithOpenAppsec`,
  but do not restructure `Config`, the engine constants, or `Check`.
- **The tree does not currently compile on Linux**: `cmd/panel/password_unix.go:40` calls a `readLine`
  that commit `e001d77` deleted. Another worker is fixing it in a separate commit. Do not fix it
  yourself; if it blocks your gate, run your package tests directly and say exactly which packages
  could not be built.
- No new third-party Go dependency.
- Migrations are not needed here.

## Gate (must be green before you commit)

1. `go build ./...` and `go vet ./...`
2. `go test ./cmd/... ./internal/...`
3. golangci-lint on touched packages with `--max-same-issues=0`.
   Note the standing rule: CI lint must be green before the commit lands; `errcheck` on
   `resp.Body.Close()` is a real past failure — check every deferred close.

## Commit

One conventional commit, e.g. `feat(appsec): install the open-appsec agent from the Store`. No push,
no force-push, no rebase. Use `-c commit.gpgsign=false` only if signing fails, and say so.

## Report

Files changed; the exact warning text you settled on; how `Status()` determines each field; how
uninstall decides a site is using it; the routes and JSON fields added; gate output; commit sha;
and anything in §6 you found to be wrong.
