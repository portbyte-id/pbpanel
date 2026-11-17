//go:build unix

package tls

import (
	"os"
	"path/filepath"
	"testing"
)

// Windows os.Chmod only toggles the read-only bit, so these assertions would
// pass there with the fix removed; they run on Unix only.
func TestSiteFileModes(t *testing.T) {
	root := t.TempDir()
	if err := WriteSiteFiles(root, "example.com", []byte("c"), []byte("k")); err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := SitePaths(root, "example.com")
	for path, want := range map[string]os.FileMode{
		keyFile:                      0o600,
		certFile:                     0o644,
		SiteDir(root, "example.com"): 0o700,
		SiteDir(root, ""):            0o700,
	} {
		fi, err := os.Stat(filepath.FromSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

// An existing loose key must be tightened by the next write, not keep its mode.
func TestWriteSiteFilesTightensALooseKey(t *testing.T) {
	root := t.TempDir()
	_ = WriteSiteFiles(root, "example.com", []byte("c"), []byte("k"))
	_, keyFile := SitePaths(root, "example.com")
	if err := os.Chmod(filepath.FromSlash(keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSiteFiles(root, "example.com", []byte("c2"), []byte("k2")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.FromSlash(keyFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o after a rewrite", fi.Mode().Perm())
	}
}

func TestACMEWebrootIsReadableByNginx(t *testing.T) {
	root := t.TempDir()
	if err := EnsureACMEWebroot(root); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{SSLDir(root), ACMEWebroot(root)} {
		if fi, _ := os.Stat(filepath.FromSlash(p)); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %o, want 755", p, fi.Mode().Perm())
		}
	}
}
