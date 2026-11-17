package tls

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSiteFilesWritesAndReplaces(t *testing.T) {
	root := t.TempDir()
	if err := WriteSiteFiles(root, "example.com", []byte("chain-1"), []byte("key-1")); err != nil {
		t.Fatalf("WriteSiteFiles: %v", err)
	}
	certFile, keyFile := SitePaths(root, "example.com")
	if got, _ := os.ReadFile(filepath.FromSlash(keyFile)); string(got) != "key-1" {
		t.Fatalf("key = %q", got)
	}
	if err := WriteSiteFiles(root, "example.com", []byte("chain-2"), []byte("key-2")); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got, _ := os.ReadFile(filepath.FromSlash(certFile)); string(got) != "chain-2" {
		t.Fatalf("certificate = %q after a rewrite", got)
	}

	entries, _ := os.ReadDir(filepath.FromSlash(SiteDir(root, "example.com")))
	if len(entries) != 2 {
		t.Fatalf("site dir holds %d entries, want 2 (no temp files left)", len(entries))
	}
}

func TestWriteSiteFilesRefusesAnUnsafeDomain(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"", "..", "../evil", "a/b"} {
		if err := WriteSiteFiles(root, d, []byte("c"), []byte("k")); err == nil {
			t.Errorf("WriteSiteFiles accepted %q", d)
		}
		if err := RemoveSiteFiles(root, d); err == nil {
			t.Errorf("RemoveSiteFiles accepted %q", d)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("a refused write left %d entries in the root", len(entries))
	}
}

func TestRemoveSiteFiles(t *testing.T) {
	root := t.TempDir()
	if err := RemoveSiteFiles(root, "never.example"); err != nil {
		t.Fatalf("removing nothing: %v", err)
	}
	_ = WriteSiteFiles(root, "example.com", []byte("c"), []byte("k"))
	if err := RemoveSiteFiles(root, "example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.FromSlash(SiteDir(root, "example.com"))); !os.IsNotExist(err) {
		t.Fatalf("directory still present: %v", err)
	}
}

func TestEnsureACMEWebroot(t *testing.T) {
	root := t.TempDir()
	if err := EnsureACMEWebroot(root); err != nil {
		t.Fatal(err)
	}
	if err := EnsureACMEWebroot(root); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if fi, err := os.Stat(filepath.FromSlash(ACMEWebroot(root))); err != nil || !fi.IsDir() {
		t.Fatalf("webroot: %v", err)
	}
}
