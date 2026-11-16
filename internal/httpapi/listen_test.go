package httpapi

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

// TestListenAddrComesFromRegistry proves the listener address is read from the
// settings registry and nowhere else: changing panel.port through the registry
// changes the address the listener would bind.
func TestListenAddrComesFromRegistry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg, err := settings.New(st)
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	ctx := context.Background()

	got, err := ListenAddr(ctx, reg)
	if err != nil {
		t.Fatalf("ListenAddr: %v", err)
	}
	if got != "0.0.0.0:8443" {
		t.Errorf("ListenAddr = %q, want the registry defaults 0.0.0.0:8443", got)
	}

	if err := reg.Set(ctx, "panel.bind", "127.0.0.1"); err != nil {
		t.Fatalf("set panel.bind: %v", err)
	}
	if err := reg.Set(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("set panel.port: %v", err)
	}

	got, err = ListenAddr(ctx, reg)
	if err != nil {
		t.Fatalf("ListenAddr after set: %v", err)
	}
	if got != "127.0.0.1:9443" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:9443", got)
	}
}

// TestNewServerServesOverTLS proves the listener helper works end to end: a
// server built from generated paths answers a real HTTPS request.
func TestNewServerServesOverTLS(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg, err := settings.New(st)
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}

	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"127.0.0.1"}); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	// A listener on an ephemeral port, so the test cannot collide with anything.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	ctx := context.Background()
	if err := reg.Set(ctx, "panel.bind", "127.0.0.1"); err != nil {
		t.Fatalf("set panel.bind: %v", err)
	}
	if err := reg.Set(ctx, "panel.port", itoa(port)); err != nil {
		t.Fatalf("set panel.port: %v", err)
	}

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	h := newHarness(t)
	srv, err := NewServer(serveCtx, reg, paths, h.handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	go func() { _ = srv.ListenAndServeTLS("", "") }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed in a temp dir
		},
	}

	// The daemon binds asynchronously, so retry until it answers or the test
	// gives up; this is a race with the goroutine, not a flaky assertion.
	var resp *http.Response
	for range 50 {
		resp, err = client.Get("https://" + srv.Addr + "/api/me")
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("GET over TLS: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 from the real chain", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Error("response did not arrive over TLS")
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("security headers are missing over the real listener")
	}
}

// TestNewServerFailsOnMissingCertificate proves a missing pair is a clear error
// at construction rather than a listener that accepts nothing.
func TestNewServerFailsOnMissingCertificate(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg, err := settings.New(st)
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}

	missing := CertPaths{
		CertFile: filepath.Join(t.TempDir(), "nope.crt"),
		KeyFile:  filepath.Join(t.TempDir(), "nope.key"),
	}
	if _, err := NewServer(context.Background(), reg, missing, http.NotFoundHandler()); err == nil {
		t.Fatal("NewServer accepted a missing certificate pair")
	}
}
