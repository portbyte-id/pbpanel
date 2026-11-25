# Task: open-appsec STEP 6 — UI

Repo `D:\10_PBCORP\server_panel`, branch **`login-browser-check`** (steps 1-5 are merged in). Work in
this worktree.

Read first: `docs/design/open-appsec-plan.md` §4 step 6 **and §6.1 (the D5 decision — it is a product
requirement for you)**, then `docs/design/open-appsec-step4-findings.md`. Then read the SPA code you
will change and the API that now feeds it:
- `web/src/` — find the WAF/site-firewall view, the firewall overview, the Store page and the WAF
  events view. Read `DESIGN.md` and the existing components before writing any UI.
- `internal/appsec/appsec.go` — the `Warning` constant and the `Availability` type.
- `internal/httpapi/handlers_appsec.go`, `handlers_waf.go` — the JSON you will render.
- `docs/API.md` — the documented shapes.

## The D5 requirement, in your step

The operator accepted open-appsec knowing it phones home, on condition that the panel **marks
clearly** that using this WAF variant can subject the host to tracking. Your job is that mark in the
UI:

1. **Store card**: an open-appsec card showing install / uninstall / version / agent state, and the
   warning text **rendered from the API's `warning` field verbatim** — do not retype or paraphrase it
   in TypeScript. Install must not be possible without an explicit acknowledgement of that text (the
   API refuses an install without it, so the UI must collect it: a checkbox or confirm dialog whose
   copy is that same string, not a generic "are you sure").
2. **Engine picker** (on the site firewall view): open-appsec is marked with the tracking warning
   **inline**, so the choice is never made without seeing it. An engine that is unavailable is
   disabled, shows the reason from the API, and links to the Store.
3. **Firewall overview**: an engine column, so an operator can see which sites carry open-appsec.
4. **Events view**: show the engine on each event, and open-appsec's own fields (confidence, attack
   type, matched parameter).

## Engine-specific controls

- **ModSecurity** selected → paranoia and the existing rule exclusions.
- **open-appsec** selected → confidence (`critical` | `high` | `medium`, default `high`) and its
  exceptions.
- **Exceptions: path exceptions are NOT renderable.** Step 0 and step 4 both proved `urlPath` does not
  compile and matches nothing. **Do not offer a path exception in the UI**, and do not imply the
  feature exists. If the model still carries the kind, the UI must not present it as usable — either
  omit it or show it disabled with an honest reason. Offering a control that silently does nothing is
  the exact failure this project keeps trying to avoid. Say in your report what you chose.
- Mode is shared: `detect` / `block` keep their labels, but for open-appsec show what they map to
  (`detect-learn` / `prevent-learn`) so the operator is not surprised by the agent's own vocabulary.

## Constraints

- Files you own: `web/` only (plus `web/dist` rebuild). Do **not** touch any Go file. If the API is
  missing a field you need, say exactly which and stop — do not add it.
- **The committed `web/dist` is what e2e drives.** Rebuild it and commit the result, the way this repo
  already does. Say in your report that you rebuilt it.
- Match the existing design system. Read `DESIGN.md`; do not invent new colours, spacing or
  components. Use the existing UI primitives.
- No new npm dependency.

## Tests

- **vitest** for: the engine picker's availability states (available / unavailable-with-reason /
  disabled), the tracking mark being present and rendered from the API string, the install
  acknowledgement gate, and form validation for confidence and exceptions.
- Follow the existing test conventions in `web/src` (including the portal-stubbing note in
  `tests/setup.ts` — reka portals are invisible in jsdom).
- **Playwright** against the rebuilt `web/dist`, as the existing e2e does. If Playwright cannot run in
  this environment, say so precisely and do not claim it passed.

## Gate (must be green before you commit)

1. `vue-tsc` / type check, as the repo does it.
2. `vitest run` — all green.
3. The SPA build (`vite build`) that regenerates `web/dist`.
4. Any existing SPA lint step.

Note: this repo's SPA gate may need WSL for Node. Check how it is normally run before improvising.

## Commit

One conventional commit, e.g. `feat(web): choose and mark the open-appsec firewall engine`. Include
the rebuilt `web/dist`. No push, no force-push, no rebase.

## Report

Screens changed and what each now shows; **the exact tracking mark text and where it appears**; how
the install acknowledgement is collected; the path-exception decision and why; whether the dist was
rebuilt and committed; whether Playwright ran; gate output; commit sha; and anything the API is
missing.
