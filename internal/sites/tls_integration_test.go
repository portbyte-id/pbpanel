//go:build integration && linux

package sites

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/store"
)

// TestRealNginxTLS proves against a real nginx that a site with a certificate
// passes `nginx -t`, serves HTTPS, answers every plain-HTTP path except the ACME
// challenge with a 301, and serves a token written the way an ACME client's
// webroot provider writes it. It needs ports 80 and 443 free on the host.
func TestRealNginxTLS(t *testing.T) {
	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	if _, err := eng.Ping(bg); err != nil {
		t.Skipf("no Docker Engine: %v", err)
	}
	_ = eng.ContainerRemove(bg, nginx.ContainerName, true)
	t.Cleanup(func() { _ = eng.ContainerRemove(bg, nginx.ContainerName, true) })

	root, err := os.MkdirTemp("", "panel-tls-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	paths := nginx.Paths{Root: root}
	for _, d := range []string{
		"", "panel", "panel/nginx", "panel/nginx/sites", "panel/nginx/includes", "panel/logs", "panel/logs/sites", "panel/history",
		"services", "services/sites", "panel/ssl", "panel/ssl/sites", "panel/ssl/acme-webroot",
	} {
		p := filepath.Join(root, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	certDir := paths.SiteCertDir("a.test")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSelfSigned(t, certDir, "a.test")

	st, err := store.Open(filepath.Join(root, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ed, err := confedit.New(confedit.Options{Roots: []string{paths.Dir()}, HistoryDir: filepath.Join(root, "panel", "history"), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := nginx.NewImages(eng, paths, images.NewCatalog(st), nil)
	if err != nil {
		t.Fatal(err)
	}
	live := nginx.NewService(eng, paths, imgs.ImageFunc())
	state := &fakeTLS{certs: map[string]TLSInfo{}}
	mgr, err := New(Options{Store: st, Applier: ed, Service: live, Paths: paths, FS: chownless{}, TLS: state})
	if err != nil {
		t.Fatal(err)
	}
	rec := &Reconciler{
		Sites: mgr, Global: nginx.NewGlobal(st, ed, live, paths), Recover: ed.Recover,
		Boot: nginx.NewValidateOnly(eng, paths, imgs.ImageFunc()), Live: live, Runtime: nginx.NewRuntime(eng, paths, imgs.ImageFunc()),
		BuildImage: func(ctx context.Context) error {
			_, err := imgs.Build(ctx, func(l string) { t.Log(l) })
			return err
		},
	}
	if s := rec.Run(bg); !s.OK {
		t.Fatalf("reconcile: %+v", s)
	}
	if _, err := mgr.Create(bg, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatalf("create: %v", err)
	}

	state.certs["a.test"] = TLSInfo{CertFile: filepath.Join(certDir, "fullchain.pem"), KeyFile: filepath.Join(certDir, "privkey.pem"), RedirectHTTP: true}
	if _, err := mgr.RefreshTLS(bg, "a.test"); err != nil {
		t.Fatalf("refresh (runs nginx -t and reloads): %v", err)
	}

	// A token the way lego's webroot provider writes it.
	tokenDir := filepath.Join(paths.ACMEWebroot(), ".well-known", "acme-challenge")
	if err := os.MkdirAll(tokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, "tok"), []byte("tok.thumb"), 0o644); err != nil {
		t.Fatal(err)
	}

	noFollow := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	fetch := func(c *http.Client, url, host string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Host = host
		resp, err := c.Do(req)
		if err != nil {
			return 0, err.Error(), ""
		}
		defer func() { _ = resp.Body.Close() }()
		var sb strings.Builder
		buf := make([]byte, 512)
		for n, _ := resp.Body.Read(buf); n > 0; n, _ = resp.Body.Read(buf) {
			sb.Write(buf[:n])
		}
		return resp.StatusCode, sb.String(), resp.Header.Get("Location")
	}

	waitFor(t, "the redirect", func() bool {
		code, _, _ := fetch(noFollow, "http://127.0.0.1/", "a.test")
		return code == http.StatusMovedPermanently
	})
	if code, _, loc := fetch(noFollow, "http://127.0.0.1/some/page?x=1", "a.test"); code != 301 || loc != "https://a.test/some/page?x=1" {
		t.Errorf("plain HTTP page = %d -> %q, want 301 to https://a.test/some/page?x=1", code, loc)
	}
	if code, body, _ := fetch(noFollow, "http://127.0.0.1/.well-known/acme-challenge/tok", "a.test"); code != 200 || body != "tok.thumb" {
		t.Errorf("challenge over HTTP = %d %q, want 200 tok.thumb (it must not be redirected)", code, body)
	}
	if code, _, _ := fetch(noFollow, "http://127.0.0.1/.well-known/acme-challenge/missing", "a.test"); code != 404 {
		t.Errorf("missing token = %d, want 404", code)
	}

	insecure := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "a.test"}}}
	if code, body, _ := fetch(insecure, "https://127.0.0.1/", "a.test"); code != 200 || !strings.Contains(body, "a.test") {
		t.Errorf("HTTPS = %d %q, want the site", code, body)
	}

	// Removing the certificate returns the site to HTTP only.
	delete(state.certs, "a.test")
	if _, err := mgr.RefreshTLS(bg, "a.test"); err != nil {
		t.Fatalf("refresh after removal: %v", err)
	}
	waitFor(t, "plain HTTP to serve again", func() bool {
		code, _, _ := fetch(noFollow, "http://127.0.0.1/", "a.test")
		return code == 200
	})
}

func writeSelfSigned(t *testing.T, dir, host string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	// 0644: the test runs as a normal user, and the container's master reads as root.
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o644); err != nil {
		t.Fatal(err)
	}
}
