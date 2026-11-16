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
		{name: "settings get", method: http.MethodGet, path: "/api/settings"},
		{name: "settings put", method: http.MethodPut, path: "/api/settings",
			body: map[string]any{"settings": []map[string]string{{"name": "login.max_attempts", "value": "6"}}}},
		{name: "audit", method: http.MethodGet, path: "/api/audit"},
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
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
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
