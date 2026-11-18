package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
)

func TestSANOnlyCSRHasNoCommonName(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !hasIPName([]string{"example.com", "203.0.113.7"}) || hasIPName([]string{"example.com"}) {
		t.Fatal("hasIPName misclassifies names")
	}
	csr, err := sanOnlyCSR(key, []string{"203.0.113.7", "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if csr.Subject.CommonName != "" {
		t.Errorf("CN = %q, want empty", csr.Subject.CommonName)
	}
	if len(csr.IPAddresses) != 1 || csr.IPAddresses[0].String() != "203.0.113.7" {
		t.Errorf("IPAddresses = %v", csr.IPAddresses)
	}
	if len(csr.DNSNames) != 1 || csr.DNSNames[0] != "example.com" {
		t.Errorf("DNSNames = %v", csr.DNSNames)
	}
}

func TestRequiresEAB(t *testing.T) {
	cases := map[string]bool{
		"https://dv.acme-v02.api.pki.goog/directory":       true,
		"https://dv.acme-v02.test-api.pki.goog/directory":  true,
		"https://acme-v02.api.letsencrypt.org/directory":   false,
		"https://acme-staging-v02.api.letsencrypt.org/dir": false,
		"https://evil-pki.goog.example.com/directory":      false,
		"https://notpki.goog/directory":                    false,
		"::not a url":                                      false,
	}
	for dir, want := range cases {
		if got := requiresEAB(dir); got != want {
			t.Errorf("requiresEAB(%q) = %v, want %v", dir, got, want)
		}
	}
}
