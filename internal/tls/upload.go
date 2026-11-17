package tls

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExpiringSoon is how close to expiry an uploaded certificate earns a warning.
// It is the renewal default, so an upload that would be renewed at once says so.
const ExpiringSoon = 30 * 24 * time.Hour

// Warning codes. A warning never blocks an upload: an operator may knowingly
// install an expired certificate to replace a worse one, or use a chain their
// own clients already hold.
const (
	WarnExpired         = "expired"
	WarnExpiringSoon    = "expiring_soon"
	WarnNotYetValid     = "not_yet_valid"
	WarnIncompleteChain = "incomplete_chain"
	WarnChainReordered  = "chain_reordered"
	WarnChainUnrelated  = "chain_unrelated_ignored"
)

// Warning is a finding that does not stop an upload.
type Warning struct {
	Code    string
	Message string
}

// Parsed is a validated upload, normalised for writing to disk.
type Parsed struct {
	// FullchainPEM is the leaf followed by its chain, issuer after subject.
	FullchainPEM []byte
	// KeyPEM is the private key as unencrypted PKCS#8.
	KeyPEM []byte
	// Names are the DNS names and IP addresses the leaf covers.
	Names     []string
	NotBefore time.Time
	NotAfter  time.Time
	Issuer    string
	Serial    string
	// KeyType is "rsa", "ecdsa" or "ed25519".
	KeyType string
}

// ValidateUpload checks an uploaded certificate, optional extra chain and private
// key for wantDomain, using now for every date comparison.
//
// The first certificate in certPEM is the leaf; further certificates in certPEM
// and chainPEM are issuers in any order. It returns an error when the upload is
// unusable (unparsable PEM, an encrypted key, a key that is not the leaf's,
// a leaf that does not cover wantDomain) and warnings when it is merely
// questionable (expired, expiring, incomplete chain).
func ValidateUpload(certPEM, chainPEM, keyPEM []byte, wantDomain string, now time.Time) (Parsed, []Warning, error) {
	if err := CheckDomain(wantDomain); err != nil {
		return Parsed{}, nil, err
	}
	certs, err := parseCertificates(certPEM, chainPEM)
	if err != nil {
		return Parsed{}, nil, err
	}
	leaf := certs[0]

	key, keyType, err := parseKey(keyPEM)
	if err != nil {
		return Parsed{}, nil, err
	}
	if !publicKeysEqual(key.Public(), leaf.PublicKey) {
		return Parsed{}, nil, errors.New("tls: the private key does not match the certificate")
	}
	if err := leaf.VerifyHostname(wantDomain); err != nil {
		return Parsed{}, nil, fmt.Errorf("tls: the certificate does not cover %s (it covers %s)", wantDomain, strings.Join(certNames(leaf), ", "))
	}

	var warnings []Warning
	ordered, chainWarnings := orderChain(certs)
	warnings = append(warnings, chainWarnings...)
	switch {
	case now.After(leaf.NotAfter):
		warnings = append(warnings, Warning{WarnExpired, "the certificate expired on " + leaf.NotAfter.UTC().Format(time.DateOnly)})
	case now.Before(leaf.NotBefore):
		warnings = append(warnings, Warning{WarnNotYetValid, "the certificate is not valid until " + leaf.NotBefore.UTC().Format(time.DateOnly)})
	case leaf.NotAfter.Sub(now) < ExpiringSoon:
		warnings = append(warnings, Warning{WarnExpiringSoon, "the certificate expires on " + leaf.NotAfter.UTC().Format(time.DateOnly)})
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Parsed{}, nil, fmt.Errorf("tls: encode key: %w", err)
	}
	var full bytes.Buffer
	for _, c := range ordered {
		full.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	return Parsed{
		FullchainPEM: full.Bytes(),
		KeyPEM:       pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		Names:        certNames(leaf),
		NotBefore:    leaf.NotBefore.UTC(),
		NotAfter:     leaf.NotAfter.UTC(),
		Issuer:       leaf.Issuer.String(),
		Serial:       leaf.SerialNumber.Text(16),
		KeyType:      keyType,
	}, warnings, nil
}

func certNames(c *x509.Certificate) []string {
	names := append([]string(nil), c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	return names
}

// parseCertificates reads every CERTIFICATE block of the inputs. Anything else
// in them is an error: a private key pasted into the certificate box is a
// mistake the operator wants to hear about, not have ignored.
func parseCertificates(inputs ...[]byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for _, in := range inputs {
		rest := in
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				if len(bytes.TrimSpace(rest)) != 0 {
					return nil, errors.New("tls: the certificate is not valid PEM")
				}
				break
			}
			if block.Type != "CERTIFICATE" {
				return nil, fmt.Errorf("tls: expected a CERTIFICATE block, found %q", block.Type)
			}
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("tls: parse certificate: %w", err)
			}
			certs = append(certs, c)
		}
	}
	if len(certs) == 0 {
		return nil, errors.New("tls: no certificate found")
	}
	return certs, nil
}

// parseKey reads exactly one unencrypted private key. nginx cannot prompt for a
// passphrase at start, so an encrypted key would take the site down at the next
// reload; refusing it here is the only safe answer.
func parseKey(in []byte) (crypto.Signer, string, error) {
	block, rest := pem.Decode(in)
	if block == nil {
		return nil, "", errors.New("tls: the private key is not valid PEM")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		if extra, _ := pem.Decode(rest); extra != nil {
			return nil, "", errors.New("tls: the private key input holds more than one PEM block")
		}
		return nil, "", errors.New("tls: the private key is not valid PEM")
	}
	if block.Type == "ENCRYPTED PRIVATE KEY" || block.Headers["Proc-Type"] != "" || block.Headers["DEK-Info"] != "" {
		return nil, "", errors.New("tls: the private key is encrypted; remove the passphrase first (openssl pkey -in key.pem -out key-plain.pem)")
	}

	var parsed any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, "", fmt.Errorf("tls: expected a private key, found a %q block", block.Type)
	}
	if err != nil {
		return nil, "", fmt.Errorf("tls: parse private key: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, "", errors.New("tls: unsupported private key type")
	}
	return signer, keyTypeOf(signer.Public()), nil
}

func keyTypeOf(pub crypto.PublicKey) string {
	switch pub.(type) {
	case *rsa.PublicKey:
		return "rsa"
	case *ecdsa.PublicKey:
		return "ecdsa"
	case ed25519.PublicKey:
		return "ed25519"
	}
	return "unknown"
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	eq, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && eq.Equal(b)
}

// orderChain returns the leaf followed by the certificates that form its issuer
// chain, in issuing order, and warns about what it had to fix.
//
// Servers send leaf then issuer then its issuer; clients that read the file in
// another order fail verification in ways that look like network problems.
func orderChain(certs []*x509.Certificate) ([]*x509.Certificate, []Warning) {
	var warnings []Warning
	ordered := []*x509.Certificate{certs[0]}
	pool := append([]*x509.Certificate(nil), certs[1:]...)

	for {
		cur := ordered[len(ordered)-1]
		if bytes.Equal(cur.RawIssuer, cur.RawSubject) {
			break // self-signed root: the chain ends here
		}
		next := -1
		for i, c := range pool {
			if bytes.Equal(c.RawSubject, cur.RawIssuer) && cur.CheckSignatureFrom(c) == nil {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		ordered = append(ordered, pool[next])
		pool = append(pool[:next], pool[next+1:]...)
	}

	if len(pool) > 0 {
		warnings = append(warnings, Warning{WarnChainUnrelated, fmt.Sprintf("%d certificate(s) that do not belong to this chain were left out", len(pool))})
	}
	// Reordered means the chain, as given, was not already in issuing order.
	for i := 1; i < len(ordered); i++ {
		if ordered[i] != certs[i] {
			warnings = append(warnings, Warning{WarnChainReordered, "the chain was not in order (leaf, issuer, root) and was reordered"})
			break
		}
	}
	last := ordered[len(ordered)-1]
	if len(ordered) == 1 && !bytes.Equal(last.RawIssuer, last.RawSubject) {
		warnings = append(warnings, Warning{WarnIncompleteChain, "no issuer certificate was supplied; clients that do not already hold it will reject the site"})
	}
	return ordered, warnings
}
