package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/portbyte-id/server-panel/internal/auth"
)

const (
	// sessionCookieName is the cookie the browser holds. The cookie is the only
	// credential the API accepts; there is no bearer token and no query-string
	// fallback, so a URL cannot leak a session.
	sessionCookieName = "pb_session"

	// csrfHeader is the header a client must echo the session's CSRF token in
	// on an unsafe method.
	csrfHeader = "X-CSRF-Token"

	// maxBody caps request bodies. The largest body the API takes is a settings
	// write of one key, so a megabyte is generous and stops a client from
	// making the daemon allocate without bound.
	maxBody = 1 << 20
)

// sessionKey is the context key the session middleware stores the session under.
type sessionKey struct{}

// sessionFrom returns the authenticated session. ok is false for a request that
// reached a handler without one, which only a test calling a handler directly
// can do: the router answers 401 before the handler for every protected route.
func sessionFrom(ctx context.Context) (auth.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(auth.Session)
	return s, ok
}

// withSession resolves the session cookie to a session and attaches it to the
// context. It never rejects a request: deciding that a route needs a session is
// the auth guard's job, so login can run on the same chain as everything else.
func (s *server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}

		session, err := s.deps.Auth.Sessions().Lookup(r.Context(), cookie.Value)
		if err != nil {
			// ErrNoSession is the ordinary expired/revoked case and needs no
			// log line; anything else is a database failure worth reporting.
			if !errors.Is(err, auth.ErrNoSession) {
				s.logger.ErrorContext(r.Context(), "session lookup failed",
					"error", err, "request_id", requestIDFrom(r.Context()))
			}
			next.ServeHTTP(w, r)
			return
		}
		// Lookup reads the row back and cannot fill ID: the table stores only the
		// hash. The cookie is the one place the live ID exists, so it is put back
		// here — without it, logout and password change revoke the empty string
		// and leave the cookie working.
		session.ID = cookie.Value
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, session)))
	})
}

// requireAuth rejects a request with 401 unless the session middleware attached
// a session. Every route except login and the health probe is wrapped in it.
func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := sessionFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, errUnauthorized, "log in to use this endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isSafeMethod reports whether a method is read-only. A safe method is exempt
// from the CSRF check because a cross-site page can trigger it but cannot read
// the response, so it can neither change state nor learn anything.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// withCSRF rejects an unsafe method whose X-CSRF-Token header does not match the
// token bound to the session. It runs after the session middleware, so the
// token it compares against is the one the server issued for this session.
func (s *server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		session, ok := sessionFrom(r.Context())
		if !ok {
			// No session: there is no token to compare against, and login — the
			// one unsafe route a client reaches without a session — must pass.
			// A route that requires auth answers 401 in requireAuth, which runs
			// inside this middleware, so nothing unauthenticated slips through.
			next.ServeHTTP(w, r)
			return
		}
		if !auth.CheckCSRF(session, r.Header.Get(csrfHeader)) {
			writeError(w, http.StatusForbidden, errInvalidCSRF,
				"missing or invalid "+csrfHeader+" header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the address a login is throttled against. It is the TCP peer as
// the listener saw it: X-Forwarded-For is deliberately ignored, because a
// client that could set it would pick a fresh throttle counter on every
// attempt and never be locked out.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
