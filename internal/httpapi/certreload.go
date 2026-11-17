package httpapi

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// prevSuffix names the one-deep rollback copy of the pair Replace displaced.
const prevSuffix = ".prev"

// CertInfo describes the leaf the listener is serving, for the API that shows
// the operator which certificate is live. It carries no key material.
type CertInfo struct {
	Subject    string    `json:"subject"`
	SANs       []string  `json:"sans"`
	Issuer     string    `json:"issuer"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	Serial     string    `json:"serial"`
	SelfSigned bool      `json:"self_signed"`
}

// CertReloader serves the panel's certificate from memory and swaps it without
// a restart. The listener's tls.Config asks it for a certificate on every
// handshake, so a Reload takes effect on the next new connection while
// connections already established keep the certificate they negotiated.
type CertReloader struct {
	paths CertPaths

	// fileMu serialises Reload and Replace: a Reload that read the cert file
	// between Replace's two renames would otherwise see a mismatched pair.
	fileMu sync.Mutex

	mu   sync.RWMutex
	cert *tls.Certificate
	info CertInfo
}

// NewCertReloader loads the pair at paths. If the pair on disk is unusable but
// the rollback copy from the last Replace is valid, the rollback is restored
// first: that is the state a crash between Replace's two renames leaves.
// Otherwise the error is the same as before this type existed.
func NewCertReloader(paths CertPaths) (*CertReloader, error) {
	r := &CertReloader{paths: paths}
	if err := r.Reload(); err != nil {
		if rerr := r.restorePrev(); rerr != nil {
			return nil, err
		}
		if err := r.Reload(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// GetCertificate is the tls.Config hook.
func (r *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cert, nil
}

// Current reports the certificate being served.
func (r *CertReloader) Current() CertInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info := r.info
	info.SANs = append([]string(nil), info.SANs...)
	return info
}

// Reload re-reads the pair from disk and swaps it in only if it parses as a
// matching pair. On any error the previous certificate keeps serving.
func (r *CertReloader) Reload() error {
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	return r.reloadLocked()
}

func (r *CertReloader) reloadLocked() error {
	cert, err := tls.LoadX509KeyPair(r.paths.CertFile, r.paths.KeyFile)
	if err != nil {
		return fmt.Errorf("httpapi: reload certificate pair: %w", err)
	}
	return r.install(cert)
}

func (r *CertReloader) install(cert tls.Certificate) error {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("httpapi: parse certificate: %w", err)
	}
	cert.Leaf = leaf
	r.mu.Lock()
	r.cert, r.info = &cert, describeLeaf(leaf)
	r.mu.Unlock()
	return nil
}

// Replace validates certPEM and keyPEM, writes them over the panel's pair and
// reloads. It refuses a key that does not match the certificate, and a leaf
// that is expired or not yet valid at now(), before touching the disk.
//
// Crash safety: the pair being displaced is first copied to panel.crt.prev and
// panel.key.prev (key 0600), then the key and the certificate are renamed into
// place one after the other. A crash between the two renames leaves a
// mismatched pair on disk, which NewCertReloader detects at the next start and
// repairs by restoring the rollback copy, so the panel comes back up on the
// old certificate rather than failing to start. The running process is never
// affected: it serves from memory.
func (r *CertReloader) Replace(certPEM, keyPEM []byte, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("httpapi: replace certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("httpapi: replace certificate: parse: %w", err)
	}
	if t := now(); t.Before(leaf.NotBefore) {
		return errors.New("httpapi: replace certificate: not valid yet")
	} else if t.After(leaf.NotAfter) {
		return errors.New("httpapi: replace certificate: expired")
	}

	r.fileMu.Lock()
	defer r.fileMu.Unlock()

	if err := r.savePrev(); err != nil {
		return err
	}
	if err := writeFileMode(r.paths.KeyFile, keyPEM, keyFileMode); err != nil {
		return fmt.Errorf("httpapi: write key: %w", err)
	}
	if err := writeFileMode(r.paths.CertFile, certPEM, certFileMode); err != nil {
		return fmt.Errorf("httpapi: write certificate: %w", err)
	}
	return r.reloadLocked()
}

// savePrev copies the current pair to the .prev names, but only when it is a
// valid pair: overwriting a good rollback with a broken one would destroy the
// only recovery path.
func (r *CertReloader) savePrev() error {
	certPEM, err := os.ReadFile(r.paths.CertFile)
	if err != nil {
		return nil // nothing to keep: first install
	}
	keyPEM, err := os.ReadFile(r.paths.KeyFile)
	if err != nil {
		return nil
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil
	}
	if err := writeFileMode(r.paths.KeyFile+prevSuffix, keyPEM, keyFileMode); err != nil {
		return fmt.Errorf("httpapi: keep previous key: %w", err)
	}
	if err := writeFileMode(r.paths.CertFile+prevSuffix, certPEM, certFileMode); err != nil {
		return fmt.Errorf("httpapi: keep previous certificate: %w", err)
	}
	return nil
}

// restorePrev puts the rollback pair back as the live pair, if it is valid.
func (r *CertReloader) restorePrev() error {
	certPEM, err := os.ReadFile(r.paths.CertFile + prevSuffix)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(r.paths.KeyFile + prevSuffix)
	if err != nil {
		return err
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	if err := writeFileMode(r.paths.KeyFile, keyPEM, keyFileMode); err != nil {
		return err
	}
	return writeFileMode(r.paths.CertFile, certPEM, certFileMode)
}

// describeLeaf marks a leaf self-signed by its own signature, not
// CheckSignatureFrom, which also demands CA bits an uploaded self-signed server
// certificate usually lacks.
func describeLeaf(c *x509.Certificate) CertInfo {
	var sans []string
	sans = append(sans, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		sans = append(sans, ip.String())
	}
	sans = append(sans, c.EmailAddresses...)
	return CertInfo{
		Subject:    c.Subject.CommonName,
		SANs:       sans,
		Issuer:     c.Issuer.CommonName,
		NotBefore:  c.NotBefore,
		NotAfter:   c.NotAfter,
		Serial:     c.SerialNumber.Text(16),
		SelfSigned: bytes.Equal(c.RawIssuer, c.RawSubject) && c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil,
	}
}
