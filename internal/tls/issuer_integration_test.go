//go:build linux && integration

// Integration test of the ACME issuer against Pebble, Let's Encrypt's test CA.
// It skips unless PEBBLE_DIRECTORY is set.
//
// Run Pebble and its fake DNS/challenge server on the host network (Docker in
// WSL Ubuntu-26.04; from Git Bash prefix wsl commands with MSYS_NO_PATHCONV=1):
//
//	docker run -d --rm --name pb-challtestsrv --network host \
//	    ghcr.io/letsencrypt/pebble-challtestsrv:latest \
//	    -defaultIPv6 "" -defaultIPv4 127.0.0.1 -http01 "" -https01 "" -tlsalpn01 ""
//	docker run -d --rm --name pb-pebble --network host \
//	    -e PEBBLE_VA_NOSLEEP=1 \
//	    ghcr.io/letsencrypt/pebble:latest \
//	    -config test/config/pebble-config.json -dnsserver 127.0.0.1:8053
//
// then, in the repository:
//
//	PEBBLE_DIRECTORY=https://localhost:14000/dir \
//	PEBBLE_CHALLTESTSRV=http://localhost:8055 \
//	go test -tags integration -run Pebble -v ./internal/tls/
//
// challtestsrv answers every name with 127.0.0.1, so Pebble's validator
// connects to this process on port 5002 (its default httpPort) for HTTP-01 and
// reads TXT records this test publishes through challtestsrv's management API
// for DNS-01. Both challenges are therefore really validated.
//
// Alternative for DNS-01 only: start Pebble with -e PEBBLE_VA_ALWAYS_VALID=1
// and set PEBBLE_VA_ALWAYS_VALID=1 for the test as well; the DNS-01 test then
// uses the in-memory provider and needs no challtestsrv. HTTP-01 still needs
// the setup above (always-valid skips the fetch, so it would prove nothing).
package tls

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
)

const pebbleHTTPPort = "127.0.0.1:5002"

// challtestsrvDNS publishes TXT records through pebble-challtestsrv's
// management API, which is the DNS server Pebble validates against.
type challtestsrvDNS struct{ base string }

func (challtestsrvDNS) Name() string { return "challtestsrv" }

func (c challtestsrvDNS) post(ctx context.Context, path, host, value string) error {
	body, _ := json.Marshal(map[string]string{"host": host, "value": value})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("challtestsrv %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

func (c challtestsrvDNS) Present(ctx context.Context, _, fqdn, value string) error {
	return c.post(ctx, "/set-txt", fqdn, value)
}

func (c challtestsrvDNS) CleanUp(ctx context.Context, _, fqdn, _ string) error {
	return c.post(ctx, "/clear-txt", fqdn, "")
}

func pebbleIssuer(t *testing.T, r *rig, providers func(string, []byte) (DNSProvider, error)) *Issuer {
	t.Helper()
	dir := os.Getenv("PEBBLE_DIRECTORY")
	if dir == "" {
		t.Skip("PEBBLE_DIRECTORY is not set; see the header of this file for how to run Pebble in Docker")
	}
	// Pebble's ACME endpoint uses a throwaway minica certificate. This client
	// only ever talks to the Pebble instance named by PEBBLE_DIRECTORY.
	hc := &http.Client{
		Timeout:   time.Minute,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // local Pebble test CA
	}
	// Pre-flight dials the local file server instead of port 80.
	probe := newProbeClient()
	probe.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, pebbleHTTPPort)
	}
	i := newTestIssuer(t, r, IssuerOptions{
		HTTPClient: hc, ProbeClient: probe, Providers: providers,
		SkipDNSPropagationCheck: true,
		Directory:               func() string { return dir },
	})
	return i
}

func serveWebroot(t *testing.T, i *Issuer) {
	t.Helper()
	if err := EnsureACMEWebroot(i.root); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", pebbleHTTPPort)
	if err != nil {
		t.Fatalf("listen %s (Pebble's validator connects here): %v", pebbleHTTPPort, err)
	}
	srv := &http.Server{Handler: http.FileServer(http.Dir(filepath.FromSlash(ACMEWebroot(i.root))))}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func assertIssued(t *testing.T, i *Issuer, res Result, source string, names ...string) {
	t.Helper()
	if res.Source != source || len(res.Names) != len(names) {
		t.Fatalf("result = %+v", res)
	}
	certFile, keyFile := SitePaths(i.root, res.Domain)
	certPEM, err := os.ReadFile(filepath.FromSlash(certFile))
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(filepath.FromSlash(keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("written files are not a matching pair: %v", err)
	}
	leaf, err := certcrypto.ParsePEMCertificate(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := leaf.VerifyHostname(strings.ReplaceAll(n, "*", "any")); err != nil {
			t.Errorf("certificate does not cover %s: %v", n, err)
		}
	}
	if !strings.Contains(res.Issuer, "Pebble") {
		t.Errorf("issuer = %q, want a Pebble CA", res.Issuer)
	}
	row, err := i.st.GetCert(context.Background(), res.Domain)
	if err != nil {
		t.Fatal(err)
	}
	if row.Source != source || row.Serial != res.Serial || row.LastRenewalResult != RenewalOK {
		t.Fatalf("stored row = %+v", row)
	}
}

func TestPebbleHTTP01(t *testing.T) {
	r := newRig(t)
	i := pebbleIssuer(t, r, nil)
	serveWebroot(t, i)
	ctx := context.Background()

	first, err := i.IssueHTTP01(ctx, "pb-http01.example.com", []string{"pb-http01.example.com", "www.pb-http01.example.com"}, "ops@example.com")
	if err != nil {
		t.Fatalf("IssueHTTP01: %v", err)
	}
	assertIssued(t, i, first, SourceHTTP01, "pb-http01.example.com", "www.pb-http01.example.com")
	acct, err := r.st.GetAccount(ctx, first.Directory)
	if err != nil {
		t.Fatal(err)
	}

	// Renewal: same account, new key and serial.
	second, err := i.IssueHTTP01(ctx, "pb-http01.example.com", []string{"pb-http01.example.com", "www.pb-http01.example.com"}, "ops@example.com")
	if err != nil {
		t.Fatalf("second IssueHTTP01: %v", err)
	}
	if second.Serial == first.Serial {
		t.Error("renewal produced the same serial")
	}
	again, err := r.st.GetAccount(ctx, first.Directory)
	if err != nil || !bytes.Equal(again.Key, acct.Key) {
		t.Errorf("the account key changed between orders: %v", err)
	}

	// A wildcard never reaches the CA over HTTP-01.
	if _, err := i.IssueHTTP01(ctx, "pb-http01.example.com", []string{"*.pb-http01.example.com"}, ""); err == nil {
		t.Error("wildcard over HTTP-01 was accepted")
	}
	// Nothing of the challenge is left in the webroot.
	left, _ := os.ReadDir(filepath.Join(filepath.FromSlash(ACMEWebroot(i.root)), ".well-known", "acme-challenge"))
	if len(left) != 0 {
		t.Errorf("challenge files left behind: %v", left)
	}
}

func TestPebbleDNS01Wildcard(t *testing.T) {
	r := newRig(t)
	var provider DNSProvider
	switch {
	case os.Getenv("PEBBLE_CHALLTESTSRV") != "":
		provider = challtestsrvDNS{base: os.Getenv("PEBBLE_CHALLTESTSRV")}
	case os.Getenv("PEBBLE_VA_ALWAYS_VALID") == "1":
		provider = NewMemoryDNS()
	default:
		t.Skip("set PEBBLE_CHALLTESTSRV (real validation) or PEBBLE_VA_ALWAYS_VALID=1 (Pebble started with it)")
	}
	i := pebbleIssuer(t, r, func(string, []byte) (DNSProvider, error) { return provider, nil })
	ctx := context.Background()
	if err := r.st.PutProvider(ctx, Provider{Name: "fake", Kind: "fake", Credentials: []byte("{}")}); err != nil {
		t.Fatal(err)
	}

	names := []string{"pb-dns01.example.com", "*.pb-dns01.example.com"}
	res, err := i.IssueDNS01(ctx, "pb-dns01.example.com", names, "fake", "ops@example.com")
	if err != nil {
		t.Fatalf("IssueDNS01: %v", err)
	}
	assertIssued(t, i, res, SourceDNS01, names...)
	if m, ok := provider.(*MemoryDNS); ok {
		calls := m.Calls()
		if len(calls) != 4 { // two presents, two cleanups
			t.Errorf("provider calls = %v, want 2 presents and 2 cleanups", calls)
		}
		if got := m.Records("_acme-challenge.pb-dns01.example.com."); len(got) != 0 {
			t.Errorf("TXT records left behind: %v", got)
		}
	}
}
