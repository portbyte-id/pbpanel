# open-appsec: handover

The second firewall engine is built end to end on Ubuntu. Plan and measurements:
`open-appsec-plan.md`; policy-compile traps: `open-appsec-step4-findings.md`;
operator-facing description: the "open-appsec engine" section of `waf.md`.

## Where things are

| Concern | Code |
| --- | --- |
| Attachment build | `internal/nginx` (`builder.Dockerfile`, `builder.go`) |
| Agent install, status, policy path, warning | `internal/appsec` |
| Per-site engine model | `internal/waf`, `internal/sites/waf.go` |
| Policy and module-loader rendering, apply | `internal/waf` (`openappsec_service.go`), confedit |
| Events | `internal/wafmon` |
| UI | `web/` Store card, `SiteWAF`, overview engine column, events |
| API | `docs/API.md`, "Store: open-appsec agent" |
| Host gate | `scripts/smoke/9-open-appsec.sh` |

## Tracking warning (product requirement)

The agent holds a continuous TLS connection to Check Point's cloud even in local
mode, cannot be switched off, and enforcement degrades without it. The operator
accepted this on 2026-11-25 on condition it is marked wherever the engine is
offered. Do not soften the wording; it lives in one constant, `appsec.Warning`.

## Traps

- `--apply-policy` cannot report failure (exit 1 either way). Check the
  orchestration log for `Could not generate appsec local policy`.
- A `specificRules` entry needs a complete practice (all four blocks) and every
  named practice must exist in the document.
- Path exceptions are not renderable (`urlPath` does not compile).
- Install must wait for the handler socket before reloading nginx, or workers
  initialise against nothing and never retry. A lone agent restart needs an nginx
  reload for the same reason; a reboot does not.
- The firewall apply runs on a context detached from the request (five minute
  cap); a client disconnect used to kill `--apply-policy` mid-run.
- Two nginx owners on `:80` invalidate every result. Keep `nginx.service` disabled.

## Not done

- EL (Rocky/RHEL): dnf builder, SELinux labels for `/dev/shm` (M9).
- Path or parameter exceptions; host-wide `hostName` exceptions are possible.
- Agent upgrades are panel-driven only (pinned version per Ubuntu codename).
