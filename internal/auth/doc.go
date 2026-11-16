// Package auth covers the admin password, sessions, failed-login throttling and
// CSRF tokens.
//
// It deliberately has no dependency on net/http: every entry point takes plain
// values (an IP address string, a user agent, a token from a header) so the
// package can be exercised without a request, and so the HTTP layer cannot leak
// request state into the security decisions made here.
//
// Decisions this package implements (plan.md): argon2id password hashes, one
// admin account, a session cookie whose value is never stored, an idle timeout,
// all sessions revoked on a password change, a CSRF token bound to the session,
// and failed-login throttling per IP and globally. The TCP peer address is
// always supplied by the caller; nothing here reads X-Forwarded-For, which a
// client controls and which would let an attacker reset their own counter.
package auth
