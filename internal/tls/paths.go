package tls

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The files below are read by nginx on Linux, so, like nginx.Paths, these are
// built with package path and never filepath: a daemon built on Windows must
// still produce `/www/panel/ssl/...`.

// SSLDir is the directory that holds every certificate file and the ACME webroot.
func SSLDir(root string) string { return path.Join(root, "panel", "ssl") }

// ACMEWebroot is the directory nginx serves `/.well-known/acme-challenge/` from.
func ACMEWebroot(root string) string { return path.Join(SSLDir(root), "acme-webroot") }

// SiteDir is the directory of one domain's certificate and key. The domain must
// have passed CheckDomain; SiteDir does not defend against `..`.
func SiteDir(root, domain string) string { return path.Join(SSLDir(root), "sites", domain) }

// SitePaths returns the certificate (leaf plus chain) and private key paths for
// a domain, in the form nginx's ssl_certificate and ssl_certificate_key take.
func SitePaths(root, domain string) (certFile, keyFile string) {
	dir := SiteDir(root, domain)
	return path.Join(dir, "fullchain.pem"), path.Join(dir, "privkey.pem")
}

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrBadDomain is returned for a domain that cannot name a certificate directory.
var ErrBadDomain = errors.New("tls: invalid domain")

// CheckDomain accepts a lowercase ASCII (punycode) host name, which is what the
// sites table stores. It exists because the domain becomes a directory name:
// anything with a slash, a dot-dot or a NUL must never reach the file system.
func CheckDomain(domain string) error {
	if domain == "" || len(domain) > 253 {
		return fmt.Errorf("%w: %q", ErrBadDomain, domain)
	}
	for _, label := range strings.Split(domain, ".") {
		if !labelRe.MatchString(label) {
			return fmt.Errorf("%w: %q", ErrBadDomain, domain)
		}
	}
	return nil
}
