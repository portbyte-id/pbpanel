package httpapi

import (
	"net/http"
	"testing"
)

// TestAPIResponsesAreNotCacheable is the regression for the 0.15 finding that
// API bodies (which carry the CSRF token) had no Cache-Control, so a shared
// browser or proxy cache could keep them.
func TestAPIResponsesAreNotCacheable(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	for _, tc := range []struct {
		name, method, path string
		cookie, csrf       string
	}{
		{"authenticated 200", http.MethodGet, "/api/me", s.cookie, s.csrf},
		{"unauthenticated 401", http.MethodGet, "/api/me", "", ""},
		{"unknown api path", http.MethodGet, "/api/nope", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(tc.method, tc.path, tc.cookie, tc.csrf, nil)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s %s: Cache-Control = %q, want no-store (status %d)", tc.method, tc.path, got, rec.Code)
			}
		})
	}
}

// TestEveryUnsafeRouteRequiresCSRF pins the coverage the review checked by
// hand: with a valid session and no token, no unsafe method on any API route
// may succeed. A route added later belongs in this table.
func TestEveryUnsafeRouteRequiresCSRF(t *testing.T) {
	h := newHarness(t)

	routes := []struct {
		method, path string
		want403      bool
	}{
		{http.MethodPost, "/api/logout", true},
		{http.MethodPost, "/api/password", true},
		{http.MethodPost, "/api/username", true},
		{http.MethodPut, "/api/settings", true},
		{http.MethodDelete, "/api/settings", false},
		{http.MethodPatch, "/api/settings", false},
		{http.MethodPost, "/api/audit", false},
		{http.MethodPost, "/api/jobs/1/events", false},
		{http.MethodDelete, "/api/jobs/1/events", false},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			s := h.login()
			rec := h.do(rt.method, rt.path, s.cookie, "", map[string]string{})
			if rec.Code < 400 {
				t.Fatalf("%s %s without a CSRF token = %d, want an error status", rt.method, rt.path, rec.Code)
			}
			if rt.want403 && rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s without a CSRF token = %d, want 403", rt.method, rt.path, rec.Code)
			}
		})
	}
}
