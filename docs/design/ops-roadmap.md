# Operations roadmap: six plans and how they fit

Status: **plans written 2026-11-25, none started; X1 ratified 2026-11-25 (age); the other decisions unratified.**

Six features, each with its own plan in this folder. This note fixes what they share, the order to
build them, and the decisions that cross plan boundaries. Each plan's own §3 lists its local decisions.

| # | Feature | Plan | New package | Migration | Smoke script |
|---|---|---|---|---|---|
| 3 | Notifications and alerts | `notifications-plan.md` | `internal/notify` | 0022 | `10-notifications.sh` |
| 1 | Site and panel backups | `backups-plan.md` | `internal/backup` | 0023 | `11-backups.sh` |
| 4 | Metrics history | `metrics-history-plan.md` | (extends `sysinfo`, `wafmon`) + `metrics.db` | 0024 | `12-metrics-history.sh` |
| 15 | Automatic IP bans | `auto-ban-plan.md` | `internal/autoban` | 0025 | `13-autoban.sh` |
| 16 | Security checks | `security-checks-plan.md` | `internal/security` | 0026 | `14-security.sh` |
| 18 | Config as code | `config-as-code-plan.md` | see plan | none (0027 free) | `15-config-as-code.sh` |

Migration numbers follow build order. If the order changes, renumber before the first one lands.

## 1. The shared contract

`notifications-plan.md` §4.1 owns it: `notify.Event`, `notify.Emitter`, the optional `notify.Syncer`
(producers that poll can resolve conditions that have cleared), `notify.Nop{}` / `notify.Or(e)`, and the rules for naming kinds
and keys. **Notifications step 1 lands only the contract (stdlib only).** Every other plan depends on
the interface alone and runs against `Nop` until the real notifier is wired. So step 1 is the one
hard prerequisite. The rest of notifications can be built at the same time as the others.

Event kinds by producer:

| Producer | Kinds |
|---|---|
| notifications (sweep, jobs, cron, TLS) | `tls.renew_failed`, `tls.expiring`, `job.failed`, `cron.failed`, `firewall.disabled`, `site.detached`, `notify.delivery_failed` |
| backups | `backup.failed`, `backup.succeeded`, `backup.verify_failed` |
| metrics history | `metrics.threshold` (Key `metrics.threshold:<rule>:<target>`, resolves) |
| auto-ban | `ban.added` (15-min digest per jail), `ban.cap_hit` (resolves) |
| security checks | `security.finding` (Key `check_id:target`, resolves), `integrity.changed` |
| config as code | `config.applied`, `config.apply_failed` |

Certificate expiry is the notifications sweep's alone (`tls.expiring`). Metrics alerts don't cover it.

## 2. Build order

1. **Notifications step 1**: the contract. Small; unblocks everything.
2. **Backups**: the biggest risk to operators today. It doesn't depend on anything else here.
3. **Notifications, rest of the plan**: the channels, the bell, the sweep. After this, backup failures reach someone.
4. **Metrics history**: the threshold alerts depend on step 3 to be useful.
5. **Auto-ban**: needs its ipset spike (step 0) on the sandbox first.
6. **Security checks**: needs its apt and sshd spike (step 0). The 2FA check stays reserved.
7. **Config as code**: last, because it reads every other feature's definitions. Building it after the others means it exports their tables too: notification channels, backup plans, jails, alert rules.

Steps 2–6 touch different packages and can run in parallel once step 1 has landed. The shared hot
spots are `cmd/panel` wiring, `internal/settings/keys.go`, `internal/cron/builtin.go` and the nav.
Merge those one at a time.

## 3. Decisions that cross plans (ratify these first)

| # | Decision | Recommendation |
|---|---|---|
| X1 | **One encryption format.** Backups D4 proposes its own XChaCha20-Poly1305 STREAM format built on `x/crypto`. Config-as-code D2 proposes `filippo.io/age` with a scrypt passphrase. | **Use `age` for both.** It is the same design (STREAM chunks, a wrapped file key, scrypt passphrase recipients), it has been reviewed, and it means one dependency instead of a hand-written crypto format. The backups keyring (D6) maps onto age's support for several recipients. |
| X2 | **Plain-text secret settings.** Backups, notifications and config-as-code all found that `Secret` settings (Turnstile secret, EAB HMAC key) are stored unencrypted. | Fix once, as a separate small change before backups: seal `Secret` settings with secretbox. Each plan currently works around it on its own. |
| X3 | **New dependencies.** zstd (backups D2), YAML (config D1), age (X1). | Accept all three. Each one replaces hand-written code that would be larger and riskier. |
| X4 | **Daemon-down CLI commands.** `pbp restore panel` (backups D11) and `pbp config apply` (config D9) both refuse while the daemon runs. | Keep that for v1. A root-only Unix socket to the daemon would serve both later; design it once, not twice. |
| X5 | **Config-as-code scope.** Should it export the tables the other five plans add? | Yes: notification channels, backup plans and targets, jails, alert rules and accepted security risks are all declarative. Credentials follow config D3 (left out unless encrypted). |

## 4. Existing defects found while planning (not fixed by any plan)

- `PANEL-FORWARD` checks the blocklist before the allowlist, so a block beats the allowlist for published container ports (auto-ban §7).
- `restoreText` does not declare `PANEL-FORWARD`, which may break a rollback that holds forward lines (auto-ban §7).
- nginx has no real-IP configuration, so behind a CDN the WAF and the logs see the CDN's address. This affects auto-ban web jails and the per-site metrics.
- Hand edits made through the raw file editors live only on disk. Backups copy them; config as code can only warn about them.
