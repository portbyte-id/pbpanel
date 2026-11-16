# M0 security review (step 0.15)

Scope: `internal/auth`, `internal/httpapi` (session, CSRF, headers, TLS) as of `587b346`.
Method: read the code, not the reports. Fixes landed in `3275142`.

**No Critical or High findings. The milestone is not blocked by this review.**

## Findings

| # | Sev | Where | Finding | Disposition |
|---|-----|-------|---------|-------------|
| 1 | Medium | `internal/auth/service.go` `VerifyLogin` | Throttle `Check` and `Fail` were separate steps. N parallel guesses all passed `Check` before any was counted, so the lockout bounded sequential attempts only, and each parallel guess also held a 64 MiB argon2 derivation (unauthenticated memory pressure). | **Fixed.** `loginMu` serialises check, verify and record. Test: `TestConcurrentLoginsCannotOutrunTheThrottle` (40 parallel guesses, exactly `max` reach verification). |
| 2 | Medium | `internal/httpapi/headers.go` | API responses (which carry the CSRF token via login and `/api/me`) had no `Cache-Control`. | **Fixed.** `no-store` on every `/api/` response, errors included. Test: `TestAPIResponsesAreNotCacheable`. |
| 3 | Medium | `internal/httpapi/tls.go` | Certificate directory was 0750 (spec: 0700), and an existing key file was rewritten in place, so a loosened key stayed readable until a later `chmod`. | **Fixed.** Directory 0700 and re-chmodded on repair; files written to a temp file and renamed. Tests: `TestCertDirectoryMode` (now exact 0700), `TestCertDirectoryModeIsRepaired` (also asserts no temp file is left). Unix-only tests. |
| 4 | Low | `internal/auth/throttle.go` | Per-IP counters key on the full IPv6 address, so a /64 holder rotates addresses freely. The global counter (10x limit) still caps total guesses. | Open. Fold to /64 in a later pass. |
| 5 | Low | `internal/auth/session.go` | Idle timeout only; a session used continuously never expires. | Open; matches the plan (idle timeout). Consider an absolute cap. |
| 6 | Low | `internal/httpapi/server.go` jobs route | An SSE stream authenticated at start keeps streaming after logout or password change until the job ends. Read-only, job-scoped. | Open. |
| 7 | Low | cookie | `pb_session` has no `__Host-` prefix. HttpOnly, Secure, SameSite=Strict, Path=/ are all set. | Open. |
| 8 | Low | `throttle.go` | The global lockout lets an attacker lock out everyone, the operator included, for one window. Documented trade-off in the code; recovery is a daemon restart or the CLI. | Accepted. |
| 9 | Low | `tls.go` | The self-signed leaf is also `IsCA` with `CertSign`. | Open. |

## Checked and sound

- **Session fixation.** There is no anonymous pre-login session. `Create` issues a fresh 256-bit ID at login; the table stores only its SHA-256.
- **CSRF.** Every unsafe method with a session needs a matching `X-CSRF-Token` (constant-time). With no session an unsafe route answers 401 in `requireAuth`. Login is the one unsafe route without a token; `SameSite=Strict` covers login CSRF. Pinned by `TestEveryUnsafeRouteRequiresCSRF`: add new routes to its table.
- **Throttle key.** `clientIP` uses the TCP peer; `X-Forwarded-For` is ignored (`TestClientIPIgnoresForwardedFor`).
- **Timing and enumeration.** Unknown users verify against a dummy hash; one 401 for both failures.
- **Headers.** The security headers wrap the whole mux, so they appear on 401, 404, 405, 500 and SPA responses.
- **argon2id.** m=64 MiB, t=3, p=2, stored in the PHC string; the decoder bounds the parameters.
- **Password change.** Re-verified through the throttle and revokes every session.
