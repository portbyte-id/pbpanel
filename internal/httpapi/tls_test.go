package httpapi

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// certPathsIn returns paths inside a fresh temp dir.
func certPathsIn(t *testing.T) CertPaths {
	t.Helper()
	dir := t.TempDir()
	return CertPaths{
		CertFile: filepath.Join(dir, "ssl", "panel.crt"),
		KeyFile:  filepath.Join(dir, "ssl", "panel.key"),
	}
}

// TestGenerateSelfSignedWritesUsablePair is the core TLS requirement: the pair
// on disk loads through crypto/tls and the certificate is valid for the names
// it was asked for.
func TestGenerateSelfSignedWritesUsablePair(t *testing.T) {
	paths := certPathsIn(t)
	hosts := []string{"192.0.2.10", "panel.example.test"}

	if err := GenerateSelfSigned(paths, hosts); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	// LoadX509KeyPair is the same call the listener makes, so a pair that
	// passes here is a pair the daemon can serve.
	pair, err := tls.LoadX509KeyPair(paths.CertFile, paths.KeyFile)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if len(pair.Certificate) == 0 {
		t.Fatal("loaded pair has no certificate")
	}

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	now := time.Now()
	if now.Before(leaf.NotBefore) {
		t.Errorf("certificate is not valid yet: NotBefore %v", leaf.NotBefore)
	}
	if now.After(leaf.NotAfter) {
		t.Errorf("certificate already expired: NotAfter %v", leaf.NotAfter)
	}

	// The IP must be an IP SAN and the name a DNS SAN: a browser rejects an IP
	// listed only as a DNS name.
	if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("192.0.2.10")) {
		t.Errorf("IP SANs = %v, want 192.0.2.10", leaf.IPAddresses)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "panel.example.test" {
		t.Errorf("DNS SANs = %v, want panel.example.test", leaf.DNSNames)
	}
	if err := leaf.VerifyHostname("192.0.2.10"); err != nil {
		t.Errorf("VerifyHostname(ip): %v", err)
	}
	if err := leaf.VerifyHostname("panel.example.test"); err != nil {
		t.Errorf("VerifyHostname(dns): %v", err)
	}

	// It must be self-signed: the issuer is the subject.
	if leaf.Issuer.String() != leaf.Subject.String() {
		t.Errorf("certificate is not self-signed: issuer %q, subject %q", leaf.Issuer, leaf.Subject)
	}
}

// TestGeneratedPairServesTLS proves the pair is usable end to end: a TLS server
// built from it completes a handshake, which is what the listener needs.
func TestGeneratedPairServesTLS(t *testing.T) {
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"127.0.0.1"}); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	pair, err := tls.LoadX509KeyPair(paths.CertFile, paths.KeyFile)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
	})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.(*tls.Conn).Handshake()
		_ = conn.Close()
	}()

	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true, // self-signed: there is no CA to check here
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("handshake returned no peer certificate")
	}
	if err := state.PeerCertificates[0].VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("served certificate does not cover 127.0.0.1: %v", err)
	}
}

// TestGenerateSelfSignedOverwrites proves a second run replaces the pair rather
// than failing on the existing files, which is what install's repair path needs.
func TestGenerateSelfSignedOverwrites(t *testing.T) {
	paths := certPathsIn(t)

	if err := GenerateSelfSigned(paths, []string{"192.0.2.1"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first, err := os.ReadFile(paths.CertFile)
	if err != nil {
		t.Fatalf("read first cert: %v", err)
	}

	if err := GenerateSelfSigned(paths, []string{"192.0.2.2"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second, err := os.ReadFile(paths.CertFile)
	if err != nil {
		t.Fatalf("read second cert: %v", err)
	}
	if string(first) == string(second) {
		t.Error("second run did not replace the certificate")
	}
}

// TestGenerateSelfSignedRequiresPaths proves an empty path is refused rather
// than writing somewhere unintended.
func TestGenerateSelfSignedRequiresPaths(t *testing.T) {
	if err := GenerateSelfSigned(CertPaths{}, []string{"x"}); err == nil {
		t.Fatal("GenerateSelfSigned accepted empty paths")
	}
	if err := GenerateSelfSigned(CertPaths{CertFile: filepath.Join(t.TempDir(), "c.pem")}, []string{"x"}); err == nil {
		t.Fatal("GenerateSelfSigned accepted a missing key path")
	}
}

// TestDefaultCertPaths proves the production path is a default the caller opts
// into, and that it names the files the plan documents.
func TestDefaultCertPaths(t *testing.T) {
	got := DefaultCertPaths("")
	want := CertPaths{
		CertFile: "/www/panel/ssl/panel.crt",
		KeyFile:  "/www/panel/ssl/panel.key",
	}
	if got != want {
		t.Errorf("DefaultCertPaths(\"\") = %+v, want %+v", got, want)
	}

	custom := DefaultCertPaths("/srv/panel/tls")
	if custom.CertFile != "/srv/panel/tls/panel.crt" {
		t.Errorf("DefaultCertPaths with a dir = %+v", custom)
	}
}

// TestCertIsPEM proves the files are PEM, which is what every tool that reads a
// certificate expects (openssl, nginx, the browser's import dialog).
func TestCertIsPEM(t *testing.T) {
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"192.0.2.3"}); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	for name, path := range map[string]string{"cert": paths.CertFile, "key": paths.KeyFile} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		block, _ := pem.Decode(data)
		if block == nil {
			t.Errorf("%s is not PEM", name)
			continue
		}
		if name == "cert" && block.Type != "CERTIFICATE" {
			t.Errorf("cert block type = %q, want CERTIFICATE", block.Type)
		}
		if name == "key" && block.Type != "EC PRIVATE KEY" {
			t.Errorf("key block type = %q, want EC PRIVATE KEY", block.Type)
		}
	}
}
