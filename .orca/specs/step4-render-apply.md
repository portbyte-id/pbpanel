# Task: open-appsec STEP 4 — rendering and applying

Repo `D:\10_PBCORP\server_panel`, branch off **`login-browser-check`** (includes step 3's `bdb4f06`).
Work in this worktree.

Read first: `docs/design/open-appsec-plan.md` §4 step 4, then **§6 (all)** — especially §0.4, §0.5,
§6.1 and §6.2, which are measured facts. Then read `internal/waf/render.go` end to end,
`internal/waf/config.go` (step 3's engine model), `internal/sites/waf.go` (`SetWAF`,
`SetWAFGlobal`, `WAFSharedFiles`), and `internal/confedit/`.

## Measured facts from step 0 that bind this step

- **`load_module` is main context**, in a file under `waf/main/` beside the existing ModSecurity
  loader: `waf/main/open-appsec.conf`. `panel-main.conf` already globs `waf/main/*.conf`.
- **`cp-nano-nginx-attachment on|off;` is `server` or `location` context, never `http`.** Its default
  is *inspected*, so **every site that does not use open-appsec must emit `off` explicitly** while
  the module is loaded. The alternative — load the module only while at least one site uses the
  engine — is the simpler one; prefer it, and say which you chose.
- **`cp-nano-nginx-attachment` without the module makes `nginx -t` fail.** So the module line and the
  directives must be added and removed **in one change** — never one without the other.
- **Policy**: `/etc/cp/conf/local_policy.yaml`, schema **`v1beta2`**. Default practice `mode:
  inactive`; only hosts in `specificRules` are acted on. Mode names are exactly `detect-learn`,
  `prevent-learn`, `inactive` — map `detect`→`detect-learn`, `block`→`prevent-learn`.
  Apply with `/etc/cp/scripts/open-appsec-ctl.sh --apply-policy <file>`.
- **Path exceptions do not work.** §6.1 proves `urlPath` (and `host`/`method` in the exceptions
  block) compile to an **empty match and silently do nothing**. The step-3 model carries them, but
  **you must NOT render path exceptions** — leave them out of the policy and leave a short comment
  saying why, referencing that they are unproven. Rendering a control that silently does nothing is
  worse than not having it. If you can find a working key cheaply, do; otherwise do not.
- **The agent must be up before nginx reloads** (§0.4): a lone agent restart silently stops
  enforcement until `systemctl reload panel-nginx`. Apply order is: write files → `nginx -t` →
  reload nginx → `open-appsec-ctl --apply-policy`.
- **The challenge locations and the ACME path are exempt**, exactly as they are for ModSecurity today
  (a flagged puzzle request would lock a visitor out).

## What to build

**Site block** (`internal/waf/render.go`)
- For an open-appsec site: **no `modsecurity` lines**; instead the per-site directive from the facts
  above. The rest of the block (rate limit, paths, IP rules, challenge) is engine-independent and
  stays exactly as it is.
- Golden tests for the block, both engines.

**Shared files**
- `waf/main/open-appsec.conf` carrying the `load_module` line, present **only while at least one
  site uses the engine** (or always-present plus explicit `off` everywhere — pick one, justify it in
  a comment, and be consistent).

**The policy**
- One `local_policy.yaml` rendered deterministically from every open-appsec site: default practice
  `inactive`, one `specificRules` entry per site with its server names/aliases, its mode, confidence,
  the exceptions that are proven to work, and a log trigger.
- **Rendered deterministically so an unchanged set writes nothing** — that is a hard requirement;
  a churning policy would reload the agent on every unrelated change. Test it: render twice, assert
  byte-identical.

**Apply order** in `SetWAF`, `SetWAFGlobal`, boot reconcile and site delete
- confedit writes the nginx files **and** the policy in one change, then `nginx -t`, then reload,
  then `--apply-policy`.
- **If the agent rejects the policy, the whole change rolls back** exactly as an `nginx -t` failure
  does today. This most likely means teaching confedit a second service kind with its own validate
  and apply commands — read `internal/confedit` and extend it in its own idiom rather than
  special-casing open-appsec inside `internal/sites`.
- Renaming a site, adding an alias, or deleting a site re-renders the policy.

**Tests**
- Golden files for the site block and the policy.
- A real-nginx integration test that the rendered config passes `nginx -t` **with the module loaded
  and no agent present** (the module fails open, so this works).
- Deterministic render test.
- A rollback test: a rejected policy restores the previous files and the stored config.

## Constraints

- Files you own: `internal/waf/render.go` (+ tests, goldens), `internal/sites/waf.go`, and
  `internal/confedit/`. Do **not** touch `internal/appsec`, `internal/httpapi`, `internal/nginx`,
  `internal/wafmon` or `web/` — other workers own those.
- `internal/waf/config.go` is step 3's committed work: read it, do not restructure it.
- **The tree does not currently compile on Linux**: `cmd/panel/password_unix.go:40` calls a `readLine`
  deleted by `e001d77`. Another worker is fixing it separately. Do not fix it; if it blocks your
  gate, run your packages directly and say which could not build.
- No new dependency. No migration.

## Gate (must be green before you commit)

1. `go build ./...` (or, if blocked by the pre-existing break, `go build ./internal/...` plus a note)
2. `go vet ./...`
3. `go test ./cmd/... ./internal/...`
4. `go test -tags integration ./internal/waf/ ./internal/sites/` on WSL:
   `MSYS_NO_PATHCONV=1 wsl -d Ubuntu-26.04 -e bash -lc 'export PATH=$PATH:/usr/local/go/bin GOFLAGS=-buildvcs=false; cd /mnt/d/10_PBCORP/server_panel && go test -tags integration ./internal/waf/ ./internal/sites/'`
   It needs Docker for some tests; report exactly what ran.
5. `golangci-lint run --max-same-issues=0 ./internal/waf/... ./internal/sites/... ./internal/confedit/...`

## Commit

One conventional commit, e.g. `feat(waf): render and apply the open-appsec engine per site`.
No push, no force-push, no rebase. `-c commit.gpgsign=false` only if signing fails, and say so.

## Report

The module-line choice you made and why; the exact rendered site block and policy (paste a golden);
how determinism is guaranteed; how the rollback works and what it covers; **the path-exception
decision** with the evidence you based it on; whether the integration tests actually ran; gate output;
commit sha; anything in §6 that is wrong.
