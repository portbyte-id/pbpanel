package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/jobs"
	tlspkg "github.com/portbyte-id/server-panel/internal/tls"
)

// A certificate for a name with no site is stored, written to disk, and leaves
// nginx alone. A site created for that name afterwards serves it.
func TestStandaloneUploadNeedsNoSite(t *testing.T) {
	h := newTLSHarness(t)
	c, k := goodCert(t, 6001, "shop.test")

	code, out := h.call(http.MethodPost, "/api/tls/certs/upload", map[string]any{
		"domain": "Shop.test", "certificate": c, "private_key": k,
	})
	if code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, out)
	}
	cert := out["cert"].(map[string]any)
	if cert["domain"] != "shop.test" || cert["source"] != "upload" {
		t.Errorf("cert = %v", cert)
	}
	if _, err := h.cs.GetCert(context.Background(), "shop.test"); err != nil {
		t.Fatalf("no certificate row stored: %v", err)
	}
	if _, ok := h.fs.files["/www/panel/nginx/sites/shop.test.conf"]; ok {
		t.Error("a site file was written for a certificate with no site")
	}
	if strings.Contains(string(mustJSON(t, out)), "PRIVATE KEY") {
		t.Error("the response carries key material")
	}

	// The site created afterwards is served with the stored certificate.
	h.mustCreate(map[string]any{"domain": "shop.test", "kind": "static"})
	if !strings.Contains(h.tlsBlock("shop.test"), "ssl_certificate /www/panel/ssl/sites/shop.test/fullchain.pem;") {
		t.Errorf("site did not pick up the stored certificate:\n%s", h.tlsBlock("shop.test"))
	}
}

func TestStandaloneUploadValidates(t *testing.T) {
	h := newTLSHarness(t)
	c, k := goodCert(t, 6002, "shop.test")

	if code, out := h.call(http.MethodPost, "/api/tls/certs/upload", map[string]any{
		"domain": "not a host", "certificate": c, "private_key": k,
	}); code != http.StatusBadRequest || out["field"] != "domain" {
		t.Errorf("bad domain: %d %v", code, out)
	}
	if code, out := h.call(http.MethodPost, "/api/tls/certs/upload", map[string]any{
		"domain": "other.test", "certificate": c, "private_key": k,
	}); code != http.StatusBadRequest {
		t.Errorf("certificate for another name: %d %v", code, out)
	}
	if _, err := h.cs.GetCert(context.Background(), "other.test"); err == nil {
		t.Error("a mismatched certificate was stored")
	}
}

func TestStandaloneIssueWithoutASite(t *testing.T) {
	h := newTLSHarness(t)

	// HTTP-01 needs nginx to answer for the name, so it needs a site.
	code, out := h.call(http.MethodPost, "/api/tls/certs/issue", map[string]any{"domain": "api.test", "method": "http01"})
	if code != http.StatusConflict || !strings.Contains(out["detail"].(string), "create a site") {
		t.Errorf("http01 without a site: %d %v", code, out)
	}

	// DNS-01 needs no site: the certificate is ordered and stored for the name.
	h.call(http.MethodPut, "/api/tls/providers/cf", map[string]any{"kind": "cloudflare", "credentials": map[string]string{"api_token": "t"}})
	code, out = h.call(http.MethodPost, "/api/tls/certs/issue", map[string]any{"domain": "API.test", "method": "dns01", "provider": "cf"})
	if code != http.StatusAccepted {
		t.Fatalf("issue: %d %v", code, out)
	}
	if done := h.waitJob(out); done.State != jobs.StateSucceeded {
		t.Fatalf("job = %+v", done)
	}
	if _, err := h.cs.GetCert(context.Background(), "api.test"); err != nil {
		t.Errorf("no certificate row stored: %v", err)
	}
	if _, ok := h.fs.files["/www/panel/nginx/sites/api.test.conf"]; ok {
		t.Error("a site file was written for a certificate with no site")
	}

	if code, out := h.call(http.MethodPost, "/api/tls/certs/issue", map[string]any{"domain": "bad..host", "method": "dns01", "provider": "cf"}); code != http.StatusBadRequest || out["field"] != "domain" {
		t.Errorf("bad domain: %d %v", code, out)
	}
}

func TestStandaloneDeleteRefusesASiteAndRemovesTheRest(t *testing.T) {
	h := newTLSHarness(t)
	c, k := goodCert(t, 6003, "shop.test")
	if code, out := h.call(http.MethodPost, "/api/tls/certs/upload", map[string]any{
		"domain": "shop.test", "certificate": c, "private_key": k,
	}); code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, out)
	}

	// Once a site serves the name, its certificate goes through the site instead.
	h.mustCreate(map[string]any{"domain": "shop.test", "kind": "static"})
	if code, out := h.call(http.MethodDelete, "/api/tls/certs/shop.test", nil); code != http.StatusConflict {
		t.Errorf("delete under a site: %d %v", code, out)
	}

	// A certificate with no site is removed, and asking again is a 404.
	c2, k2 := goodCert(t, 6004, "loose.test")
	if code, out := h.call(http.MethodPost, "/api/tls/certs/upload", map[string]any{
		"domain": "loose.test", "certificate": c2, "private_key": k2,
	}); code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, out)
	}
	if code, out := h.call(http.MethodDelete, "/api/tls/certs/loose.test", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, err := h.cs.GetCert(context.Background(), "loose.test"); !errors.Is(err, tlspkg.ErrNotFound) {
		t.Errorf("certificate survived the delete: %v", err)
	}
	if code, _ := h.call(http.MethodDelete, "/api/tls/certs/loose.test", nil); code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", code)
	}
}
