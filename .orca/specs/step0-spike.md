# Task: open-appsec STEP 0 — spike on the Ubuntu sandbox (no product code)

You are a worker on the PB Panel repo (`D:\10_PBCORP\server_panel`, branch `login-browser-check`,
Windows host, Git Bash available). Read `docs/design/open-appsec-plan.md` first: it is the plan you
are executing step 0 of. Step 0 is a **manual spike against a real host**. It produces **no product
code**. Its only deliverable is a findings section written into `docs/design/open-appsec-plan.md`
plus a committed change.

## The host

- `root@10.10.11.106` — Ubuntu 26.04.1, 4 vCPU, 3.3 GB RAM, systemd, Docker 29.9.3.
- SSH with the key already installed (no password). From Windows use
  `ssh -o StrictHostKeyChecking=no root@10.10.11.106 '<cmd>'`; for anything longer than a couple of
  lines write a script locally and pipe it: `ssh root@10.10.11.106 'bash -s' < /tmp/spike.sh`.
- The host already has the panel installed: `panel.service` and `panel-nginx.service` **active**.
- Serving nginx: **nginx 1.30.7**, built with **OpenSSL 3.5.9** and gcc 15.2.0, at
  `/www/panel/nginx/versions/20261119214848-e0757d`, with `/www/panel/nginx/current` symlinked to it.
  Its build prefix has `conf/ lib/ modsec/ modules/ sbin/ version`. `modules/` already holds
  `ngx_http_modsecurity_module.so`.
- `/dev/shm` is a **1.7G tmpfs**. **Disk is tight: ~3.8G free of 29G** — if a step would need more,
  record the blocker and move on rather than filling the disk. `docker system df` and
  `docker image prune -f` on the host's unused images is allowed.
- open-appsec is **NOT** installed: no `/etc/cp`, no `open-appsec-ctl`, no `cp-nano-agent`.

## What to produce

Add a new top-level section to `docs/design/open-appsec-plan.md` — heading
`## 6. Step 0 findings` — and **answer every item below with a recorded answer**, quoting the exact
commands and their real output (trimmed). Where an answer is "unknown", say so and why; never guess.
Also update the file's `Status:` line at the top to `plan + step 0 spike complete (<date>); steps 1-8
not built.` Finally, settle **D4** and **D5** explicitly at the end of the section.

### 0.1 Confirm the plan's open-appsec facts

The plan's §2 facts came from prior knowledge and are **explicitly unverified**. Check each against
the current release and correct the plan text where it is wrong:
- The attachment's real name, source repo, and release/tag naming (`ngx_cp_attachment_module.so`
  plus `libosrc_*.so` — confirm the exact file names and count).
- Whether the attachment must be compiled against the exact nginx release/flags (so it belongs in
  our build recipe), and which tag pairs with which agent release.
- The agent package names (`cp-nano-agent`, `cp-nano-http-transaction-handler`), the systemd unit
  name (`nano_agent.service`?), and `open-appsec-ctl`'s subcommands.

### 0.2 Build the attachment against OUR nginx build

Use the repo's existing builder machinery so the answer is the recipe, not a one-off: read
`internal/nginx/builder.Dockerfile` and `internal/nginx/builder.go` first (note the `@PLACEHOLDER@`
substitution and the `patchelf` RUNPATH treatment ModSecurity's libs get). Then, **by hand in the
builder image**, build the attachment against nginx 1.30.7 with the same configure flags our build
uses (`nginx -V` from `/www/panel/nginx/current/sbin/nginx` gives the exact input; record them).
Report:
- the exact repo, tag, CMake/make flags, and build dependencies;
- the files it produces and where each must land in a build prefix (`modules/` vs `lib/`);
- whether `ldd` is clean once bundled into the build's `lib/` the way ModSecurity's libraries are
  (paste `ldd` output for the module and each new `.so`, and `readelf -d` showing RUNPATH);
- **whether the module loads at all when the agent is absent** — start the current nginx (or a
  throwaway instance on a free port) with `load_module` for the attachment and run `nginx -t`.

Keep any build script you write; put its path and the diff-shaped recipe into the findings so step 1
can transcribe it into `builder.Dockerfile`.

### 0.3 Run the vendor installer in local-policy / no-cloud mode

Download the pinned official installer, then run it. Report:
- the exact download URL, version, and its SHA-256;
- the exact flags for a **local-policy, no-cloud** install (and which flags you rejected and why);
- everything it writes: paths, the unit, the user/group the agent runs as, any dpkg/apt action;
- **whether it touches any nginx file** — it MUST NOT touch ours; verify by checksumming
  `/www/panel/nginx/current/**` and `/www/panel/nginx/includes|sites|panel-*.conf` before and after,
  and paste the diff (empty = good).

### 0.4 Load the module under `panel-nginx.service` and check sandboxing

The real unit runs nginx as user `www` (uid 1500) with a sandbox (`ProtectHome=read-only` etc. —
read the unit). Confirm the worker processes can reach the agent's shared memory and socket in
`/dev/shm`: paste the actual `/dev/shm` entries the agent creates, their owner/mode, and the unit's
sandbox keys that could block them. Then load the attachment in the real serving config (one site's
`server` block or the `http` level — whichever step 0 finds correct) and prove `nginx -t` passes and
a request still serves.

### 0.5 Pin down the policy schema

Confirm and record, with a real example file and a real `open-appsec-ctl --apply-policy` run:
- the `local_policy.yaml` schema version;
- the mode names (`detect-learn` / `prevent-learn` / `inactive`) — exact spelling;
- the confidence field and its values (`critical|high|medium`?), and the field that sets the
  minimum confidence that blocks;
- the exception syntax (path / source IP / parameter), with a working example;
- **whether there is a per-server or per-location nginx directive to turn the attachment off**
  (this settles part of D4). If one exists, name it exactly and show it in a config that
  `nginx -t` accepts.

### 0.6 Security events: where and in what format

Find where the agent writes security events (JSON file? syslog? stdout/journal?) and paste **3 real
event lines** from a request you deliberately made malicious (an SQL-injection and an XSS attempt).
State whether each event names the host, so events can be routed to a site. Note the audit/event log
path, rotation behaviour, and the permission the reader needs.

### 0.7 Outbound traffic (settles D5)

With the agent installed and idle, then under a simple load (`curl` loop against a site), watch
outbound connections with `ss -tp` sampled repeatedly and `tcpdump -nn -i any 'not port 22 and not
port <ssh+panel ports>'` for **at least 45 minutes** (an hour is ideal; if the session cannot hold an
hour, say exactly how long you watched and report samples). Report every outbound destination with
its owning process. If any call cannot be switched off in local mode, that is a **hard stop**: say so
loudly at the top of the findings and recommend stopping before step 2 (D5 says bring it back to the
operator).

### 0.8 Measure footprint

Record agent RSS and idle CPU, then under a simple load test; and the request latency the
attachment adds (compare a site with it to the same site without — at least 200 requests each,
report p50/p95/max). Put the numbers in a small table.

### 0.9 Fail-open and reboot

Kill the agent (`systemctl stop nano_agent`) and confirm every site still serves (fail-open) and
that the panel/nginx report nothing fatal. Then **reboot the host** and confirm nginx, the agent and
the policy all come back with no panel action. Record `systemctl is-active` for each after reboot.

## Rules

- **No product code changes.** Only `docs/design/open-appsec-plan.md` (plus any throwaway script
  you keep under `/tmp` on the host, or a `docs/design/` appendix if it is worth keeping — say so).
- Do not push, do not force-push, do not `git rebase`.
- Do not modify the host's existing panel install or nginx build beyond loading the module for the
  test; leave the host in a state where `panel-nginx.service` is active and serving. Uninstall the
  agent again at the end **only if** you can do so cleanly; if you leave it installed, say so.
- A non-zero exit from `nginx -t`, the installer, or a test is **data**, not automatically a
  failure of the task: capture it. But if the task cannot reach the host at all, say so and stop.
- Evidence over claims: every "confirmed" needs a command and its output in the findings.

## Done means

`docs/design/open-appsec-plan.md` has the `## 6. Step 0 findings` section answering 0.1–0.9 with
real output, D4 and D5 are settled, the `Status:` line is updated, and the change is committed with
a conventional message such as `docs(waf): step 0 spike findings for open-appsec`. Then report:
what was confirmed vs corrected, the D4/D5 verdicts, the footprint table, anything that blocks
step 1, and the commit sha.
