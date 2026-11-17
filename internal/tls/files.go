package tls

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// KeyFileMode is the private key's mode.
	KeyFileMode os.FileMode = 0o600
	// CertFileMode is the certificate's mode. A certificate is public.
	CertFileMode os.FileMode = 0o644
	// siteDirMode keeps other accounts out of a domain's directory. nginx's
	// master process reads the files as root, so nothing else needs entry.
	siteDirMode os.FileMode = 0o700
	// sharedDirMode is for the directories nginx workers must traverse to reach
	// the ACME webroot.
	sharedDirMode os.FileMode = 0o755
)

// WriteSiteFiles writes a domain's fullchain.pem and privkey.pem.
//
// Each file is written to a temp file and renamed, so a reader never sees a
// truncated file and the key is never briefly world-readable. The key goes
// first: if the process dies between the two renames the pair is mismatched,
// which `nginx -t` rejects and confedit rolls back, instead of nginx loading a
// new certificate whose key it does not have.
func WriteSiteFiles(root, domain string, fullchainPEM, keyPEM []byte) error {
	if err := CheckDomain(domain); err != nil {
		return err
	}
	if err := ensureDir(SSLDir(root), sharedDirMode); err != nil {
		return fmt.Errorf("tls: create ssl dir: %w", err)
	}
	if err := ensureDir(SiteDir(root, ""), siteDirMode); err != nil {
		return fmt.Errorf("tls: create sites dir: %w", err)
	}
	if err := ensureDir(SiteDir(root, domain), siteDirMode); err != nil {
		return fmt.Errorf("tls: create site dir: %w", err)
	}
	certFile, keyFile := SitePaths(root, domain)
	if err := writeFileMode(keyFile, keyPEM, KeyFileMode); err != nil {
		return fmt.Errorf("tls: write key: %w", err)
	}
	if err := writeFileMode(certFile, fullchainPEM, CertFileMode); err != nil {
		return fmt.Errorf("tls: write certificate: %w", err)
	}
	return nil
}

// RemoveSiteFiles deletes a domain's directory. A directory that is already gone
// is not an error: the caller wants it absent.
func RemoveSiteFiles(root, domain string) error {
	if err := CheckDomain(domain); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.FromSlash(SiteDir(root, domain))); err != nil {
		return fmt.Errorf("tls: remove site files: %w", err)
	}
	return nil
}

// EnsureACMEWebroot creates the challenge directory. Its parents are
// traversable and it is readable by nginx's workers; it holds only tokens.
func EnsureACMEWebroot(root string) error {
	if err := ensureDir(SSLDir(root), sharedDirMode); err != nil {
		return fmt.Errorf("tls: create ssl dir: %w", err)
	}
	if err := ensureDir(ACMEWebroot(root), sharedDirMode); err != nil {
		return fmt.Errorf("tls: create acme webroot: %w", err)
	}
	return nil
}

// ensureDir creates dir and every missing parent, then sets dir's mode exactly:
// MkdirAll applies the umask, and a directory created by an older run may be looser.
func ensureDir(dir string, mode os.FileMode) error {
	dir = filepath.FromSlash(dir)
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(dir, mode)
}

// writeFileMode writes data to path with exactly mode. It is the same pattern as
// the httpapi listener certificate writer, copied rather than imported so this
// package does not depend on the HTTP layer.
func writeFileMode(path string, data []byte, mode os.FileMode) (err error) {
	path = filepath.FromSlash(path)
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
	// CreateTemp opens 0600; widen only after the content is written.
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
