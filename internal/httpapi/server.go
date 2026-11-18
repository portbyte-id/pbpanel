package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/portbyte-id/server-panel/internal/jobs"
)

// server carries the dependencies every handler and middleware needs. There is
// no package-level state: one server value owns one Deps, which is what lets a
// test run two independent panels in one process.
type server struct {
	deps   Deps
	logger *slog.Logger
}

// New builds the HTTP handler for the panel: the middleware chain and every
// route. The returned handler is ready for httptest or a real listener.
//
// The chain runs outermost first:
//
//	request ID -> recover -> security headers -> session -> CSRF -> audit -> route
//
// Request ID is first so every later line can be correlated; recover is next so
// a panic in anything inside it still becomes a JSON 500; headers are set before
// any body is written; session resolves the cookie for both the CSRF check and
// the handlers; CSRF guards every unsafe method; audit records the outcome last,
// after the handler has decided it.
func New(deps Deps) (http.Handler, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	s := &server{deps: deps, logger: deps.Logger}
	return s.routes(), nil
}

// routes builds the mux and wraps it in the chain. Patterns use the go1.22
// method+path syntax, so the method is part of the route: a GET to a POST-only
// path is a 405 from the mux, not a handler that has to check the method itself.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Public: login is the only endpoint reachable without a session. Every
	// other route is wrapped in requireAuth.
	mux.Handle("POST /api/login", http.HandlerFunc(s.handleLogin))

	// The health probe answers without a session. `panel upgrade` uses it to
	// decide whether a new binary came up, and the smoke script uses it to wait
	// for a restart. It reads nothing, so it cannot leak state and cannot fail
	// because the database is busy.
	mux.Handle("GET /api/health", http.HandlerFunc(s.handleHealth))

	mux.Handle("POST /api/logout", s.requireAuth(http.HandlerFunc(s.handleLogout)))

	mux.Handle("GET /api/me", s.requireAuth(http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /api/password", s.requireAuth(http.HandlerFunc(s.handlePassword)))

	mux.Handle("GET /api/settings", s.requireAuth(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PUT /api/settings", s.requireAuth(http.HandlerFunc(s.handlePutSettings)))

	mux.Handle("GET /api/audit", s.requireAuth(http.HandlerFunc(s.handleAudit)))

	s.nginxRoutes(mux)
	s.fmRoutes(mux)
	s.firewallRoutes(mux)
	s.appsRoutes(mux)
	s.tlsRoutes(mux)
	s.sysinfoRoutes(mux)
	s.logsRoutes(mux)
	s.cronRoutes(mux)
	s.wafMonRoutes(mux)

	// The jobs SSE handler is served by internal/jobs and guarded here, so a job
	// id is not a way to read another session's output without a session.
	mux.Handle(jobs.EventsPattern, s.requireAuth(http.HandlerFunc(s.deps.Jobs.ServeEvents)))

	// The SPA is deliberately not behind requireAuth: its files are what renders
	// the login page, and the API is what decides who may do anything. A route
	// guard in the browser is a convenience, not the boundary — every protected
	// route above still answers 401 on its own.
	if s.deps.UI != nil {
		// An unknown /api path is a missing endpoint, not a client-side route.
		// Without this, the SPA fallback would answer it with HTML and a client
		// parsing JSON would report a syntax error instead of a 404.
		//
		// Both patterns here are method-less. The mux refuses a GET-only "/"
		// beside a method-less "/api/", because the narrower path would be the
		// more general method; the GET check therefore happens in the handler.
		mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, errNotFound, "no such endpoint")
		}))
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isSafeMethod(r.Method) {
				w.Header().Set("Allow", "GET, HEAD, OPTIONS")
				writeError(w, http.StatusMethodNotAllowed, errBadRequest,
					"the interface is served over GET")
				return
			}
			s.deps.UI.ServeHTTP(w, r)
		}))
	}

	return s.middleware(mux)
}

// middleware applies the chain in the order documented on New.
func (s *server) middleware(next http.Handler) http.Handler {
	h := next
	h = s.withAudit(h)
	h = s.withCSRF(h)
	h = s.withSession(h)
	h = withSecurityHeaders(h)
	h = s.withRecover(h)
	h = withRequestID(h)
	return h
}
