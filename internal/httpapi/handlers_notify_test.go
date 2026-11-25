package httpapi

import (
	"net/http"
	"testing"
)

// notifyPaths is every route of §4.5 with a representative method.
var notifyPaths = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/notify/events"},
	{http.MethodGet, "/api/notify/summary"},
	{http.MethodPost, "/api/notify/events/read"},
	{http.MethodGet, "/api/notify/stream"},
	{http.MethodGet, "/api/notify/kinds"},
	{http.MethodGet, "/api/notify/channels"},
	{http.MethodPost, "/api/notify/channels"},
	{http.MethodGet, "/api/notify/channels/1"},
	{http.MethodPut, "/api/notify/channels/1"},
	{http.MethodDelete, "/api/notify/channels/1"},
	{http.MethodPost, "/api/notify/channels/1/test"},
	{http.MethodGet, "/api/notify/deliveries"},
	{http.MethodPost, "/api/notify/deliveries/1/retry"},
}

// Every notify route is behind the session guard.
func TestNotifyRoutesRequireSession(t *testing.T) {
	h := newHarness(t)
	for _, c := range notifyPaths {
		rec := h.do(c.method, c.path, "", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

// With no notify service wired (a host without data/secret.key, or a failed
// start), every route answers 503 with the reason, not a 404.
func TestNotifyRoutesAnswer503WithoutService(t *testing.T) {
	h := newHarness(t)
	s := h.login()
	for _, c := range notifyPaths {
		rec := h.do(c.method, c.path, s.cookie, s.csrf, nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}
