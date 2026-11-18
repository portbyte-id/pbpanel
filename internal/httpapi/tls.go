package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path"
	"path/filepath"
	"time"
)

// CertPaths is where the panel's certificate and key live. The caller supplies
// both: this package never hard-codes `/www/panel/ssl`, so a test writes into a
// temp dir and an install decides the production path.
type CertPaths struct {
	CertFile string
	KeyFile  string
}

// DefaultCertDir is the directory install uses for the panel certificate, from
// the filesystem layout in plan.md §2. It is a default for the caller, not a
// value this package reaches for on its own.
const DefaultCertDir = "/www/panel/ssl"

// DefaultCertPaths returns the production certificate paths under dir. An empty
// dir means DefaultCertDir.
//
// path.Join, not filepath.Join: these are Linux paths, and filepath.Join would
// rewrite them with backslashes on the Windows dev machine the tests run on,
// producing a path the daemon would never use. The settings registry's path
// validator makes the same choice for the same reason.
func DefaultCertPaths(dir string) CertPaths {
	if dir == "" {
		dir = DefaultCertDir
	}
	return CertPaths{
		CertFile: path.Join(dir, "panel.crt"),
		KeyFile:  path.Join(dir, "panel.key"),
	}
}

const (
	certValidity = 10 * 365 * 24 * time.Hour

	// certFileMode is world-readable: a certificate is public by design and a
	// browser has to be handed it. keyFileMode is owner-only, which is the
	// whole point of keeping the key in its own file.
	certFileMode os.FileMode = 0o644
	keyFileMode  os.FileMode = 0o600

	// dirMode keeps the directory root-only: nothing but the daemon needs to
	// list or traverse it.
	dirMode os.FileMode = 0o700
)

// bootstrapOrg is the organization the installer's self-signed certificate
// carries, which is how the panel tells it from one an operator uploaded.
const bootstrapOrg = "Server Panel"

// GenerateSelfSigned writes a self-signed certificate and key to paths. It
// creates the directory with 0750, replaces any existing pair, and leaves the
// key at 0600.
//
// hosts are the names the certificate must be valid for: the panel's bind
// address and any name the operator will type. A bare IP is put in the IP
// SAN list and everything else in the DNS list, because a certificate with an IP
// in the DNS list is rejected by every browser.
func GenerateSelfSigned(paths CertPaths, hosts []string) error {
	if paths.CertFile == "" || paths.KeyFile == "" {
		return errors.New("httpapi: certificate and key paths are required")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("httpapi: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("httpapi: generate serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName(hosts),
			Organization: []string{bootstrapOrg},
		},
		// Backdated an hour so a host whose clock is slightly behind does not
		// reject a certificate that was just issued.
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("httpapi: create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("httpapi: marshal key: %w", err)
	}

	for _, dir := range []string{filepath.Dir(paths.CertFile), filepath.Dir(paths.KeyFile)} {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return fmt.Errorf("httpapi: create certificate directory: %w", err)
		}
		// MkdirAll leaves an existing directory as it was, and the umask can
		// only tighten a new one: set the mode explicitly so a repair
		// re-establishes it.
		if err := os.Chmod(dir, dirMode); err != nil {
			return fmt.Errorf("httpapi: chmod certificate directory: %w", err)
		}
	}

	// os.WriteFile applies its mode only when it creates the file: rewriting an
	// existing key keeps whatever mode it had. A key an operator loosened to
	// 0644 would therefore stay readable after a repair, so the mode is set
	// explicitly after the write as well.
	if err := writeFileMode(paths.KeyFile, pemEncode("EC PRIVATE KEY", keyDER), keyFileMode); err != nil {
		return fmt.Errorf("httpapi: write key: %w", err)
	}
	if err := writeFileMode(paths.CertFile, pemEncode("CERTIFICATE", der), certFileMode); err != nil {
		return fmt.Errorf("httpapi: write certificate: %w", err)
	}
	return nil
}

// writeFileMode writes data to path with exactly mode, on create and on rewrite.
//
// The data goes to a temp file created with mode and is renamed over path, so
// the key is never in a file looser than mode: rewriting an existing 0644 file
// in place would expose the new key until a later chmod. The rename is also
// atomic, so a crash leaves the old pair, not a truncated key.
func writeFileMode(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	// CreateTemp opens 0600; widen to mode (the certificate) only after the
	// content is written, and set it exactly in case of a umask.
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// commonName picks the name for the certificate subject. Browsers ignore the CN
// now and match the SANs, but a subject with something in it is still easier to
// read in `openssl x509` than an empty one.
func commonName(hosts []string) string {
	for _, host := range hosts {
		if host != "" && net.ParseIP(host) == nil {
			return host
		}
	}
	if len(hosts) > 0 && hosts[0] != "" {
		return hosts[0]
	}
	return "server-panel"
}

func pemEncode(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}
