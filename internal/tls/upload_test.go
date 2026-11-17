package tls

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

type testCert struct {
	cert *x509.Certificate
	key  crypto.Signer
	der  []byte
}

func (c testCert) pem() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})
}

func (c testCert) keyPEM(t *testing.T) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(c.key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

var serial int64

func issue(t *testing.T, parent *testCert, cn string, ca bool, names []string, notBefore, notAfter time.Time, key crypto.Signer) testCert {
	t.Helper()
	if key == nil {
		var err error
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			t.Fatal(err)
		}
	}
	serial++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		DNSNames:              names,
		BasicConstraintsValid: true,
		IsCA:                  ca,
	}
	if ca {
		tmpl.KeyUsage = x509.KeyUsageCertSign
	}
	signer, signCert := key, tmpl
	if parent != nil {
		signer, signCert = parent.key, parent.cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signCert, key.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCert{cert: c, key: key, der: der}
}

type pki struct{ root, inter, leaf testCert }

func newPKI(t *testing.T, names ...string) pki {
	t.Helper()
	nb, na := testNow.Add(-24*time.Hour), testNow.Add(90*24*time.Hour)
	root := issue(t, nil, "Test Root", true, nil, nb, na, nil)
	inter := issue(t, &root, "Test Intermediate", true, nil, nb, na, nil)
	leaf := issue(t, &inter, "leaf", false, names, nb, na, nil)
	return pki{root, inter, leaf}
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func hasWarning(ws []Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestValidateUploadAcceptsAGoodChain(t *testing.T) {
	p := newPKI(t, "example.com", "www.example.com")
	parsed, warns, err := ValidateUpload(p.leaf.pem(), cat(p.inter.pem(), p.root.pem()), p.leaf.keyPEM(t), "example.com", testNow)
	if err != nil {
		t.Fatalf("ValidateUpload: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %+v, want none", warns)
	}
	if strings.Join(parsed.Names, ",") != "example.com,www.example.com" || parsed.KeyType != "ecdsa" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !parsed.NotAfter.Equal(p.leaf.cert.NotAfter) || parsed.Serial == "" || parsed.Issuer == "" {
		t.Fatalf("parsed metadata = %+v", parsed)
	}
	// The output is leaf, intermediate, root; the key is re-encoded as PKCS#8.
	certs, err := parseCertificates(parsed.FullchainPEM)
	if err != nil || len(certs) != 3 || !certs[0].Equal(p.leaf.cert) || !certs[1].Equal(p.inter.cert) {
		t.Fatalf("fullchain = %d certs, %v", len(certs), err)
	}
	if !strings.Contains(string(parsed.KeyPEM), "BEGIN PRIVATE KEY") {
		t.Fatal("key was not normalised to PKCS#8")
	}
}

func TestValidateUploadRejectsAMismatchedKey(t *testing.T) {
	p := newPKI(t, "example.com")
	other := newPKI(t, "example.com")
	_, _, err := ValidateUpload(p.leaf.pem(), p.inter.pem(), other.leaf.keyPEM(t), "example.com", testNow)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v, want a key mismatch", err)
	}
}

func TestValidateUploadKeyTypes(t *testing.T) {
	nb, na := testNow.Add(-time.Hour), testNow.Add(90*24*time.Hour)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, _ := x509.MarshalECPrivateKey(ecKey)

	cases := []struct {
		name    string
		key     crypto.Signer
		pemKey  []byte
		keyType string
	}{
		{"rsa pkcs1", rsaKey, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}), "rsa"},
		{"ec sec1", ecKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}), "ecdsa"},
		{"ed25519 pkcs8", edKey, nil, "ed25519"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			leaf := issue(t, nil, "leaf", false, []string{"example.com"}, nb, na, c.key)
			keyPEM := c.pemKey
			if keyPEM == nil {
				keyPEM = leaf.keyPEM(t)
			}
			parsed, _, err := ValidateUpload(leaf.pem(), nil, keyPEM, "example.com", testNow)
			if err != nil {
				t.Fatalf("ValidateUpload: %v", err)
			}
			if parsed.KeyType != c.keyType {
				t.Fatalf("KeyType = %s, want %s", parsed.KeyType, c.keyType)
			}
		})
	}
}

func TestValidateUploadRejectsAnEncryptedKey(t *testing.T) {
	p := newPKI(t, "example.com")
	legacy := string(pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00"},
		Bytes:   []byte("x"),
	}))
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("x")}))
	for name, key := range map[string]string{"legacy": legacy, "pkcs8": pkcs8} {
		_, _, err := ValidateUpload(p.leaf.pem(), p.inter.pem(), []byte(key), "example.com", testNow)
		if err == nil || !strings.Contains(err.Error(), "encrypted") {
			t.Fatalf("%s: err = %v, want an encrypted-key error", name, err)
		}
	}
}

func TestValidateUploadRejectsUnparsableInput(t *testing.T) {
	p := newPKI(t, "example.com")
	good, key := p.leaf.pem(), p.leaf.keyPEM(t)
	cases := map[string]struct{ cert, chain, key []byte }{
		"empty cert":     {nil, nil, key},
		"garbage cert":   {[]byte("not pem"), nil, key},
		"trailing junk":  {cat(good, []byte("junk")), nil, key},
		"bad der":        {pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}), nil, key},
		"key as cert":    {key, nil, key},
		"garbage chain":  {good, []byte("zzz"), key},
		"empty key":      {good, nil, nil},
		"garbage key":    {good, nil, []byte("zzz")},
		"cert as key":    {good, nil, good},
		"two key blocks": {good, nil, cat(key, key)},
		"bad key der":    {good, nil, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})},
	}
	for name, c := range cases {
		if _, _, err := ValidateUpload(c.cert, c.chain, c.key, "example.com", testNow); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateUploadRejectsABadDomain(t *testing.T) {
	p := newPKI(t, "example.com")
	for _, d := range []string{"", "../etc", "a/b", "Example.com", "*.example.com"} {
		if _, _, err := ValidateUpload(p.leaf.pem(), nil, p.leaf.keyPEM(t), d, testNow); err == nil {
			t.Errorf("domain %q accepted", d)
		}
	}
}

func TestValidateUploadDomainCoverage(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		want  string
		ok    bool
	}{
		{"exact", []string{"example.com"}, "example.com", true},
		{"alias san", []string{"example.com", "www.example.com"}, "www.example.com", true},
		{"wildcard covers one label", []string{"*.example.com"}, "app.example.com", true},
		{"wildcard does not cover the apex", []string{"*.example.com"}, "example.com", false},
		{"wildcard does not cover two labels", []string{"*.example.com"}, "a.b.example.com", false},
		{"wildcard plus apex", []string{"example.com", "*.example.com"}, "example.com", true},
		{"other domain", []string{"other.com"}, "example.com", false},
		{"suffix lookalike", []string{"notexample.com"}, "example.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPKI(t, c.names...)
			_, _, err := ValidateUpload(p.leaf.pem(), p.inter.pem(), p.leaf.keyPEM(t), c.want, testNow)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestValidateUploadCommonNameDoesNotCountAsCoverage(t *testing.T) {
	nb, na := testNow.Add(-time.Hour), testNow.Add(24*time.Hour*90)
	leaf := issue(t, nil, "example.com", false, nil, nb, na, nil) // CN only, no SAN
	if _, _, err := ValidateUpload(leaf.pem(), nil, leaf.keyPEM(t), "example.com", testNow); err == nil {
		t.Fatal("a certificate with no SANs was accepted on its common name")
	}
}

func TestValidateUploadDateWarnings(t *testing.T) {
	cases := []struct {
		name       string
		nb, na     time.Time
		wantWarn   string
		wantNoWarn bool
	}{
		{"expired", testNow.Add(-90 * 24 * time.Hour), testNow.Add(-time.Hour), WarnExpired, false},
		{"expiring in 10 days", testNow.Add(-24 * time.Hour), testNow.Add(10 * 24 * time.Hour), WarnExpiringSoon, false},
		{"expiring in 29 days", testNow.Add(-24 * time.Hour), testNow.Add(29 * 24 * time.Hour), WarnExpiringSoon, false},
		{"fine at 31 days", testNow.Add(-24 * time.Hour), testNow.Add(31 * 24 * time.Hour), "", true},
		{"not yet valid", testNow.Add(time.Hour), testNow.Add(90 * 24 * time.Hour), WarnNotYetValid, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := issue(t, nil, "Root", true, nil, c.nb, c.na, nil)
			leaf := issue(t, &root, "leaf", false, []string{"example.com"}, c.nb, c.na, nil)
			_, warns, err := ValidateUpload(leaf.pem(), root.pem(), leaf.keyPEM(t), "example.com", testNow)
			if err != nil {
				t.Fatalf("a date problem must warn, not fail: %v", err)
			}
			if c.wantNoWarn {
				if len(warns) != 0 {
					t.Fatalf("warnings = %+v, want none", warns)
				}
				return
			}
			if !hasWarning(warns, c.wantWarn) {
				t.Fatalf("warnings = %+v, want %s", warns, c.wantWarn)
			}
		})
	}
}

func TestValidateUploadUsesTheInjectedClock(t *testing.T) {
	p := newPKI(t, "example.com") // valid for 90 days from testNow
	_, warns, err := ValidateUpload(p.leaf.pem(), p.inter.pem(), p.leaf.keyPEM(t), "example.com", testNow.Add(365*24*time.Hour))
	if err != nil || !hasWarning(warns, WarnExpired) {
		t.Fatalf("a year later: warns = %+v, err = %v; want expired", warns, err)
	}
}

func TestValidateUploadChainHandling(t *testing.T) {
	p := newPKI(t, "example.com")
	key := p.leaf.keyPEM(t)

	t.Run("leaf only warns about an incomplete chain", func(t *testing.T) {
		_, warns, err := ValidateUpload(p.leaf.pem(), nil, key, "example.com", testNow)
		if err != nil || !hasWarning(warns, WarnIncompleteChain) {
			t.Fatalf("warns = %+v, err = %v", warns, err)
		}
	})
	t.Run("intermediate without root is complete", func(t *testing.T) {
		_, warns, err := ValidateUpload(p.leaf.pem(), p.inter.pem(), key, "example.com", testNow)
		if err != nil || len(warns) != 0 {
			t.Fatalf("warns = %+v, err = %v", warns, err)
		}
	})
	t.Run("chain in the wrong order is reordered", func(t *testing.T) {
		parsed, warns, err := ValidateUpload(p.leaf.pem(), cat(p.root.pem(), p.inter.pem()), key, "example.com", testNow)
		if err != nil || !hasWarning(warns, WarnChainReordered) {
			t.Fatalf("warns = %+v, err = %v", warns, err)
		}
		certs, _ := parseCertificates(parsed.FullchainPEM)
		if len(certs) != 3 || !certs[1].Equal(p.inter.cert) || !certs[2].Equal(p.root.cert) {
			t.Fatalf("fullchain order wrong: %d certs", len(certs))
		}
	})
	t.Run("bundle in the certificate box works", func(t *testing.T) {
		_, warns, err := ValidateUpload(cat(p.leaf.pem(), p.inter.pem(), p.root.pem()), nil, key, "example.com", testNow)
		if err != nil || len(warns) != 0 {
			t.Fatalf("warns = %+v, err = %v", warns, err)
		}
	})
	t.Run("an unrelated certificate is dropped with a warning", func(t *testing.T) {
		stranger := newPKI(t, "other.com")
		parsed, warns, err := ValidateUpload(p.leaf.pem(), cat(p.inter.pem(), stranger.root.pem()), key, "example.com", testNow)
		if err != nil || !hasWarning(warns, WarnChainUnrelated) {
			t.Fatalf("warns = %+v, err = %v", warns, err)
		}
		certs, _ := parseCertificates(parsed.FullchainPEM)
		if len(certs) != 2 {
			t.Fatalf("fullchain has %d certs, want leaf+intermediate", len(certs))
		}
	})
	t.Run("a chain that does not sign the leaf is not followed", func(t *testing.T) {
		stranger := newPKI(t, "other.com")
		_, warns, err := ValidateUpload(p.leaf.pem(), stranger.inter.pem(), key, "example.com", testNow)
		if err != nil || !hasWarning(warns, WarnIncompleteChain) {
			t.Fatalf("warns = %+v, err = %v", warns, err)
		}
	})
}

func TestCheckDomain(t *testing.T) {
	good := []string{"example.com", "a.b.example.com", "xn--bcher-kva.example", "localhost", "a-b.com", "1.example.com"}
	bad := []string{"", ".", "a..b", "-a.com", "a-.com", "a_b.com", "A.com", "a/b", "..", "a b.com", "*.example.com", "a.com\x00", strings.Repeat("a", 64) + ".com"}
	for _, d := range good {
		if err := CheckDomain(d); err != nil {
			t.Errorf("CheckDomain(%q) = %v", d, err)
		}
	}
	for _, d := range bad {
		if err := CheckDomain(d); err == nil {
			t.Errorf("CheckDomain(%q) accepted", d)
		}
	}
}

func TestSitePaths(t *testing.T) {
	cert, key := SitePaths("/www", "example.com")
	if cert != "/www/panel/ssl/sites/example.com/fullchain.pem" || key != "/www/panel/ssl/sites/example.com/privkey.pem" {
		t.Fatalf("SitePaths = %q, %q", cert, key)
	}
	if got := ACMEWebroot("/www"); got != "/www/panel/ssl/acme-webroot" {
		t.Fatalf("ACMEWebroot = %q", got)
	}
}
