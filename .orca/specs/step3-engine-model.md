# Task: open-appsec STEP 3 — the per-site engine in the data model

Repo `D:\10_PBCORP\server_panel`, branch **`login-browser-check`**. Work in this worktree.

Read first: `docs/design/open-appsec-plan.md` §4 step 3, then **§6.1** (the D5 decision) and §0.5 of
§6 (the real policy schema — it constrains your field names). Then read `internal/waf/config.go`
end to end, `internal/waf/config` tests, and `internal/waf/engine.go`'s `Check`.

You are changing the **data model only**. Rendering is step 4 (another worker). Do not render.

## What to build

**`waf.Config` gains an engine**
- `Engine` (`modsecurity` | `open-appsec`), default `modsecurity`, so **every stored row without an
  `engine` key reads exactly as it does today**. This default is load-bearing for upgrades — prove it
  with a test that unmarshals an old-shaped JSON row.
- `OpenAppsec{Confidence, Exceptions}`.

**`Mode` stays shared.** `detect` maps to open-appsec's `detect-learn`, `block` to `prevent-learn`.
Do not add a second mode enum.

**`Confidence`**: `critical` | `high` | `medium` — the minimum confidence that blocks. Default `high`.
Accept only those three, case-insensitively, canonicalised to lower case; anything else is a 422
naming the field.

**Exceptions**: kind `path` (prefix), `source_ip` (CIDR) or `param` (name), each with a note. Apply
the **same limits and character rules** as today's ModSecurity exclusions (`MaxExclusions`,
`MaxNote`, `checkPath`, the CIDR rules in `normalizeAddrs`, `validArg` for the parameter name). Reuse
those helpers rather than duplicating their rules — if a helper is unexported and in the same
package, just call it.

**`Paranoia` and `Exclusions` are kept** but only apply to ModSecurity. `Normalize` must not reject
them when the engine is open-appsec, and must not silently drop them either (they come back so a
switch of engine does not lose settings).

**Behaviour changes**
- `UsesModSecurity()` becomes engine-aware: true only when the engine is modsecurity **and** the mode
  is detect/block.
- Add `UsesOpenAppsec()` on the same pattern.
- `IsDefault()` accounts for both.
- `Check` refuses open-appsec when it is unavailable, with a **field-named 422** exactly the way
  ModSecurity does today (`ErrModuleMissing` → `invalid("mode", ...)` in `internal/sites/waf.go`).
  You own the `waf`-side error value and the message; another worker owns `internal/sites`, so export
  whatever the sites layer will need (e.g. an `ErrOpenAppsecMissing`) and leave a short doc comment
  saying which field it maps to (`engine`).
- `Normalize` validates the new fields and returns canonical JSON (lists never nil, trimmed,
  de-duplicated, sorted where the existing code sorts).

## Constraints

- **Only** `internal/waf/config.go`, `internal/waf/engine.go` and their tests. Do not touch
  `internal/sites`, `internal/httpapi`, `internal/nginx`, `internal/wafmon` or `web/` — other workers
  own those this wave.
- `internal/waf/engine.go` is also being touched by the step-2 worker, but **only** to add an
  `OpenAppsec` block inside `Availability`. Keep your `engine.go` edits minimal and in different
  symbols; re-read the file before editing.
- No new dependency. No migration.
- Follow the existing style exactly: no new comment blocks, no narrating comments; only comment where
  the WHY is non-obvious (e.g. why `modsecurity` is the zero-value default).

## Tests (this is the bulk of the step)

Table tests for:
- normalize: valid/invalid engine values, confidence values, each exception kind, the limits.
- defaults: an empty `Config{}` normalises to engine `modsecurity`.
- **old rows**: a JSON blob with no `engine` key unmarshals to `modsecurity` and round-trips.
- engine-aware `UsesModSecurity` / `UsesOpenAppsec` / `IsDefault` across the mode matrix.
- an open-appsec config with `Paranoia`/`Exclusions` set normalises without error and keeps them.
- `Check` returns the field-named error when open-appsec is unavailable.

## Gate (must be green before you commit)

1. `go build ./...` && `go vet ./...`
2. `go test ./cmd/... ./internal/...`
3. golangci-lint: `golangci-lint run --max-same-issues=0 ./internal/waf/...`
   (CI lint must be green before any commit lands.)

## Commit

One conventional commit, e.g. `feat(waf): add the open-appsec engine to the site firewall model`.
No push, no force-push, no rebase. `-c commit.gpgsign=false` only if signing fails, and say so.

## Report

The final shape of `Config` and `OpenAppsec` as JSON; the exact validation rules and their limits; the
error value/message the sites layer must map; the test list with what each proves; gate output;
commit sha; and anything in §6 that contradicts the plan.
