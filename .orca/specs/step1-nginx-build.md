# Task: open-appsec STEP 1 — the attachment in the nginx build

Repo `D:\10_PBCORP\server_panel`, branch **`login-browser-check`**. Work in this worktree.

Read first: `docs/design/open-appsec-plan.md` §4 step 1 **and §6 (step 0 findings)** — §6 is measured
fact from a real host, it overrides the plan's original guesses. Also read
`internal/nginx/builder.Dockerfile`, `internal/nginx/builder.go`, `internal/nginx/paths.go` and
`internal/nginx/builder_test.go`.

## Measured facts from step 0 that bind this step

- Source repo is **`https://github.com/openappsec/attachment`**. It has **zero tags and zero
  releases**, so pin a **commit SHA** (`4c88f6389e301fd1958b0448f0e0ce01888a20c5` was the tip
  measured in step 0 — verify it still resolves; if you move it, say why).
- It builds with `make install` against a configured nginx tree; needs `-DCMAKE_POLICY_VERSION_MINIMUM=3.5`
  for CMake 4, plus `python3` and `dos2unix`.
- It produces **seven** files: `libngx_module.so` and six libraries
  (`libosrc_compression_utils.so`, `libosrc_nginx_attachment_util.so`, `libosrc_shmem_ipc.so`,
  `libshmem_ipc_2.so`, `libnano_attachment.so`, `libnano_attachment_util.so`).
- The panel's own convention renames the module: it ships and loads as
  `ngx_cp_attachment_module.so`. Keep that name.
- The module's `NEEDED` entries are `libshmem_ipc_2.so`, `libosrc_nginx_attachment_util.so`,
  `libosrc_compression_utils.so`, `libm`, `libc`, with `RUNPATH $ORIGIN/../lib` — so module in
  `$P/modules/`, libraries in `$P/lib/` works unchanged.
- `ldd` is clean after the existing `patchelf` RUNPATH treatment ModSecurity's libraries already get.
- The module **loads and fails open with the agent absent** — so `nginx -t` with it loaded must pass
  on a host with no agent.
- `nginx -V` of the serving build in step 0 was nginx **1.30.7**, gcc 15.2.0, OpenSSL **3.5.9**.

## What to change

**`internal/nginx/builder.Dockerfile`**
- Fetch the attachment source at the pinned **commit SHA** and check it against a pinned **SHA-256**
  the same way the Core Rule Set tarball is checked. A commit archive is
  `https://github.com/openappsec/attachment/archive/<sha>.tar.gz` — pin that archive's digest.
- Build it against the nginx tree the recipe already builds, install the module into `$P/modules/` and
  the six libraries into `$P/lib/`, and give the libraries the **same `patchelf` RUNPATH treatment**
  ModSecurity's libraries get (read how the existing recipe does it — do not invent a new mechanism).
- Add it to the build's self-check next to the ModSecurity one: `test -f $P/modules/ngx_cp_attachment_module.so`
  and an `ldd`-clean assertion.
- Add a `LABEL` alongside the existing ones recording the attachment commit.

**`internal/nginx/builder.go`**
- Add `openAppsecAttachmentVersion` (the commit SHA) and its digest constant, and the placeholder
  substitution entries for the Dockerfile. Follow exactly how the ModSecurity/CRS placeholders are
  declared and substituted — there is one obvious pattern; use it.
- Extend **`BuildSource`** so the Nginx page says open-appsec is part of the build.
- If `paths.go` needs a helper for the module's path or a directory, follow the existing ModSecurity
  path helpers rather than hard-coding a string. Do not add a new directory layout.

**`internal/nginx/builder_test.go`**
- The existing real-Docker build test asserts the ModSecurity module exists and `nginx -t` passes.
  Extend it: assert `ngx_cp_attachment_module.so` exists, that `ldd` on it is clean, and that
  `nginx -t` passes with a config that **loads** it while **no agent is present**. Follow the test's
  existing integration-tag structure; it needs Docker.

## Constraints

- **Do not** change `internal/waf`, `internal/sites`, `internal/httpapi`, `internal/wafmon` or
  `web/` — other workers own those this wave. If you believe a change there is required, stop and
  report instead of making it.
- Do not touch the CI package workflow: it uses `BuildSpec`, so it picks this up with no change.
  Verify that claim by reading the workflow, and say in your report whether it holds.
- No new third-party Go dependency.

## Gate (must be green before you commit)

1. `go build ./...` and `go vet ./...`
2. `go test ./cmd/... ./internal/...`
3. `go test -tags integration ./internal/nginx/` — this needs Docker. On Windows there is no Docker;
   use WSL: `MSYS_NO_PATHCONV=1 wsl -d Ubuntu-26.04 -e bash -lc 'export PATH=$PATH:/usr/local/go/bin GOFLAGS=-buildvcs=false; cd /mnt/d/10_PBCORP/server_panel && go test -tags integration ./internal/nginx/'`
   It is a long build; if it cannot complete in this worktree, say so **precisely**, name what ran,
   and do not claim it passed.
4. golangci-lint on the packages you touched, the repo's own config, with
   `--max-same-issues=0`, e.g.
   `golangci-lint run --max-same-issues=0 ./internal/nginx/...`

## Commit

One conventional commit, e.g. `feat(nginx): build the open-appsec attachment into the nginx build`.
Do not push. Do not force-push. Do not rebase. Signing may fail — commit with `-c commit.gpgsign=false`
if so and say it in the report.

## Report

Exact files changed; the pinned SHA and digest; whether the integration test actually ran to
completion (and on which host/distro); whether the CI workflow claim holds; the gate output summary;
the commit sha; and anything you found that contradicts §6 of the plan.
