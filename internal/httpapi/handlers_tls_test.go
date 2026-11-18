package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/sites"
	tlspkg "github.com/portbyte-id/server-panel/internal/tls"
)

// siteCert returns a self-signed PEM pair for names.
func siteCert(t *testing.T, serial int64, names []string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemEncode("CERTIFICATE", der), pemEncode("EC PRIVATE KEY", keyDER)
}

func goodCert(t *testing.T, serial int64, names ...string) (string, string) {
	c, k := siteCert(t, serial, names, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	return string(c), string(k)
}

// testTLSState is cmd/panel's tlsState with the container-side paths the real
// sites.Manager requires; the files themselves live under the test's temp root.
type testTLSState struct{ st *tlspkg.Store }

func (s testTLSState) Lookup(ctx context.Context, domain string) (sites.TLSInfo, bool, error) {
	c, err := s.st.GetCert(ctx, domain)
	if errors.Is(err, tlspkg.ErrNotFound) {
		return sites.TLSInfo{}, false, nil
	}
	if err != nil {
		return sites.TLSInfo{}, false, err
	}
	certFile, keyFile := tlspkg.SitePaths("/www", domain)
	return sites.TLSInfo{CertFile: certFile, KeyFile: keyFile, RedirectHTTP: c.RedirectHTTP}, true, nil
}

// fakeIssuer writes a certificate the way tls.Issuer does: files, then the row.
type fakeIssuer struct {
	t     *testing.T
	st    *tlspkg.Store
	root  string
	err   error
	mu    sync.Mutex
	calls []string
	names [][]string
}

func (f *fakeIssuer) issue(ctx context.Context, domain string, names []string, source, provider string) (tlspkg.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, source+" "+domain+" "+provider)
	f.names = append(f.names, names)
	f.mu.Unlock()
	if f.err != nil {
		return tlspkg.Result{}, f.err
	}
	c, k := siteCert(f.t, 777, names, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	if err := tlspkg.WriteSiteFiles(f.root, domain, c, k); err != nil {
		return tlspkg.Result{}, err
	}
	redirect := false
	if cur, err := f.st.GetCert(ctx, domain); err == nil {
		redirect = cur.RedirectHTTP
	}
	row := tlspkg.Cert{
		Domain: domain, Source: source, Names: names, Provider: provider, RedirectHTTP: redirect,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour),
		Issuer: "Fake CA", Serial: "309", LastRenewalAt: time.Now(), LastRenewalResult: tlspkg.RenewalOK,
	}
	if err := f.st.PutCert(ctx, row); err != nil {
		return tlspkg.Result{}, err
	}
	return tlspkg.Result{Domain: domain, Source: source, Names: names, Issuer: "Fake CA", Serial: "309", NotAfter: row.NotAfter}, nil
}

func (f *fakeIssuer) IssueHTTP01(ctx context.Context, domain string, names []string, _ string) (tlspkg.Result, error) {
	return f.issue(ctx, domain, names, tlspkg.SourceHTTP01, "")
}

func (f *fakeIssuer) IssueDNS01(ctx context.Context, domain string, names []string, provider, _ string) (tlspkg.Result, error) {
	return f.issue(ctx, domain, names, tlspkg.SourceDNS01, provider)
}

type fakeRenewer struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeRenewer) RenewNow(_ context.Context, domain string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, domain)
	return f.err
}

type tlsHarness struct {
	*nginxHarness
	td      *TLSDeps
	cs      *tlspkg.Store
	root    string
	issuer  *fakeIssuer
	renewer *fakeRenewer
}

func newTLSHarness(t *testing.T) *tlsHarness {
	t.Helper()
	nh := newNginxHarness(t)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, secretbox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := tlspkg.NewStore(tlspkg.Options{DB: nh.store, Box: box})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.ToSlash(t.TempDir())

	mgr, err := sites.New(sites.Options{
		Store: nh.store, Applier: nh.applier, Service: nh.svc, Paths: nginx.Paths{Root: "/www"},
		FS: nh.fs, TLS: testTLSState{cs},
	})
	if err != nil {
		t.Fatal(err)
	}
	nh.deps.Nginx.Sites = mgr

	h := &tlsHarness{nginxHarness: nh, cs: cs, root: root}
	h.issuer = &fakeIssuer{t: t, st: cs, root: root}
	h.renewer = &fakeRenewer{}
	h.td = &TLSDeps{Store: cs, Issuer: h.issuer, Renewer: h.renewer, Sites: mgr, Root: root}
	nh.deps.TLS = h.td
	if nh.handler, err = New(nh.deps); err != nil {
		t.Fatal(err)
	}
	return h
}

// withPanel starts a real listener and hands its reloader to the routes.
func (h *tlsHarness) withPanel() *Listener {
	h.t.Helper()
	l := newTestListener(h.t, 50*time.Millisecond)
	h.td.PanelCerts = func() *CertReloader { return l.Certs() }
	return l
}

// tlsBlock is the text inside the TLS block of a site's file ("" when the site
// file or the block is missing). A site keeps TLS there, not in a tls.conf.
func (h *tlsHarness) tlsBlock(domain string) string {
	var in []string
	open := false
	for _, l := range strings.Split(string(h.fs.files["/www/panel/nginx/sites/"+domain+".conf"]), "\n") {
		switch tl := strings.TrimSpace(l); {
		case !open && tl == "# >>> pb:tls":
			open = true
		case open && tl == "# <<< pb:tls":
			return strings.Join(in, "\n")
		case open:
			in = append(in, l)
		}
	}
	return ""
}

// tlsConf is the legacy include file, which a block site must never grow.
func (h *tlsHarness) tlsConf(domain string) string {
	return string(h.fs.files["/www/panel/nginx/includes/"+domain+"/tls.conf"])
}

func (h *tlsHarness) waitJob(out map[string]any) jobs.Job {
	h.t.Helper()
	id, _ := out["job_id"].(string)
	if id == "" {
		h.t.Fatalf("response has no job_id: %v", out)
	}
	done, err := h.deps.Jobs.Wait(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return done
}

func (h *tlsHarness) upload(domain string, serial int64, names ...string) map[string]any {
	h.t.Helper()
	c, k := goodCert(h.t, serial, names...)
	code, out := h.call(http.MethodPost, "/api/sites/"+domain+"/tls/upload", map[string]any{"certificate": c, "private_key": k})
	if code != http.StatusOK {
		h.t.Fatalf("upload: %d %v", code, out)
	}
	return out
}

func TestTLSRoutesUnavailableWithoutDeps(t *testing.T) {
	h := newNginxHarness(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/sites/a.test/tls"},
		{http.MethodPost, "/api/sites/a.test/tls/upload"},
		{http.MethodPost, "/api/sites/a.test/tls/issue"},
		{http.MethodPost, "/api/sites/a.test/tls/renew"},
		{http.MethodPut, "/api/sites/a.test/tls/redirect"},
		{http.MethodDelete, "/api/sites/a.test/tls"},
		{http.MethodGet, "/api/tls/certs"},
		{http.MethodGet, "/api/tls/providers"},
		{http.MethodPut, "/api/tls/providers/cf"},
		{http.MethodDelete, "/api/tls/providers/cf"},
		{http.MethodGet, "/api/panel/cert"},
		{http.MethodPost, "/api/panel/cert/upload"},
		{http.MethodPost, "/api/panel/cert/issue"},
	} {
		if rec := h.do(c.method, c.path, h.s.cookie, h.s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestTLSRoutesNeedLogin(t *testing.T) {
	h := newTLSHarness(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/sites/a.test/tls"},
		{http.MethodGet, "/api/tls/certs"},
		{http.MethodGet, "/api/tls/providers"},
		{http.MethodGet, "/api/panel/cert"},
	} {
		if rec := h.do(c.method, c.path, "", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, rec.Code)
		}
	}
	// A state-changing request without the CSRF token is refused before the handler.
	rec := h.do(http.MethodPost, "/api/panel/cert/upload", h.s.cookie, "", map[string]string{})
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF = %d, want 403", rec.Code)
	}
}

func TestTLSSiteRoutesWithoutNginxAnd404ForUnknownSite(t *testing.T) {
	h := newTLSHarness(t)
	if code, _ := h.call(http.MethodGet, "/api/sites/nope.test/tls", nil); code != http.StatusNotFound {
		t.Errorf("unknown site = %d, want 404", code)
	}
	h.td.Sites = nil
	if code, _ := h.call(http.MethodGet, "/api/sites/a.test/tls", nil); code != http.StatusServiceUnavailable {
		t.Errorf("without sites = %d, want 503", code)
	}
	// Providers do not need nginx.
	if code, _ := h.call(http.MethodGet, "/api/tls/providers", nil); code != http.StatusOK {
		t.Errorf("providers without sites = %d, want 200", code)
	}
}

func TestUploadRejectsMismatchWithAMessage(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	// A certificate for another name.
	c, k := goodCert(t, 1, "other.test")
	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/upload", map[string]any{"certificate": c, "private_key": k})
	if code != http.StatusBadRequest || !strings.Contains(out["detail"].(string), "does not cover a.test") {
		t.Errorf("domain mismatch: %d %v", code, out)
	}
	// A key that is not the certificate's.
	c1, _ := goodCert(t, 2, "a.test")
	_, k2 := goodCert(t, 3, "a.test")
	code, out = h.call(http.MethodPost, "/api/sites/a.test/tls/upload", map[string]any{"certificate": c1, "private_key": k2})
	if code != http.StatusBadRequest || !strings.Contains(out["detail"].(string), "does not match") {
		t.Errorf("key mismatch: %d %v", code, out)
	}
	// Nothing was stored or written.
	if _, err := h.cs.GetCert(context.Background(), "a.test"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("a rejected upload left a row: %v", err)
	}
	if strings.Contains(h.tlsBlock("a.test"), "ssl_certificate") {
		t.Error("a rejected upload changed the tls block")
	}
	if code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/upload", map[string]any{"certificate": "", "private_key": k}); code != http.StatusBadRequest || out["field"] != "certificate" {
		t.Errorf("empty certificate: %d %v", code, out)
	}
}

func TestUploadStoresRendersAndWarns(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	// Expired: accepted with a warning, as the plan says.
	c, k := siteCert(t, 5, []string{"a.test"}, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/upload", map[string]any{"certificate": string(c), "private_key": string(k)})
	if code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, out)
	}
	warns := out["warnings"].([]any)
	if len(warns) == 0 || warns[0].(map[string]any)["code"] != "expired" || warns[0].(map[string]any)["message"] == "" {
		t.Errorf("warnings = %v", warns)
	}
	cert := out["cert"].(map[string]any)
	if cert["source"] != "upload" || cert["domain"] != "a.test" || cert["last_renewal_at"] != nil || cert["provider"] != "" {
		t.Errorf("cert = %v", cert)
	}
	if cert["days_left"].(float64) >= 0 {
		t.Errorf("days_left = %v for an expired certificate", cert["days_left"])
	}
	if !strings.Contains(h.tlsBlock("a.test"), "ssl_certificate /www/panel/ssl/sites/a.test/fullchain.pem;") {
		t.Errorf("tls block:\n%s", h.tlsBlock("a.test"))
	}
	if h.tlsConf("a.test") != "" {
		t.Error("a tls.conf was written for a site that keeps TLS in a block")
	}
	certFile, keyFile := tlspkg.SitePaths(h.root, "a.test")
	for _, f := range []string{certFile, keyFile} {
		if _, err := os.Stat(filepath.FromSlash(f)); err != nil {
			t.Errorf("file not written: %v", err)
		}
	}
	if strings.Contains(string(mustJSON(t, out)), "PRIVATE KEY") {
		t.Error("the response carries key material")
	}

	// GET shows the same cert; an unknown-certificate site shows null.
	code, out = h.call(http.MethodGet, "/api/sites/a.test/tls", nil)
	if code != 200 || out["cert"].(map[string]any)["serial"] != "5" {
		t.Errorf("get: %d %v", code, out)
	}
	h.mustCreate(map[string]any{"domain": "b.test", "kind": "static"})
	code, out = h.call(http.MethodGet, "/api/sites/b.test/tls", nil)
	if code != 200 {
		t.Fatalf("get: %d", code)
	}
	if v, present := out["cert"]; !present || v != nil {
		t.Errorf("site without a certificate: %v", out)
	}
	if code, out = h.call(http.MethodGet, "/api/tls/certs", nil); code != 200 || len(out["certs"].([]any)) != 1 {
		t.Errorf("certs: %d %v", code, out)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUploadRollsBackWhenNginxRejects(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	// No previous certificate: everything is removed again.
	h.applier.err = rejected("nginx: [emerg] cannot load certificate")
	c, k := goodCert(t, 11, "a.test")
	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/upload", map[string]any{"certificate": c, "private_key": k})
	if code != http.StatusUnprocessableEntity || out["error"] != errConfigRejected || !strings.Contains(out["output"].(string), "[emerg]") {
		t.Fatalf("rejected upload: %d %v", code, out)
	}
	if _, err := h.cs.GetCert(context.Background(), "a.test"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("row kept after a rejected upload: %v", err)
	}
	certFile, _ := tlspkg.SitePaths(h.root, "a.test")
	if _, err := os.Stat(filepath.FromSlash(certFile)); !os.IsNotExist(err) {
		t.Errorf("files kept after a rejected upload: %v", err)
	}

	// With a previous certificate: the previous one is back, row and files.
	h.applier.err = nil
	h.upload("a.test", 12, "a.test")
	before, _ := os.ReadFile(filepath.FromSlash(certFile))
	h.applier.err = rejected("nginx: [emerg] boom")
	c2, k2 := goodCert(t, 13, "a.test")
	if code, _ := h.call(http.MethodPost, "/api/sites/a.test/tls/upload", map[string]any{"certificate": c2, "private_key": k2}); code != http.StatusUnprocessableEntity {
		t.Fatalf("second rejected upload = %d", code)
	}
	row, err := h.cs.GetCert(context.Background(), "a.test")
	if err != nil || row.Serial != "c" { // 12 in hex
		t.Errorf("row after rollback = %+v, %v", row, err)
	}
	after, _ := os.ReadFile(filepath.FromSlash(certFile))
	if !bytes.Equal(before, after) {
		t.Error("the previous certificate file was not restored")
	}
}

func TestUploadOnADetachedSiteIsRefusedAndRolledBack(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	// Detach: take the tls block's marker lines out of the site file.
	site := h.fs.files["/www/panel/nginx/sites/a.test.conf"]
	var kept []string
	for _, l := range strings.Split(string(site), "\n") {
		if tl := strings.TrimSpace(l); tl != "# >>> pb:tls" && tl != "# <<< pb:tls" {
			kept = append(kept, l)
		}
	}
	h.fs.files["/www/panel/nginx/sites/a.test.conf"] = []byte(strings.Join(kept, "\n"))

	c, k := goodCert(t, 1, "a.test")
	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/upload", map[string]any{"certificate": c, "private_key": k})
	if code != http.StatusConflict || out["error"] != errDetached {
		t.Fatalf("detached upload: %d %v", code, out)
	}
	if _, err := h.cs.GetCert(context.Background(), "a.test"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("a certificate that is not live was kept: %v", err)
	}
}

func TestIssueValidation(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "aliases": []string{"www.a.test"}, "kind": "static"})

	for _, c := range []struct {
		name  string
		body  map[string]any
		field string
		text  string
	}{
		{"wildcard with http01", map[string]any{"method": "http01", "names": []string{"*.a.test", "a.test"}}, "names", "DNS-01"},
		{"unknown method", map[string]any{"method": "tls-alpn"}, "method", "http01"},
		{"no method", map[string]any{}, "method", "http01"},
		{"unknown provider", map[string]any{"method": "dns01", "provider": "ghost"}, "provider", "no DNS provider is named ghost"},
		{"dns01 without provider", map[string]any{"method": "dns01"}, "provider", "choose"},
		{"names that miss the domain", map[string]any{"method": "http01", "names": []string{"other.test"}}, "names", "covers a.test"},
		{"bad name", map[string]any{"method": "http01", "names": []string{"a.test", "bad name"}}, "names", "not a valid"},
		{"bad email", map[string]any{"method": "http01", "email": "nobody"}, "email", "email"},
	} {
		code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/issue", c.body)
		if code != http.StatusBadRequest || out["field"] != c.field || !strings.Contains(out["detail"].(string), c.text) {
			t.Errorf("%s: %d %v", c.name, code, out)
		}
	}
	if len(h.issuer.calls) != 0 {
		t.Errorf("a rejected request reached the issuer: %v", h.issuer.calls)
	}
}

func TestIssueHTTP01RunsAsAJobAndRefreshesNginx(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "aliases": []string{"www.a.test"}, "kind": "static"})

	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/issue", map[string]any{"method": "http01"})
	if code != http.StatusAccepted {
		t.Fatalf("issue: %d %v", code, out)
	}
	done := h.waitJob(out)
	if done.State != jobs.StateSucceeded {
		t.Fatalf("job = %+v", done)
	}
	if got := h.issuer.names[0]; len(got) != 2 || got[0] != "a.test" || got[1] != "www.a.test" {
		t.Errorf("default names = %v", got)
	}
	if h.issuer.calls[0] != "http01 a.test " {
		t.Errorf("issuer calls = %v", h.issuer.calls)
	}
	if !strings.Contains(h.tlsBlock("a.test"), "listen 443 ssl") {
		t.Errorf("nginx was not refreshed:\n%s", h.tlsBlock("a.test"))
	}
	lines, err := h.deps.Jobs.Replay(context.Background(), done.ID, 0)
	if err != nil || len(lines) < 3 {
		t.Fatalf("job output = %v, %v", lines, err)
	}
	var text []string
	for _, l := range lines {
		text = append(text, l.Text)
	}
	if joined := strings.Join(text, "\n"); !strings.Contains(joined, "Fake CA") || !strings.Contains(joined, "live") {
		t.Errorf("job output:\n%s", joined)
	}

	// An order for the same domain while one runs is a 409 (simulated).
	h.td.begin("a.test")
	if code, _ := h.call(http.MethodPost, "/api/sites/a.test/tls/issue", map[string]any{"method": "http01"}); code != http.StatusConflict {
		t.Errorf("concurrent issue = %d, want 409", code)
	}
	h.td.end("a.test")
}

func TestIssueDNS01WithAProviderAndAWildcard(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	if code, out := h.call(http.MethodPut, "/api/tls/providers/cf", map[string]any{"kind": "cloudflare", "credentials": map[string]string{"api_token": "tok-1"}}); code != 200 {
		t.Fatalf("provider: %d %v", code, out)
	}
	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/issue",
		map[string]any{"method": "dns01", "provider": "cf", "names": []string{"a.test", "*.a.test"}})
	if code != http.StatusAccepted {
		t.Fatalf("issue: %d %v", code, out)
	}
	if done := h.waitJob(out); done.State != jobs.StateSucceeded {
		t.Fatalf("job = %+v", done)
	}
	if h.issuer.calls[0] != "dns01 a.test cf" || len(h.issuer.names[0]) != 2 {
		t.Errorf("issuer = %v %v", h.issuer.calls, h.issuer.names)
	}
	_, out = h.call(http.MethodGet, "/api/sites/a.test/tls", nil)
	if out["cert"].(map[string]any)["provider"] != "cf" {
		t.Errorf("cert = %v", out["cert"])
	}
}

func TestIssueFailureSurfacesAsTheJobError(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	h.issuer.err = errors.New("tls: pre-flight: a.test did not answer on port 80")
	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/issue", map[string]any{"method": "http01"})
	if code != http.StatusAccepted {
		t.Fatalf("issue: %d %v", code, out)
	}
	done := h.waitJob(out)
	if done.State != jobs.StateFailed || !strings.Contains(done.Err, "did not answer on port 80") || strings.HasPrefix(done.Err, "tls:") {
		t.Errorf("job = %+v", done)
	}
	// The slot is free again.
	if !h.td.begin("a.test") {
		t.Error("the domain stayed busy after a failed job")
	}
}

func TestIssueThatIsNotLiveSaysSoInTheJob(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	h.applier.err = rejected("nginx: [emerg] no")
	_, out := h.call(http.MethodPost, "/api/sites/a.test/tls/issue", map[string]any{"method": "http01"})
	done := h.waitJob(out)
	if done.State != jobs.StateFailed || !strings.Contains(done.Err, "issued but is not live") || !strings.Contains(done.Err, "nginx rejected") {
		t.Errorf("job = %+v", done)
	}
}

func TestRenew(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	if code, _ := h.call(http.MethodPost, "/api/sites/a.test/tls/renew", nil); code != http.StatusNotFound {
		t.Errorf("no certificate = %d, want 404", code)
	}
	h.upload("a.test", 1, "a.test")
	code, out := h.call(http.MethodPost, "/api/sites/a.test/tls/renew", nil)
	if code != http.StatusConflict || !strings.Contains(out["detail"].(string), "uploaded") {
		t.Errorf("renew of an upload: %d %v", code, out)
	}

	if _, out := h.call(http.MethodPost, "/api/sites/a.test/tls/issue", map[string]any{"method": "http01"}); h.waitJob(out).State != jobs.StateSucceeded {
		t.Fatal("issue failed")
	}
	code, out = h.call(http.MethodPost, "/api/sites/a.test/tls/renew", nil)
	if code != http.StatusAccepted {
		t.Fatalf("renew: %d %v", code, out)
	}
	if done := h.waitJob(out); done.State != jobs.StateSucceeded || len(h.renewer.calls) != 1 || h.renewer.calls[0] != "a.test" {
		t.Errorf("job = %+v calls = %v", done, h.renewer.calls)
	}

	h.renewer.err = errors.New("the CA said no")
	_, out = h.call(http.MethodPost, "/api/sites/a.test/tls/renew", nil)
	if done := h.waitJob(out); done.State != jobs.StateFailed || !strings.Contains(done.Err, "the CA said no") {
		t.Errorf("failed renew job = %+v", done)
	}
}

func TestRedirectToggle(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	if code, _ := h.call(http.MethodPut, "/api/sites/a.test/tls/redirect", map[string]any{"redirect_http": true}); code != http.StatusNotFound {
		t.Errorf("redirect without a certificate = %d, want 404", code)
	}
	h.upload("a.test", 1, "a.test")
	if strings.Contains(h.tlsBlock("a.test"), "return 301") {
		t.Fatal("redirect on by default")
	}
	code, out := h.call(http.MethodPut, "/api/sites/a.test/tls/redirect", map[string]any{"redirect_http": true})
	if code != 200 || out["redirect_http"] != true || out["domain"] != "a.test" {
		t.Fatalf("redirect on: %d %v", code, out)
	}
	if !strings.Contains(h.tlsBlock("a.test"), "return 301 https://$host$request_uri;") {
		t.Errorf("tls.conf:\n%s", h.tlsBlock("a.test"))
	}
	if code, out := h.call(http.MethodPut, "/api/sites/a.test/tls/redirect", map[string]any{}); code != http.StatusBadRequest || out["field"] != "redirect_http" {
		t.Errorf("missing field: %d %v", code, out)
	}

	// Rejected by nginx: the stored flag goes back.
	h.applier.err = rejected("nginx: [emerg] no")
	if code, _ := h.call(http.MethodPut, "/api/sites/a.test/tls/redirect", map[string]any{"redirect_http": false}); code != http.StatusUnprocessableEntity {
		t.Errorf("rejected = %d", code)
	}
	h.applier.err = nil
	if row, _ := h.cs.GetCert(context.Background(), "a.test"); !row.RedirectHTTP {
		t.Error("the redirect flag changed although nginx rejected the change")
	}
	if code, out := h.call(http.MethodPut, "/api/sites/a.test/tls/redirect", map[string]any{"redirect_http": false}); code != 200 || out["redirect_http"] != false {
		t.Errorf("redirect off: %d %v", code, out)
	}
	if strings.Contains(h.tlsBlock("a.test"), "return 301") {
		t.Error("redirect still rendered")
	}
}

func TestDeleteRevertsTheSiteToPlainHTTP(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	if code, _ := h.call(http.MethodDelete, "/api/sites/a.test/tls", nil); code != http.StatusNotFound {
		t.Errorf("delete without a certificate = %d, want 404", code)
	}
	h.upload("a.test", 1, "a.test")
	certFile, _ := tlspkg.SitePaths(h.root, "a.test")

	// nginx rejects: the row is back and the files were never touched.
	h.applier.err = rejected("nginx: [emerg] no")
	if code, _ := h.call(http.MethodDelete, "/api/sites/a.test/tls", nil); code != http.StatusUnprocessableEntity {
		t.Errorf("rejected delete = %d", code)
	}
	h.applier.err = nil
	if _, err := h.cs.GetCert(context.Background(), "a.test"); err != nil {
		t.Errorf("row lost after a rejected delete: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(certFile)); err != nil {
		t.Errorf("files lost after a rejected delete: %v", err)
	}

	if rec := h.do(http.MethodDelete, "/api/sites/a.test/tls", h.s.cookie, h.s.csrf, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(h.tlsBlock("a.test"), "ssl_certificate") || strings.Contains(h.tlsBlock("a.test"), "listen") {
		t.Errorf("tls.conf still has directives:\n%s", h.tlsBlock("a.test"))
	}
	if _, err := h.cs.GetCert(context.Background(), "a.test"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("row kept: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(certFile)); !os.IsNotExist(err) {
		t.Errorf("files kept: %v", err)
	}
}

func TestSiteDeleteRemovesItsCertificate(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	h.mustCreate(map[string]any{"domain": "b.test", "kind": "static"})
	h.upload("a.test", 1, "a.test")
	h.upload("b.test", 2, "b.test")
	aCert, _ := tlspkg.SitePaths(h.root, "a.test")
	bCert, _ := tlspkg.SitePaths(h.root, "b.test")

	code, out := h.call(http.MethodDelete, "/api/sites/a.test?confirm=a.test", nil)
	if code != http.StatusOK {
		t.Fatalf("delete site: %d %v", code, out)
	}
	if w := out["warnings"].([]any); len(w) != 0 {
		t.Errorf("warnings = %v", w)
	}
	if _, err := h.cs.GetCert(context.Background(), "a.test"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("deleted site's row kept: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(aCert)); !os.IsNotExist(err) {
		t.Errorf("deleted site's files kept: %v", err)
	}
	// The other site is untouched.
	if _, err := h.cs.GetCert(context.Background(), "b.test"); err != nil {
		t.Errorf("another site's row went: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(bCert)); err != nil {
		t.Errorf("another site's files went: %v", err)
	}
}

func TestSiteDeleteKeepsTheCertificateThatFeedsThePanel(t *testing.T) {
	h := newTLSHarness(t)
	h.mustCreate(map[string]any{"domain": "p.test", "kind": "static"})
	h.upload("p.test", 1, "p.test")
	if err := h.td.setPanelDomain("p.test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.call(http.MethodDelete, "/api/sites/p.test/tls", nil); code != http.StatusConflict {
		t.Errorf("deleting the panel's certificate from the site = %d, want 409", code)
	}
	if code, _ := h.call(http.MethodDelete, "/api/sites/p.test?confirm=p.test", nil); code != http.StatusOK {
		t.Fatalf("delete site = %d", code)
	}
	if _, err := h.cs.GetCert(context.Background(), "p.test"); err != nil {
		t.Errorf("the panel's certificate row went with the site: %v", err)
	}
}

func TestProvidersNeverEchoCredentials(t *testing.T) {
	h := newTLSHarness(t)
	const token = "cf-super-secret-token-9d7"
	code, out := h.call(http.MethodPut, "/api/tls/providers/cf", map[string]any{"kind": "cloudflare", "credentials": map[string]string{"api_token": token}})
	if code != 200 || out["name"] != "cf" || out["kind"] != "cloudflare" {
		t.Fatalf("put: %d %v", code, out)
	}
	rec := h.do(http.MethodGet, "/api/tls/providers", h.s.cookie, h.s.csrf, nil)
	var list struct {
		Providers []map[string]any `json:"providers"`
		Kinds     []string         `json:"kinds"`
	}
	h.decode(rec, &list)
	if len(list.Providers) != 1 || list.Providers[0]["name"] != "cf" || len(list.Kinds) != 1 || list.Kinds[0] != "cloudflare" {
		t.Errorf("list = %+v", list)
	}
	for _, body := range []string{rec.Body.String(), mustString(t, out)} {
		if strings.Contains(body, token) {
			t.Errorf("a response carries the token: %s", body)
		}
	}
	// The audit trail has the request and not the secret.
	if len(h.audit.entries) == 0 {
		t.Fatal("no audit entry")
	}
	for _, e := range h.audit.entries {
		if strings.Contains(string(mustJSON(t, e)), token) {
			t.Errorf("audit entry carries the token: %+v", e)
		}
	}
	last := h.audit.entries[len(h.audit.entries)-1]
	if last.Action != "PUT /api/tls/providers/cf" || last.Target != "dns-provider:cf" {
		t.Errorf("audit entry = %+v", last)
	}
	// And so does the stored audit_log table.
	var n int
	if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ?`, "%"+token+"%").Scan(&n); err != nil || n != 0 {
		t.Errorf("audit_log rows with the token = %d, %v", n, err)
	}

	for _, c := range []struct {
		name string
		body map[string]any
		path string
	}{
		{"unknown kind", map[string]any{"kind": "route53", "credentials": map[string]string{"a": "b"}}, "cf2"},
		{"no credentials", map[string]any{"kind": "cloudflare"}, "cf2"},
		{"wrong shape", map[string]any{"kind": "cloudflare", "credentials": map[string]string{"global_key": "leaky-value-1"}}, "cf2"},
		{"bad name", map[string]any{"kind": "cloudflare", "credentials": map[string]string{"api_token": "t"}}, "Bad Name"},
	} {
		code, out := h.call(http.MethodPut, "/api/tls/providers/"+strings.ReplaceAll(c.path, " ", "%20"), c.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", c.name, code, out)
		}
		if strings.Contains(mustString(t, out), "leaky-value-1") {
			t.Errorf("%s: the error echoes a credential: %v", c.name, out)
		}
	}
}

func mustString(t *testing.T, v any) string { return string(mustJSON(t, v)) }

func TestProviderDelete(t *testing.T) {
	h := newTLSHarness(t)
	h.call(http.MethodPut, "/api/tls/providers/cf", map[string]any{"kind": "cloudflare", "credentials": map[string]string{"api_token": "t"}})
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	if _, out := h.call(http.MethodPost, "/api/sites/a.test/tls/issue", map[string]any{"method": "dns01", "provider": "cf"}); h.waitJob(out).State != jobs.StateSucceeded {
		t.Fatal("issue failed")
	}
	code, out := h.call(http.MethodDelete, "/api/tls/providers/cf", nil)
	if code != http.StatusConflict || !strings.Contains(out["detail"].(string), "a.test") {
		t.Errorf("delete in use: %d %v", code, out)
	}
	if code, _ := h.call(http.MethodDelete, "/api/sites/a.test/tls", nil); code != http.StatusNoContent {
		t.Fatalf("remove cert = %d", code)
	}
	if rec := h.do(http.MethodDelete, "/api/tls/providers/cf", h.s.cookie, h.s.csrf, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete unused = %d", rec.Code)
	}
	if code, _ := h.call(http.MethodDelete, "/api/tls/providers/cf", nil); code != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", code)
	}
}

func TestPanelCertUploadSwapsTheLiveCertificate(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	before := dialSerial(t, l.Port())

	code, out := h.call(http.MethodGet, "/api/panel/cert", nil)
	if code != 200 || out["previous_available"] != false || out["cert"].(map[string]any)["self_signed"] != true {
		t.Fatalf("get: %d %v", code, out)
	}

	c, k := goodCert(t, 4711, "panel.test")
	code, out = h.call(http.MethodPost, "/api/panel/cert/upload", map[string]any{"certificate": c, "private_key": k})
	if code != 200 || out["serial"] != "1267" { // 4711 in hex
		t.Fatalf("upload: %d %v", code, out)
	}
	if got := dialSerial(t, l.Port()); got != 4711 || got == before {
		t.Errorf("served serial = %d, want 4711 (was %d)", got, before)
	}
	code, out = h.call(http.MethodGet, "/api/panel/cert", nil)
	if code != 200 || out["previous_available"] != true || out["cert"].(map[string]any)["serial"] != "1267" {
		t.Errorf("get after: %d %v", code, out)
	}
	if strings.Contains(mustString(t, out), "PRIVATE KEY") {
		t.Error("the response carries key material")
	}

	// A bad pair is a 400 and the live certificate stays.
	_, k2 := goodCert(t, 1, "panel.test")
	code, out = h.call(http.MethodPost, "/api/panel/cert/upload", map[string]any{"certificate": c, "private_key": k2})
	if code != http.StatusBadRequest || strings.Contains(out["detail"].(string), "httpapi:") {
		t.Errorf("mismatched pair: %d %v", code, out)
	}
	if got := dialSerial(t, l.Port()); got != 4711 {
		t.Errorf("a rejected upload changed the served certificate to %d", got)
	}
	// An expired one too.
	ec, ek := siteCert(t, 2, []string{"panel.test"}, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	if code, _ := h.call(http.MethodPost, "/api/panel/cert/upload", map[string]any{"certificate": string(ec), "private_key": string(ek)}); code != http.StatusBadRequest {
		t.Errorf("expired = %d, want 400", code)
	}
}

func TestPanelCertRoutesWithoutAListener(t *testing.T) {
	h := newTLSHarness(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/panel/cert"},
		{http.MethodPost, "/api/panel/cert/upload"},
		{http.MethodPost, "/api/panel/cert/issue"},
	} {
		if rec := h.do(c.method, c.path, h.s.cookie, h.s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestPanelCertIssue(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}

	// HTTP-01 needs a managed site with that domain.
	code, out := h.call(http.MethodPost, "/api/panel/cert/issue", map[string]any{"hostname": "panel.test", "method": "http01"})
	if code != http.StatusConflict || !strings.Contains(out["detail"].(string), "create a site") || !strings.Contains(out["detail"].(string), "DNS-01") {
		t.Errorf("http01 without a site: %d %v", code, out)
	}
	if code, out := h.call(http.MethodPost, "/api/panel/cert/issue", map[string]any{"hostname": "not a host", "method": "http01"}); code != http.StatusBadRequest || out["field"] != "hostname" {
		t.Errorf("bad hostname: %d %v", code, out)
	}
	if code, out := h.call(http.MethodPost, "/api/panel/cert/issue", map[string]any{"hostname": "panel.test", "method": "dns01", "provider": "ghost"}); code != http.StatusBadRequest || out["field"] != "provider" {
		t.Errorf("unknown provider: %d %v", code, out)
	}

	// DNS-01 needs no site: issue, record the hostname, hot-swap.
	h.call(http.MethodPut, "/api/tls/providers/cf", map[string]any{"kind": "cloudflare", "credentials": map[string]string{"api_token": "t"}})
	code, out = h.call(http.MethodPost, "/api/panel/cert/issue", map[string]any{"hostname": "Panel.test", "method": "dns01", "provider": "cf"})
	if code != http.StatusAccepted {
		t.Fatalf("issue: %d %v", code, out)
	}
	if done := h.waitJob(out); done.State != jobs.StateSucceeded {
		t.Fatalf("job = %+v", done)
	}
	if got := dialSerial(t, l.Port()); got != 777 {
		t.Errorf("the panel serves serial %d, want the issued 777", got)
	}
	if d := h.td.PanelDomain(); d != "panel.test" {
		t.Errorf("panel domain marker = %q", d)
	}
	_, out = h.call(http.MethodGet, "/api/panel/cert", nil)
	if out["managed_domain"] != "panel.test" {
		t.Errorf("managed_domain = %v", out["managed_domain"])
	}

	// A renewal's Apply now swaps the listener again; uploading ends the management.
	c, k := goodCert(t, 4711, "panel.test")
	if code, _ := h.call(http.MethodPost, "/api/panel/cert/upload", map[string]any{"certificate": c, "private_key": k}); code != 200 {
		t.Fatalf("upload = %d", code)
	}
	if d := h.td.PanelDomain(); d != "" {
		t.Errorf("marker survived an upload: %q", d)
	}
	if _, err := h.cs.GetCert(context.Background(), "panel.test"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("the replaced issued certificate is still renewed: %v", err)
	}
}

func TestPanelCertIssueHTTP01WithASite(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	h.mustCreate(map[string]any{"domain": "panel.test", "kind": "static"})
	code, out := h.call(http.MethodPost, "/api/panel/cert/issue", map[string]any{"hostname": "panel.test", "method": "http01"})
	if code != http.StatusAccepted {
		t.Fatalf("issue: %d %v", code, out)
	}
	if done := h.waitJob(out); done.State != jobs.StateSucceeded {
		t.Fatalf("job = %+v", done)
	}
	if got := dialSerial(t, l.Port()); got != 777 {
		t.Errorf("served serial = %d", got)
	}
	// The site shares the row, so it is HTTPS as well.
	if !strings.Contains(h.tlsBlock("panel.test"), "listen 443 ssl") {
		t.Errorf("site tls.conf:\n%s", h.tlsBlock("panel.test"))
	}
	// Uploading a panel certificate keeps the row: the site still uses it.
	c, k := goodCert(t, 5, "panel.test")
	h.call(http.MethodPost, "/api/panel/cert/upload", map[string]any{"certificate": c, "private_key": k})
	if _, err := h.cs.GetCert(context.Background(), "panel.test"); err != nil {
		t.Errorf("the site's certificate went with the panel's: %v", err)
	}
}

func TestApplyDispatchesToTheSiteAndTheListener(t *testing.T) {
	h := newTLSHarness(t)
	l := h.withPanel()
	if err := l.Start(0); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A site only: nginx refreshed, listener untouched.
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	before := dialSerialPort(t, l)
	if _, err := h.issuer.IssueHTTP01(ctx, "a.test", []string{"a.test"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.td.Apply(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.tlsBlock("a.test"), "listen 443 ssl") {
		t.Error("site not refreshed")
	}
	if dialSerialPort(t, l) != before {
		t.Error("a site's apply swapped the panel certificate")
	}

	// The panel domain with no site: listener swapped, no error.
	if _, err := h.issuer.IssueDNS01(ctx, "panel.test", []string{"panel.test"}, "cf", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.td.Apply(ctx, "panel.test"); err != nil || dialSerialPort(t, l) != before {
		t.Fatalf("unmanaged domain: err=%v serial=%d", err, dialSerialPort(t, l))
	}
	if err := h.td.setPanelDomain("panel.test"); err != nil {
		t.Fatal(err)
	}
	if err := h.td.Apply(ctx, "panel.test"); err != nil {
		t.Fatal(err)
	}
	if got := dialSerialPort(t, l); got != 777 {
		t.Errorf("served serial = %d, want 777", got)
	}

	// Both at once.
	h.mustCreate(map[string]any{"domain": "p2.test", "kind": "static"})
	if _, err := h.issuer.IssueHTTP01(ctx, "p2.test", []string{"p2.test"}, ""); err != nil {
		t.Fatal(err)
	}
	_ = h.td.setPanelDomain("p2.test")
	if err := h.td.Apply(ctx, "p2.test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.tlsBlock("p2.test"), "listen 443 ssl") {
		t.Error("site not refreshed when it is also the panel's domain")
	}

	// A failing nginx is reported, and the listener swap is not skipped.
	h.applier.err = rejected("nginx: [emerg] no")
	err := h.td.Apply(ctx, "p2.test")
	if err == nil {
		t.Fatal("Apply hid an nginx rejection")
	}
	if text, out := applyFailureText(err); !strings.Contains(text, "nginx rejected") || !strings.Contains(out, "[emerg]") {
		t.Errorf("applyFailureText = %q, %q", text, out)
	}
}

func dialSerialPort(t *testing.T, l *Listener) int64 {
	t.Helper()
	return dialSerial(t, l.Port())
}

func TestCoversName(t *testing.T) {
	for _, c := range []struct {
		names  []string
		domain string
		want   bool
	}{
		{[]string{"a.test"}, "a.test", true},
		{[]string{"*.a.test"}, "www.a.test", true},
		{[]string{"*.a.test"}, "a.test", false},
		{[]string{"*.a.test"}, "x.y.a.test", false},
		{[]string{"b.test"}, "a.test", false},
	} {
		if got := coversName(c.names, c.domain); got != c.want {
			t.Errorf("coversName(%v, %q) = %v", c.names, c.domain, got)
		}
	}
}
