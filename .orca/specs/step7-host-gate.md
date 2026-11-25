# Task: open-appsec STEP 7 — host gate on the Ubuntu sandbox

Repo `D:\10_PBCORP\server_panel`, branch **`login-browser-check`** (steps 1-6 merged). Work in this
worktree.

Read first: `docs/design/open-appsec-plan.md` §4 step 7, then **§6 (all)**, **§6.1 (D5)**, and
`docs/design/open-appsec-step4-findings.md`. Then read the existing smoke scripts in `scripts/smoke/`
and follow their shape — this is a new one in that style, not a new harness.

## The host

`root@10.10.11.106` — Ubuntu 26.04.1, panel installed, `panel.service` and `panel-nginx.service`
active, `nano_agent.service` active, `nginx.service` **disabled** (it used to share `:80` via
SO_REUSEPORT — see §6.2; if you ever see two nginx owners on `:80`, stop and report, because results
are meaningless then).

## What to build

A smoke script, e.g. `scripts/smoke/9-open-appsec.sh`, in the style of the existing ones: POSIX shell,
a remote host, a clear PASS/FAIL line per check, a final count, and `--strict` if the others have it.
It must be runnable against a fresh-ish host and be honest when a prerequisite is missing.

The gate clauses from the plan:

1. **A fresh nginx build with the attachment**, then install the agent **from the Store** (through the
   panel's own job, not by hand). Prove the module is in the build prefix and the agent is active.
2. **Enable open-appsec on one site in `detect`.** An SQL-injection and an XSS request **pass**
   (200) and **show up as events**. Normal traffic passes and does not appear as a threat event.
3. **Switch to `block`.** The same requests get **403** with the panel's block page; normal traffic
   still passes.
4. **An exception lets the attack through on that path only.** NOTE: path exceptions are NOT
   renderable (step 0 and step 4 proved `urlPath` does not compile — see plan §6.1 and findings §C).
   **Do not test a path exception as if it worked.** Instead assert the honest current behaviour:
   that the UI/API does not offer a path exception, and that an unsupported stored kind is refused or
   reported. If you can find a working exception key cheaply (findings §C says `hostName` compiles),
   you may test that instead — say exactly which and why.
5. **A second site on ModSecurity and a third with the firewall off** behave exactly as before. This
   is the regression clause: ModSecurity must be untouched.
6. **Stop the agent**: every site still serves (fail-open) and the UI/API reports the agent down.
7. **Reboot**: nginx, the agent and the policy all come back with no panel action.
8. **Uninstall is refused while a site uses the engine**; it works once none does, and the module line
   is gone from the config.
9. **No outbound connections from the agent during the whole run (D5).** D5 is *accepted* — the
   operator chose to ship with the cloud link and mark it. So this clause is **not** "prove there is
   no egress"; it is: **record the egress** (destinations and owning process, via `ss -tnp` sampling)
   so the run documents exactly what the tracking mark refers to. Report the destinations you saw.

Record **RSS, CPU and added latency** next to the step-0 numbers (218.8 MB RSS idle, ~10% of a core,
+2.8 ms p50 / +4.4 ms p95) and say whether they moved.

## Honesty rules for this task

- Every clause needs the command and its real output in the report. A clause you could not run is
  reported as **NOT RUN** with the reason — never as a pass.
- The host is shared and was used for the spike. **Leave it in a working state**: panel and nginx
  active and serving, and say exactly what you changed (sites added, agent installed or removed).
- Restore the agent's policy to its stock file if you change it, and say so.
- If a clause reveals a **product defect**, that is a finding, not a reason to make the test pass.
  Report it and do not paper over it. Fixes belong to a follow-up, not this task — unless it is a
  one-line obvious bug, which you may fix in a separate commit and say so.

## Constraints

- Files you own: `scripts/smoke/` (your new script) and, if a clause exposes a doc-worthy fact,
  short additions to `docs/design/`. **Do not** change Go or `web/` code except for a one-line
  obvious bug as allowed above.
- No push, no force-push, no rebase.

## Gate

The script itself must be shell-lint clean if the repo lints scripts. Run it end to end against the
host and paste the PASS/FAIL summary.

## Report

The script path; a clause-by-clause table of PASS / FAIL / NOT RUN with the evidence line for each;
the egress destinations you recorded; the RSS/CPU/latency comparison with step 0; every product
defect found; the exact final state of the host; commit sha.
