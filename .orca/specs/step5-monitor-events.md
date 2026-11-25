# Task: open-appsec STEP 5 — events in the monitor

Repo `D:\10_PBCORP\server_panel`, branch off **`login-browser-check`** (includes step 3's `bdb4f06`).
Work in this worktree.

Read first: `docs/design/open-appsec-plan.md` §4 step 5 **and §6.6 (security events: where and in what
format)** — that section holds real captured event lines from the sandbox. Then read
`internal/wafmon/` end to end: `collector.go`, `parse.go`, `query.go`, `sources.go`, `real_test.go`,
and the `testdata/` directory including `audit-real.json`.

## Measured facts from step 0 that bind this step

From §6.6 and §0.6 of the plan — read them, but the short version:
- The agent writes **JSON-lines security events**; the sample lines in §6.6 are real and are the
  ground truth for your parser. Do not invent a schema.
- Each event carries the host, so events can be **routed to a site** — that is what makes the
  per-site pages work.
- `open-appsec-ctl --view-logs` reads them, and the files live under `/var/log/nano_agent/`
  (per-service `cp-nano-http-transaction-handler.log<N>` files plus an `event_buffer/` dir).
  **Read §6.6 for the exact paths and the retention/rotation behaviour it recorded**; if it does not
  record something you need (rotation, permissions), say so explicitly in your report rather than
  guessing.
- Events are **not** the ModSecurity audit format. They must be **mapped** onto the same internal
  shape the monitor already uses, so the events page, per-site counters and top sources work
  unchanged.

## What to build

**A second parser** (`internal/wafmon/parse.go` or a sibling file)
- Parse the open-appsec JSON event line into the monitor's existing internal event type.
- Map open-appsec fields to what the UI shows today: **action, confidence, attack type, matched
  parameter, source IP, URI, request ID**.
- **Tag each event with its engine** (`modsecurity` | `open-appsec`). This is the field the UI and the
  per-site view will read; keep the existing ModSecurity events tagged too, so nothing downstream has
  to guess.

**A second source** (`internal/wafmon/sources.go`)
- Watch the agent's event log the way the ModSecurity audit log is watched. **Read how
  `collector.go` opens, tails and reopens the ModSecurity file** and follow that mechanism — including
  how it handles rotation. Do not build a second tailing implementation if the existing one can be
  parameterised; if it cannot, say why and keep the new one small.
- **Fail quietly when the agent is not installed**: no log directory must mean "no events", not an
  error that breaks the whole monitor. This host may never have open-appsec.

**Routing**
- Route each event to its site by host, using the same lookup the ModSecurity path uses.
- An event whose host matches no site must not be dropped silently — decide what the existing code
  does for unmatched hosts and be consistent with it.

**Tests**
- **Parser tests from the real event lines captured in §6.6.** Copy two or three of them verbatim into
  `internal/wafmon/testdata/` (e.g. `appsec-real.jsonl`) and assert every mapped field.
- A test that a missing agent log directory is not an error.
- A test that the engine tag is set on both parsers' output.
- If the existing `real_test.go` pattern applies, follow it; state whether it ran.

## Constraints

- Files you own: `internal/wafmon/` only. Do **not** touch `internal/waf`, `internal/appsec`,
  `internal/httpapi`, `internal/sites`, `internal/nginx` or `web/` — other workers own those.
  If the HTTP layer needs a new field, put the data on your existing types and say what the HTTP layer
  must expose; do not edit it.
- **The tree does not currently compile on Linux**: `cmd/panel/password_unix.go:40` calls a `readLine`
  that commit `e001d77` deleted. A fix exists on another branch (`b645184`) but is **not yet merged
  into this worktree**. Do not write that fix yourself; if it blocks your gate, run your package
  directly and say precisely which packages could not build.
- No new dependency. No migration.

## Gate (must be green before you commit)

1. `go build ./internal/...` and `go vet ./internal/...` (or `./...` if the pre-existing break is
   already merged in your worktree)
2. `go test ./internal/wafmon/` — must pass, and must pass on **Windows** as well as Linux if your
   tests touch the filesystem: this repo's tests run on both. Follow how existing tests seam the
   filesystem for that.
3. `golangci-lint run --max-same-issues=0 ./internal/wafmon/...`

## Commit

One conventional commit, e.g. `feat(wafmon): read open-appsec security events`. No push, no
force-push, no rebase. `-c commit.gpgsign=false` only if signing fails, and say so.

## Report

The exact open-appsec→internal field mapping as a table; the real event lines you used as fixtures
(quote them); how rotation and the missing-directory case are handled; what the HTTP layer still needs
to expose; whether the integration test ran; gate output; commit sha; and anything in §6.6 that is
wrong or missing.
