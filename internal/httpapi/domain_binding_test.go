package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/settings"
)

// hostRequest sends one request through the full chain with an explicit Host
// header. httptest.NewRequest takes the Host from the absolute URL, so the URL
// carries the host the test is pretending the client used.
func (h *harness) hostRequest(host, method, path, cookie, csrf string, body any) *httptest.ResponseRecorder {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}

	req := httptest.NewRequest(method, "https://"+host+path, reader)
	req.RemoteAddr = "203.0.113.9:54321"
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	}
	if csrf != "" {
		req.Header.Set(csrfHeader, csrf)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// loginOn signs in through host, so a bound panel can be reached at all.
func (h *harness) loginOn(host string) session {
	h.t.Helper()

	creds := map[string]string{"username": testUser, "password": testPassword}
	rec := h.hostRequest(host, http.MethodPost, "/api/login", "", "", creds)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("login on %s: status %d, body %s", host, rec.Code, rec.Body.String())
	}

	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode login: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		h.t.Fatal("login set no cookie")
	}
	return session{cookie: cookies[0].Value, csrf: resp.CSRFToken}
}

func (h *harness) storedDomain() string {
	h.t.Helper()
	v, err := h.deps.Settings.Str(context.Background(), settings.KeyPanelDomain)
	if err != nil {
		h.t.Fatalf("read %s: %v", settings.KeyPanelDomain, err)
	}
	return v
}

func (h *harness) bindDomain(domain string) {
	h.t.Helper()
	if err := h.deps.Settings.Set(context.Background(), settings.KeyPanelDomain, domain); err != nil {
		h.t.Fatalf("Set %s: %v", settings.KeyPanelDomain, err)
	}
}

func TestDomainBindingIsOffUntilSet(t *testing.T) {
	h := newHarness(t)

	rec := h.hostRequest("203.0.113.5:8443", http.MethodGet, "/api/me", "", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unbound panel on an IP host: status %d, want 401 from the session check", rec.Code)
	}
}

func TestDomainBindingRefusesAForeignHost(t *testing.T) {
	h := newHarness(t)
	h.bindDomain("panel.example.com")

	for _, host := range []string{"203.0.113.5:8443", "other.example", "panel.example.org"} {
		rec := h.hostRequest(host, http.MethodGet, "/api/me", "", "", nil)
		if rec.Code != http.StatusMisdirectedRequest {
			t.Fatalf("host %q: status %d, want 421", host, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "panel.example.com") {
			t.Fatalf("host %q: body %q does not name the bound domain", host, rec.Body.String())
		}
	}
}

func TestDomainBindingAcceptsTheBoundHostWhateverItsCaseOrPort(t *testing.T) {
	h := newHarness(t)
	h.bindDomain("panel.example.com")

	for _, host := range []string{"panel.example.com", "PANEL.Example.COM:8443", "panel.example.com."} {
		rec := h.hostRequest(host, http.MethodGet, "/api/me", "", "", nil)
		// The session check answers 401: the request got past the binding.
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("host %q: status %d, want 401 from the session check", host, rec.Code)
		}
	}
}

func TestDomainBindingLetsTheHealthProbeThrough(t *testing.T) {
	h := newHarness(t)
	h.bindDomain("panel.example.com")

	rec := h.hostRequest("127.0.0.1:8443", http.MethodGet, "/api/health", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health on a foreign host: status %d, want 200 so the upgrade probe still works", rec.Code)
	}
}

func TestDomainBindingIsSetOnlyFromTheDomain(t *testing.T) {
	h := newHarness(t)
	put := func(host, value string, s session) *httptest.ResponseRecorder {
		return h.hostRequest(host, http.MethodPut, "/api/settings", s.cookie, s.csrf,
			map[string]any{"settings": []settingWrite{{Name: settings.KeyPanelDomain, Value: value}}})
	}

	s := h.loginOn("panel.example.com")

	// Typed from an address the operator is not using: refused, nothing stored.
	if rec := put("203.0.113.5:8443", "panel.example.com", s); rec.Code != http.StatusBadRequest {
		t.Fatalf("bind from another host: status %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if got := h.storedDomain(); got != "" {
		t.Fatalf("refused bind still stored %q", got)
	}

	// From the domain itself: accepted and in effect at once.
	if rec := put("panel.example.com", "panel.example.com", s); rec.Code != http.StatusOK {
		t.Fatalf("bind from the domain: status %d; body %s", rec.Code, rec.Body.String())
	}
	if got := h.storedDomain(); got != "panel.example.com" {
		t.Fatalf("stored domain = %q, want panel.example.com", got)
	}
	if rec := h.hostRequest("203.0.113.5:8443", http.MethodGet, "/api/me", "", "", nil); rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("after binding, IP host: status %d, want 421", rec.Code)
	}

	// Clearing from the domain is allowed and takes effect at once.
	if rec := put("panel.example.com", "", s); rec.Code != http.StatusOK {
		t.Fatalf("clear from the domain: status %d; body %s", rec.Code, rec.Body.String())
	}
	if got := h.storedDomain(); got != "" {
		t.Fatalf("after clearing, stored domain = %q, want empty", got)
	}
}

func TestDomainBindingRejectsAnAddressAsTheName(t *testing.T) {
	h := newHarness(t)
	s := h.loginOn("192.168.1.5")

	rec := h.hostRequest("192.168.1.5", http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []settingWrite{{Name: settings.KeyPanelDomain, Value: "192.168.1.5"}}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "domain name") {
		t.Fatalf("IP as the binding: status %d body %s, want 400 naming a domain", rec.Code, rec.Body.String())
	}
}
