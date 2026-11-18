package tls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeACME is the least of an ACME server that lego's account registration
// needs: a directory, a nonce and newAccount. Orders are exercised against
// Pebble in the integration test.
type fakeACME struct {
	srv         *httptest.Server
	newAccounts atomic.Int32
}

func newFakeACME(t *testing.T) *fakeACME {
	t.Helper()
	f := &fakeACME{}
	mux := http.NewServeMux()
	mux.HandleFunc("/dir", func(w http.ResponseWriter, _ *http.Request) {
		base := f.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]string{
			"newNonce": base + "/nonce", "newAccount": base + "/acct", "newOrder": base + "/order",
			"revokeCert": base + "/revoke", "keyChange": base + "/keychange",
		})
	})
	mux.HandleFunc("/nonce", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Replay-Nonce", "nonce-1")
	})
	mux.HandleFunc("/acct", func(w http.ResponseWriter, _ *http.Request) {
		n := f.newAccounts.Add(1)
		w.Header().Set("Replay-Nonce", "nonce-2")
		w.Header().Set("Location", fmt.Sprintf("%s/acct/%d", f.srv.URL, n))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"valid"}`))
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeACME) dir() string { return f.srv.URL + "/dir" }

func newTestIssuer(t *testing.T, r *rig, o IssuerOptions) *Issuer {
	t.Helper()
	o.Store = r.st
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	if o.Directory == nil {
		o.Directory = func() string { return "http://127.0.0.1:1/dir" }
	}
	i, err := NewIssuer(o)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestAccountReusedPerDirectory(t *testing.T) {
	r := newRig(t)
	a, b := newFakeACME(t), newFakeACME(t)
	dir := a.dir()
	i := newTestIssuer(t, r, IssuerOptions{HTTPClient: a.srv.Client(), Directory: func() string { return dir }})
	ctx := context.Background()

	u1, err := i.account(ctx, a.dir(), "ops@example.com")
	if err != nil {
		t.Fatalf("first account: %v", err)
	}
	u2, err := i.account(ctx, a.dir(), "ops@example.com")
	if err != nil {
		t.Fatalf("second account: %v", err)
	}
	if got := a.newAccounts.Load(); got != 1 {
		t.Fatalf("newAccount calls on A = %d, want 1: the second order must reuse the account", got)
	}
	if !u1.key.Equal(u2.key) || u2.reg == nil || u2.reg.URI != u1.reg.URI {
		t.Fatal("reused account differs from the registered one")
	}

	// A fresh Issuer on the same store is a daemon restart: still no new account.
	i2 := newTestIssuer(t, r, IssuerOptions{HTTPClient: a.srv.Client(), Directory: func() string { return dir }})
	if _, err := i2.account(ctx, a.dir(), ""); err != nil {
		t.Fatal(err)
	}
	if got := a.newAccounts.Load(); got != 1 {
		t.Fatalf("newAccount calls after restart = %d, want 1", got)
	}

	// Another directory is another CA: its own account and key.
	u3, err := i.account(ctx, b.dir(), "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if b.newAccounts.Load() != 1 || a.newAccounts.Load() != 1 {
		t.Fatalf("A=%d B=%d, want 1 and 1", a.newAccounts.Load(), b.newAccounts.Load())
	}
	if u3.key.Equal(u1.key) {
		t.Fatal("two directories share an account key")
	}
	for _, d := range []string{a.dir(), b.dir()} {
		got, err := r.st.GetAccount(ctx, d)
		if err != nil || len(got.Registration) == 0 {
			t.Fatalf("account for %s not stored with its registration: %v", d, err)
		}
	}
}

func TestAccountKeyIsECDSAP256(t *testing.T) {
	r := newRig(t)
	a := newFakeACME(t)
	i := newTestIssuer(t, r, IssuerOptions{HTTPClient: a.srv.Client()})
	u, err := i.account(context.Background(), a.dir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if u.key.Curve.Params().Name != "P-256" {
		t.Fatalf("account key curve = %s", u.key.Curve.Params().Name)
	}
}

func TestDirectoryReadLive(t *testing.T) {
	r := newRig(t)
	a := newFakeACME(t)
	cur := ""
	i := newTestIssuer(t, r, IssuerOptions{HTTPClient: a.srv.Client(), Directory: func() string { return cur }})
	// An empty directory is a typed error, and the setting is consulted per order.
	_, err := i.IssueDNS01(context.Background(), "example.com", []string{"example.com"}, "cf", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing provider: %v", err)
	}
	if err := r.st.PutProvider(context.Background(), Provider{Name: "cf", Kind: "memory", Credentials: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	i.providers = func(string, []byte) (DNSProvider, error) { return NewMemoryDNS(), nil }
	if _, err := i.IssueDNS01(context.Background(), "example.com", []string{"example.com"}, "cf", ""); !errors.Is(err, ErrNoDirectory) {
		t.Fatalf("empty directory: %v", err)
	}
	cur = a.dir()
	// The registration succeeds; the order then fails against the fake, which
	// has no newOrder: what matters is that the live value reached the account.
	_, _ = i.IssueDNS01(context.Background(), "example.com", []string{"example.com"}, "cf", "")
	if a.newAccounts.Load() != 1 {
		t.Fatal("the directory returned by the func was not used")
	}
}

func TestWildcardRejectedForHTTP01(t *testing.T) {
	r := newRig(t)
	i := newTestIssuer(t, r, IssuerOptions{})
	for _, names := range [][]string{{"*.example.com"}, {"example.com", "*.example.com"}} {
		_, err := i.IssueHTTP01(context.Background(), "example.com", names, "")
		if !errors.Is(err, ErrWildcardNeedsDNS) {
			t.Errorf("names %v: err = %v, want ErrWildcardNeedsDNS", names, err)
		}
	}
}

func TestCheckNames(t *testing.T) {
	got, err := checkNames("example.com", []string{"Example.com", "*.EXAMPLE.com", "example.com"}, true)
	if err != nil || len(got) != 2 || got[0] != "example.com" || got[1] != "*.example.com" {
		t.Fatalf("checkNames = %v, %v", got, err)
	}
	for _, bad := range [][]string{nil, {"a/b"}, {"*.*.example.com"}, {"exa mple.com"}, {"*."}} {
		if _, err := checkNames("example.com", bad, true); err == nil {
			t.Errorf("names %v accepted", bad)
		}
	}
	if _, err := checkNames("../etc", []string{"example.com"}, true); !errors.Is(err, ErrBadDomain) {
		t.Errorf("bad site domain: %v", err)
	}
}

func TestIssueCancelledContext(t *testing.T) {
	r := newRig(t)
	a := newFakeACME(t)
	i := newTestIssuer(t, r, IssuerOptions{HTTPClient: a.srv.Client(), Directory: func() string { return a.dir() }})
	if err := r.st.PutProvider(context.Background(), Provider{Name: "cf", Kind: "memory", Credentials: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	i.providers = func(string, []byte) (DNSProvider, error) { return NewMemoryDNS(), nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := i.IssueDNS01(ctx, "example.com", []string{"example.com"}, "cf", "")
	if err == nil || (!errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled")) {
		t.Fatalf("err = %v, want a cancellation", err)
	}
	if a.newAccounts.Load() != 0 {
		t.Fatal("a cancelled order still registered an account")
	}
}

// --- pre-flight ---

type probeRig struct {
	i      *Issuer
	webDir string
	dirs   atomic.Int32
}

// newProbeRig builds an issuer whose probe client dials dial instead of the
// network and whose Directory counts calls: a pre-flight failure must leave the
// count at zero, because the CA is contacted only after the directory is read.
func newProbeRig(t *testing.T, dial func(ctx context.Context, network, addr string) (net.Conn, error), timeout time.Duration) *probeRig {
	t.Helper()
	r := newRig(t)
	p := &probeRig{}
	client := newProbeClient()
	client.Transport.(*http.Transport).DialContext = dial
	if timeout > 0 {
		client.Timeout = timeout
	}
	p.i = newTestIssuer(t, r, IssuerOptions{
		ProbeClient: client,
		Directory:   func() string { p.dirs.Add(1); return "http://127.0.0.1:1/dir" },
	})
	p.webDir = filepath.FromSlash(ACMEWebroot(p.i.root))
	return p
}

func dialTo(srv *httptest.Server) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
}

func (p *probeRig) run(t *testing.T) error {
	t.Helper()
	_, err := p.i.IssueHTTP01(context.Background(), "example.com", []string{"example.com", "www.example.com"}, "")
	return err
}

func (p *probeRig) assertClean(t *testing.T) {
	t.Helper()
	if n := p.dirs.Load(); n != 0 {
		t.Errorf("directory read %d times: the CA would have been contacted", n)
	}
	entries, _ := os.ReadDir(filepath.Join(p.webDir, ".well-known", "acme-challenge"))
	if len(entries) != 0 {
		t.Errorf("probe files left behind: %v", entries)
	}
}

func requirePreflight(t *testing.T, err error, name, reasonPart string) *PreflightError {
	t.Helper()
	var pe *PreflightError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *PreflightError", err)
	}
	if pe.Name != name {
		t.Errorf("Name = %q, want %q", pe.Name, name)
	}
	if !strings.Contains(pe.Reason, reasonPart) {
		t.Errorf("Reason = %q, want it to contain %q", pe.Reason, reasonPart)
	}
	return pe
}

func TestPreflightWrongContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("welcome to another server"))
	}))
	defer srv.Close()
	p := newProbeRig(t, dialTo(srv), 0)
	pe := requirePreflight(t, p.run(t), "example.com", "different content")
	if !errors.Is(pe, ErrPreflightMismatch) {
		t.Error("wrong content should wrap ErrPreflightMismatch")
	}
	p.assertClean(t)
}

func TestPreflightNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	p := newProbeRig(t, dialTo(srv), 0)
	_ = requirePreflight(t, p.run(t), "example.com", "HTTP 404")
	p.assertClean(t)
}

func TestPreflightRedirectNotFollowed(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	}))
	defer srv.Close()
	p := newProbeRig(t, dialTo(srv), 0)
	_ = requirePreflight(t, p.run(t), "example.com", "redirects")
	if followed.Load() {
		t.Error("the probe followed a redirect")
	}
	p.assertClean(t)
}

func TestPreflightTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	p := newProbeRig(t, dialTo(srv), 50*time.Millisecond)
	_ = requirePreflight(t, p.run(t), "example.com", "timed out")
	p.assertClean(t)
}

func TestPreflightConnectionRefused(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	}
	p := newProbeRig(t, dial, 0)
	_ = requirePreflight(t, p.run(t), "example.com", "refused")
	p.assertClean(t)
}

func TestPreflightDNSDoesNotResolve(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "example.com", IsNotFound: true}
	}
	p := newProbeRig(t, dial, 0)
	_ = requirePreflight(t, p.run(t), "example.com", "does not resolve")
	p.assertClean(t)
}

func TestPreflightReportsTheFailingName(t *testing.T) {
	// example.com is served from the webroot, www fails: the error must name www.
	var webroot atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "www.") {
			http.NotFound(w, r)
			return
		}
		http.FileServer(http.Dir(webroot.Load().(string))).ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := newProbeRig(t, dialTo(srv), 0)
	webroot.Store(p.webDir)
	_ = requirePreflight(t, p.run(t), "www.example.com", "HTTP 404")
}

func TestPreflightPassesWhenTheWebrootIsServed(t *testing.T) {
	var webroot atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.Dir(webroot.Load().(string))).ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := newProbeRig(t, dialTo(srv), 0)
	webroot.Store(p.webDir)
	// The order proceeds past pre-flight and stops at the unreachable CA, which
	// proves pre-flight passed without needing a CA.
	err := p.run(t)
	var pe *PreflightError
	if errors.As(err, &pe) {
		t.Fatalf("pre-flight failed: %v", err)
	}
	if p.dirs.Load() == 0 {
		t.Fatalf("pre-flight passed but the order never started: %v", err)
	}
}

func TestPreflightCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	p := newProbeRig(t, dialTo(srv), 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.i.IssueHTTP01(ctx, "example.com", []string{"example.com"}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
