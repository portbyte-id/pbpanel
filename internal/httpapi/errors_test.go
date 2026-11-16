package httpapi

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBodyOverLimitIs400 proves the size cap answers with the envelope rather
// than letting the decoder allocate without bound or closing the connection
// with no body.
func TestBodyOverLimitIs400(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	huge := `{"settings":[{"name":"panel.bind","value":"` + strings.Repeat("x", maxBody) + `"}]}`
	rec := h.doRaw(http.MethodPut, "/api/settings", s.cookie, s.csrf, huge)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); !strings.Contains(env.Detail, "too large") {
		t.Errorf("detail = %q, want it to name the size limit", env.Detail)
	}
}

// TestInternalErrorIsGeneric proves a handler's internal failure becomes a 500
// with a detail that says nothing about the server, which is what writeInternal
// exists for. Dropping the audit table forces a real query failure after the
// session has already resolved, so the 500 comes from the handler and not from
// the auth guard.
func TestInternalErrorIsGeneric(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	if _, err := h.store.DB().Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatalf("drop audit_log: %v", err)
	}

	rec := h.do(http.MethodGet, "/api/audit", s.cookie, "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}

	env := h.errorEnvelopeOf(rec)
	if env.Error != errInternal {
		t.Errorf("error = %q, want %q", env.Error, errInternal)
	}
	if env.Detail != "the request could not be completed" {
		t.Errorf("detail = %q, want the generic message", env.Detail)
	}
	// The database error must not have reached the client.
	for _, leak := range []string{"sql", "database", "closed"} {
		if strings.Contains(strings.ToLower(env.Detail), leak) {
			t.Errorf("detail leaks %q: %s", leak, env.Detail)
		}
	}
}

// TestNewRequiresDependencies proves a missing dependency fails at construction,
// so a wiring mistake cannot become a nil dereference on the first request.
func TestNewRequiresDependencies(t *testing.T) {
	full := newHarness(t).deps

	cases := map[string]func(*Deps){
		"store":    func(d *Deps) { d.Store = nil },
		"settings": func(d *Deps) { d.Settings = nil },
		"auth":     func(d *Deps) { d.Auth = nil },
		"audit":    func(d *Deps) { d.Audit = nil },
	}
	for name, drop := range cases {
		t.Run(name, func(t *testing.T) {
			deps := full
			drop(&deps)
			if _, err := New(deps); err == nil {
				t.Fatalf("New accepted Deps without %s", name)
			}
		})
	}
}

// TestNewFillsDefaults proves the optional fields are filled in, so a caller
// that omits them still gets a working server. New takes Deps by value, so the
// defaults are visible through the server it builds rather than on the caller's
// copy.
func TestNewFillsDefaults(t *testing.T) {
	deps := newHarness(t).deps
	deps.Logger = nil
	deps.Actor = ""

	h, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The server answers, which proves the nil logger did not survive.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	// validate is what fills the defaults; calling it on a copy shows them.
	filled := deps
	if err := filled.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if filled.Actor != defaultActor {
		t.Errorf("Actor = %q, want %q", filled.Actor, defaultActor)
	}
	if filled.Logger == nil {
		t.Error("Logger was not defaulted")
	}
}

// TestIsClosed proves the helper a serve loop uses to tell a clean shutdown from
// a crash recognises both errors Go uses for that.
func TestIsClosed(t *testing.T) {
	if !IsClosed(http.ErrServerClosed) {
		t.Error("IsClosed(http.ErrServerClosed) = false")
	}
	if !IsClosed(net.ErrClosed) {
		t.Error("IsClosed(net.ErrClosed) = false")
	}
	if IsClosed(errors.New("address already in use")) {
		t.Error("IsClosed treated a real failure as a clean shutdown")
	}
}

// TestStatusRecorderUnwrap proves the recorder exposes the underlying writer, so
// http.ResponseController can still flush a streamed response.
func TestStatusRecorderUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec}

	if got := sr.Unwrap(); got != http.ResponseWriter(rec) {
		t.Errorf("Unwrap returned %T, want the original writer", got)
	}

	// A handler that writes without calling WriteHeader records a 200.
	if _, err := sr.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sr.code() != http.StatusOK {
		t.Errorf("code = %d, want 200", sr.code())
	}
}

// TestListenAndServeTLSServes proves the entry point the daemon calls actually
// serves TLS on the registry's address. The server is stopped through the
// context, which also exercises the shutdown goroutine.
func TestListenAndServeTLSServes(t *testing.T) {
	h := newHarness(t)
	reg := h.deps.Settings
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"127.0.0.1"}); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	port := freePort(t)
	ctx := context.Background()
	if err := reg.Set(ctx, "panel.bind", "127.0.0.1"); err != nil {
		t.Fatalf("set panel.bind: %v", err)
	}
	if err := reg.Set(ctx, "panel.port", itoa(port)); err != nil {
		t.Fatalf("set panel.port: %v", err)
	}

	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- ListenAndServeTLS(serveCtx, reg, paths, h.handler) }()

	// Wait until the listener answers, so cancelling tests shutdown and not a
	// race with startup.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed in a temp dir
	}}
	addr := "https://127.0.0.1:" + itoa(port)
	var up bool
	for range 100 {
		resp, err := client.Get(addr + "/api/me")
		if err == nil {
			_ = resp.Body.Close()
			up = true
			break
		}
	}
	if !up {
		cancel()
		t.Fatal("the TLS listener never answered")
	}

	// Cancelling must shut the server down and return a clean result, which is
	// what the daemon's signal handler relies on.
	cancel()
	select {
	case err := <-done:
		if err != nil && !IsClosed(err) {
			t.Errorf("ListenAndServeTLS returned %v after shutdown, want nil or a closed error", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ListenAndServeTLS did not return after its context was cancelled")
	}
}

// freePort returns a port nothing is listening on, for a test that has to bind.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return port
}
