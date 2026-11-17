//go:build unix

package httpapi

import (
	"os"
	"testing"
)

func TestReplaceFileModes(t *testing.T) {
	l, paths := startReloadListener(t)
	certPEM, keyPEM := validCert(t, 31)
	if err := l.Certs().Replace(certPEM, keyPEM, nil); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		paths.KeyFile:               0o600,
		paths.KeyFile + prevSuffix:  0o600,
		paths.CertFile:              0o644,
		paths.CertFile + prevSuffix: 0o644,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}
