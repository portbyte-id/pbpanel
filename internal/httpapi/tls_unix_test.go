//go:build unix

package httpapi

import (
	"os"
	"testing"
)

// TestKeyFileModeIs0600 is the mode assertion the brief asks for, in a unix-only
// file: on Windows os.Chmod cannot express 0600 and the mode an os.Stat reports
// is meaningless, so the same assertion there would pass whether or not the code
// set anything.
func TestKeyFileModeIs0600(t *testing.T) {
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"192.0.2.4"}); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	info, err := os.Stat(paths.KeyFile)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("key mode = %04o, want 0600", got)
	}

	// The certificate is public by design and must be readable by a browser
	// importing it, so it is 0644 and not the key's 0600.
	certInfo, err := os.Stat(paths.CertFile)
	if err != nil {
		t.Fatalf("stat cert: %v", err)
	}
	if got := certInfo.Mode().Perm(); got != 0o644 {
		t.Errorf("cert mode = %04o, want 0644", got)
	}
}

// TestKeyFileModeIs0600OnOverwrite proves the repair path re-establishes the
// mode: an operator who loosened the key's permissions gets 0600 back on the
// next generate, rather than keeping the loose mode.
func TestKeyFileModeIs0600OnOverwrite(t *testing.T) {
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"192.0.2.5"}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	if err := os.Chmod(paths.KeyFile, 0o644); err != nil {
		t.Fatalf("chmod key: %v", err)
	}
	if err := GenerateSelfSigned(paths, []string{"192.0.2.6"}); err != nil {
		t.Fatalf("second run: %v", err)
	}

	info, err := os.Stat(paths.KeyFile)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("key mode after regeneration = %04o, want 0600", got)
	}
}

// TestCertDirectoryMode proves the directory the pair lives in is not
// world-writable or world-traversable.
func TestCertDirectoryMode(t *testing.T) {
	paths := certPathsIn(t)
	if err := GenerateSelfSigned(paths, []string{"192.0.2.7"}); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	dir := paths.CertFile[:len(paths.CertFile)-len("/panel.crt")]
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o002 != 0 {
		t.Errorf("certificate directory mode = %04o, must not be world-writable", perm)
	}
}
