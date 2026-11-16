package httpapi

import (
	"errors"
	"net/http"
	"testing"
)

func harnessWithRebind(t *testing.T, fn func(int) error) *harness {
	t.Helper()
	h := newHarness(t)
	deps := h.deps
	deps.Rebind = fn
	handler, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.handler = handler
	return h
}

func putPort(h *harness, s session, value string) int {
	body := map[string]any{"settings": []map[string]string{{"name": "panel.port", "value": value}}}
	return h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf, body).Code
}

// TestPortChangeRebindsThenStores: a valid new port moves the listener and is
// then stored.
func TestPortChangeRebindsThenStores(t *testing.T) {
	var got []int
	h := harnessWithRebind(t, func(p int) error { got = append(got, p); return nil })
	s := h.login()

	if code := putPort(h, s, "9443"); code != http.StatusOK {
		t.Fatalf("PUT panel.port = %d, want 200", code)
	}
	if len(got) != 1 || got[0] != 9443 {
		t.Fatalf("Rebind calls = %v, want [9443]", got)
	}
	if v, _ := h.deps.Settings.Int(t.Context(), "panel.port"); v != 9443 {
		t.Errorf("stored panel.port = %d, want 9443", v)
	}
}

// TestFailedRebindLeavesThePortSettingUnchanged is the 0.8 requirement: when the
// new port cannot be bound the write is refused and nothing is stored.
func TestFailedRebindLeavesThePortSettingUnchanged(t *testing.T) {
	h := harnessWithRebind(t, func(int) error { return errors.New("address already in use") })
	s := h.login()
	before, _ := h.deps.Settings.Int(t.Context(), "panel.port")

	if code := putPort(h, s, "9443"); code != http.StatusBadRequest {
		t.Fatalf("PUT with a busy port = %d, want 400", code)
	}
	after, _ := h.deps.Settings.Int(t.Context(), "panel.port")
	if after != before {
		t.Errorf("panel.port changed from %d to %d despite the failed rebind", before, after)
	}
}

// TestInvalidPortNeverReachesTheListener: a value the registry rejects must not
// move the listener first.
func TestInvalidPortNeverReachesTheListener(t *testing.T) {
	called := false
	h := harnessWithRebind(t, func(int) error { called = true; return nil })
	s := h.login()

	if code := putPort(h, s, "99999"); code != http.StatusBadRequest {
		t.Fatalf("PUT invalid port = %d, want 400", code)
	}
	if called {
		t.Error("Rebind was called for a value the registry rejects")
	}
}
