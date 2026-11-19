package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// protectedRoute is one route the matrix below exercises. body is a valid
// request body for the route, so a test that reaches the handler gets a real
// answer rather than a body failure.
type protectedRoute struct {
	name   string
	method string
	path   string
	body   any
	// okStatus is the status a request with a session and no CSRF header must
	// get. Zero means 200. A route whose path names a record that does not
	// exist answers 404 to a well-formed request, and that still proves it
	// reached the handler.
	okStatus int
}

// protectedRoutes is every route except login. Login is the deliberate
// exception (it is how a client gets a session) and is covered by its own tests.
//
// The list is the single source for the 401 and CSRF matrices, so a route added
// to the router without being added here shows up as a coverage gap rather than
// silently going untested.
func protectedRoutes() []protectedRoute {
	return []protectedRoute{
		{name: "logout", method: http.MethodPost, path: "/api/logout"},
		{name: "me", method: http.MethodGet, path: "/api/me"},
		{name: "password", method: http.MethodPost, path: "/api/password",
			body: map[string]string{"current_password": testPassword, "new_password": "a brand new long password"}},
		{name: "username", method: http.MethodPost, path: "/api/username",
			body: map[string]string{"current_password": testPassword, "new_username": "operator"}},
		{name: "settings get", method: http.MethodGet, path: "/api/settings"},
		{name: "settings put", method: http.MethodPut, path: "/api/settings",
			body: map[string]any{"settings": []map[string]string{{"name": "login.max_attempts", "value": "6"}}}},
		{name: "audit", method: http.MethodGet, path: "/api/audit"},
		{name: "job events", method: http.MethodGet, path: "/api/jobs/00000000000000000000000000000000/events",
			okStatus: http.StatusNotFound},
		{name: "jobs list", method: http.MethodGet, path: "/api/jobs"},

		// The nginx and sites routes. This harness has no nginx, so a request
		// that passes the session and CSRF checks reaches the handler and gets
		// its 503; handlers_nginx_test.go covers them with nginx present.
		{name: "sites list", method: http.MethodGet, path: "/api/sites", okStatus: http.StatusServiceUnavailable},
		{name: "sites create", method: http.MethodPost, path: "/api/sites", body: map[string]string{"domain": "a.test", "kind": "static"}, okStatus: http.StatusServiceUnavailable},
		{name: "site get", method: http.MethodGet, path: "/api/sites/a.test", okStatus: http.StatusServiceUnavailable},
		{name: "site put", method: http.MethodPut, path: "/api/sites/a.test", body: map[string]any{"aliases": []string{}}, okStatus: http.StatusServiceUnavailable},
		{name: "site delete", method: http.MethodDelete, path: "/api/sites/a.test?confirm=a.test", okStatus: http.StatusServiceUnavailable},
		{name: "site file get", method: http.MethodGet, path: "/api/sites/a.test/file", okStatus: http.StatusServiceUnavailable},
		{name: "site file put", method: http.MethodPut, path: "/api/sites/a.test/file", body: map[string]string{"content": "x"}, okStatus: http.StatusServiceUnavailable},
		{name: "site regenerate", method: http.MethodPost, path: "/api/sites/a.test/regenerate?confirm=a.test", okStatus: http.StatusServiceUnavailable},
		{name: "nginx settings get", method: http.MethodGet, path: "/api/nginx/settings", okStatus: http.StatusServiceUnavailable},
		{name: "nginx settings put", method: http.MethodPut, path: "/api/nginx/settings", body: map[string]string{"worker_processes": "auto"}, okStatus: http.StatusServiceUnavailable},
		{name: "nginx conf get", method: http.MethodGet, path: "/api/nginx/conf", okStatus: http.StatusServiceUnavailable},
		{name: "nginx conf put", method: http.MethodPut, path: "/api/nginx/conf", body: map[string]string{"content": "x"}, okStatus: http.StatusServiceUnavailable},
		{name: "php list", method: http.MethodGet, path: "/api/php", okStatus: http.StatusServiceUnavailable},
		{name: "php pull", method: http.MethodPost, path: "/api/php/8.3/pull", okStatus: http.StatusServiceUnavailable},
		{name: "php settings get", method: http.MethodGet, path: "/api/php/8.3/settings", okStatus: http.StatusServiceUnavailable},
		{name: "php settings put", method: http.MethodPut, path: "/api/php/8.3/settings", body: map[string]string{"memory_limit": "1M"}, okStatus: http.StatusServiceUnavailable},
		{name: "php ini get", method: http.MethodGet, path: "/api/php/8.3/ini", okStatus: http.StatusServiceUnavailable},
		{name: "php ini put", method: http.MethodPut, path: "/api/php/8.3/ini", body: map[string]string{"content": "x"}, okStatus: http.StatusServiceUnavailable},
		{name: "php extensions", method: http.MethodGet, path: "/api/php/extensions", okStatus: http.StatusServiceUnavailable},
		{name: "php remove", method: http.MethodDelete, path: "/api/php/8.3", okStatus: http.StatusServiceUnavailable},
		{name: "php build", method: http.MethodPost, path: "/api/php/8.3/build", body: map[string]any{"extensions": []string{"intl"}}, okStatus: http.StatusServiceUnavailable},
		{name: "php builds", method: http.MethodGet, path: "/api/php/8.3/builds", okStatus: http.StatusServiceUnavailable},
		{name: "php build activate", method: http.MethodPost, path: "/api/php/8.3/builds/20261117120000-abcdef/activate", body: map[string]any{}, okStatus: http.StatusServiceUnavailable},
		{name: "nginx image", method: http.MethodGet, path: "/api/nginx/image", okStatus: http.StatusServiceUnavailable},
		{name: "nginx image update", method: http.MethodPost, path: "/api/nginx/image/update", body: map[string]any{}, okStatus: http.StatusServiceUnavailable},
		{name: "nginx image activate", method: http.MethodPost, path: "/api/nginx/image/builds/20261117120000-abcdef/activate", body: map[string]any{}, okStatus: http.StatusServiceUnavailable},
		{name: "site pool get", method: http.MethodGet, path: "/api/sites/a.test/pool", okStatus: http.StatusServiceUnavailable},
		{name: "site pool put", method: http.MethodPut, path: "/api/sites/a.test/pool", body: map[string]string{"pm": "static"}, okStatus: http.StatusServiceUnavailable},
		{name: "site php version", method: http.MethodPut, path: "/api/sites/a.test/php-version", body: map[string]string{"version": "8.4"}, okStatus: http.StatusServiceUnavailable},
		{name: "history list", method: http.MethodGet, path: "/api/config-history/nginx.conf", okStatus: http.StatusServiceUnavailable},
		{name: "history version", method: http.MethodGet, path: "/api/config-history/nginx.conf/1", okStatus: http.StatusServiceUnavailable},
		{name: "history restore", method: http.MethodPost, path: "/api/config-history/nginx.conf/1/restore", okStatus: http.StatusServiceUnavailable},
		{name: "nginx service", method: http.MethodGet, path: "/api/services/nginx", okStatus: http.StatusServiceUnavailable},

		// M7: dashboard, log viewer and cron. No dependency is wired here, so a
		// request that gets past the session and CSRF checks answers 503.
		{name: "sysinfo", method: http.MethodGet, path: "/api/sysinfo", okStatus: http.StatusServiceUnavailable},
		{name: "sysinfo stream", method: http.MethodGet, path: "/api/sysinfo/stream", okStatus: http.StatusServiceUnavailable},
		{name: "services list", method: http.MethodGet, path: "/api/services", okStatus: http.StatusServiceUnavailable},
		{name: "services control", method: http.MethodPost, path: "/api/services/panel/restart", body: map[string]any{}, okStatus: http.StatusServiceUnavailable},
		{name: "logs list", method: http.MethodGet, path: "/api/logs", okStatus: http.StatusServiceUnavailable},
		{name: "logs tail", method: http.MethodGet, path: "/api/logs/panel:audit/tail", okStatus: http.StatusServiceUnavailable},
		{name: "logs search", method: http.MethodGet, path: "/api/logs/panel:audit/search?q=a", okStatus: http.StatusServiceUnavailable},
		{name: "cron list", method: http.MethodGet, path: "/api/cron", okStatus: http.StatusServiceUnavailable},
		{name: "cron create", method: http.MethodPost, path: "/api/cron", body: map[string]any{"name": "a"}, okStatus: http.StatusServiceUnavailable},
		{name: "cron get", method: http.MethodGet, path: "/api/cron/1", okStatus: http.StatusServiceUnavailable},
		{name: "cron put", method: http.MethodPut, path: "/api/cron/1", body: map[string]any{"schedule": "* * * * *"}, okStatus: http.StatusServiceUnavailable},
		{name: "cron delete", method: http.MethodDelete, path: "/api/cron/1", okStatus: http.StatusServiceUnavailable},
		{name: "cron enable", method: http.MethodPost, path: "/api/cron/1/enable", body: map[string]any{}, okStatus: http.StatusServiceUnavailable},
		{name: "cron disable", method: http.MethodPost, path: "/api/cron/1/disable", body: map[string]any{}, okStatus: http.StatusServiceUnavailable},
		{name: "cron run", method: http.MethodPost, path: "/api/cron/1/run", body: map[string]any{}, okStatus: http.StatusServiceUnavailable},
		{name: "cron runs", method: http.MethodGet, path: "/api/cron/1/runs", okStatus: http.StatusServiceUnavailable},
		{name: "cron run detail", method: http.MethodGet, path: "/api/cron/1/runs/2", okStatus: http.StatusServiceUnavailable},
	}
}

// TestUnauthenticatedIsRejected covers the "unauthenticated access to any route
// except login returns 401" requirement for every route, including unsafe
// methods carrying a CSRF token that belongs to nobody.
func TestUnauthenticatedIsRejected(t *testing.T) {
	h := newHarness(t)

	for _, route := range protectedRoutes() {
		t.Run(route.name, func(t *testing.T) {
			rec := h.do(route.method, route.path, "", "", route.body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			env := h.errorEnvelopeOf(rec)
			if env.Error != errUnauthorized {
				t.Errorf("error = %q, want %q", env.Error, errUnauthorized)
			}
		})
	}
}

// TestUnauthenticatedWithStrayCSRFStill401 proves a header cannot substitute for
// a session: an attacker who guesses a CSRF token and sends no cookie is still
// turned away with 401, not let through to the handler.
func TestUnauthenticatedWithStrayCSRFStill401(t *testing.T) {
	h := newHarness(t)

	for _, route := range protectedRoutes() {
		t.Run(route.name, func(t *testing.T) {
			rec := h.do(route.method, route.path, "", "some-guessed-token", route.body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestExpiredOrUnknownCookieIs401 proves a cookie the server does not recognise
// is treated as no session at all.
func TestExpiredOrUnknownCookieIs401(t *testing.T) {
	h := newHarness(t)

	for _, route := range protectedRoutes() {
		t.Run(route.name, func(t *testing.T) {
			rec := h.do(route.method, route.path, "not-a-real-session-id", "not-a-real-token", route.body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestCSRFMissingIsForbidden covers the "unsafe methods without a valid CSRF
// token return 403" requirement, for the missing case.
func TestCSRFMissingIsForbidden(t *testing.T) {
	h := newHarness(t)

	for _, route := range protectedRoutes() {
		if isSafeMethod(route.method) {
			continue
		}
		t.Run(route.name, func(t *testing.T) {
			s := h.login()
			rec := h.do(route.method, route.path, s.cookie, "", route.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
			if env := h.errorEnvelopeOf(rec); env.Error != errInvalidCSRF {
				t.Errorf("error = %q, want %q", env.Error, errInvalidCSRF)
			}
		})
	}
}

// TestCSRFWrongIsForbidden covers the wrong-token case, including a token that
// belongs to a different session.
func TestCSRFWrongIsForbidden(t *testing.T) {
	h := newHarness(t)

	other := h.login()

	for _, route := range protectedRoutes() {
		if isSafeMethod(route.method) {
			continue
		}
		t.Run(route.name, func(t *testing.T) {
			s := h.login()

			for _, token := range []string{"wrong", other.csrf} {
				rec := h.do(route.method, route.path, s.cookie, token, route.body)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status with token %q = %d, want 403 (body %s)", token, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// TestSafeMethodsNeedNoCSRF proves the read routes are usable with a session and
// no CSRF header, which is what lets the SPA poll them without ceremony.
func TestSafeMethodsNeedNoCSRF(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	for _, route := range protectedRoutes() {
		if !isSafeMethod(route.method) {
			continue
		}
		t.Run(route.name, func(t *testing.T) {
			rec := h.do(route.method, route.path, s.cookie, "", route.body)
			want := route.okStatus
			if want == 0 {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, want, rec.Body.String())
			}
		})
	}
}

// TestMalformedBodyIs400 covers the error-envelope-on-bad-input requirement for
// every route that takes a body. The CSRF token is valid, so the request reaches
// the decoder and the 400 is the decoder's answer, not the guard's.
func TestMalformedBodyIs400(t *testing.T) {
	h := newHarness(t)

	bodies := map[string]string{
		"empty":         "",
		"not json":      "this is not json",
		"array":         "[1,2,3]",
		"truncated":     `{"settings": [`,
		"two values":    `{"username":"a","password":"b"}{"username":"c"}`,
		"unknown field": `{"username":"a","password":"b","surprise":true}`,
	}

	routes := []protectedRoute{
		{name: "password", method: http.MethodPost, path: "/api/password"},
		{name: "username", method: http.MethodPost, path: "/api/username"},
		{name: "settings put", method: http.MethodPut, path: "/api/settings"},
	}

	for _, route := range routes {
		for name, body := range bodies {
			t.Run(route.name+"/"+name, func(t *testing.T) {
				s := h.login()
				rec := h.doRaw(route.method, route.path, s.cookie, s.csrf, body)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
				}
				h.errorEnvelopeOf(rec)
			})
		}
	}

	t.Run("login", func(t *testing.T) {
		for name, body := range bodies {
			t.Run(name, func(t *testing.T) {
				rec := h.doRaw(http.MethodPost, "/api/login", "", "", body)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
				}
				h.errorEnvelopeOf(rec)
			})
		}
	})
}

// TestErrorEnvelopeIsExactlyTwoFields proves the envelope's shape is the
// contract and nothing else: a client that finds a third field, or a body that
// is not an object, would be reading a different API.
func TestErrorEnvelopeIsExactlyTwoFields(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodGet, "/api/me", "", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	var raw map[string]json.RawMessage
	h.decode(rec, &raw)
	if len(raw) != 2 {
		t.Fatalf("envelope has %d fields, want 2: %s", len(raw), rec.Body.String())
	}
	for _, key := range []string{"error", "detail"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("envelope is missing %q: %s", key, rec.Body.String())
		}
	}
}

// TestWrongMethodIsNotRouted proves the method is part of the route: a GET to a
// POST-only path is a 405 from the mux, so a handler never has to check the
// method itself.
func TestWrongMethodIsNotRouted(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/login"},
		{http.MethodDelete, "/api/settings"},
		{http.MethodPost, "/api/me"},
	}
	for _, c := range cases {
		rec := h.do(c.method, c.path, s.cookie, s.csrf, nil)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status = %d, want 405 (body %s)", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// TestNoRouteLeaksInternals is the belt to TestErrorEnvelopeIsExactlyTwoFields'
// braces: no route's error body may contain a stack trace or a source path.
func TestNoRouteLeaksInternals(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	for _, route := range protectedRoutes() {
		rec := h.do(route.method, route.path, s.cookie, s.csrf, route.body)
		body := rec.Body.Bytes()
		if bytes.Contains(body, []byte("goroutine")) || bytes.Contains(body, []byte(".go:")) {
			t.Errorf("%s leaks internals: %s", route.name, body)
		}
	}
}

// TestHealthNeedsNoSession covers GET /api/health, the one route besides login
// that answers without a cookie. `panel upgrade` polls it to decide whether a new
// binary came up, so a 401 here would roll back every upgrade.
func TestHealthNeedsNoSession(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodGet, "/api/health", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var body healthResponse
	h.decode(rec, &body)
	if body.Status != "ok" {
		t.Errorf("status field = %q, want ok", body.Status)
	}
	if body.Version == "" {
		t.Error("the health body carries no version")
	}
}

// TestHealthIsGetOnly proves the probe cannot be used to reach a handler that
// changes something.
func TestHealthIsGetOnly(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/health", "", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/health = %d, want 405", rec.Code)
	}
}
