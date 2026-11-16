//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// TestEuidIsTheRealSyscall pins the seam: the injected check must default to
// os.Geteuid, or every non-root refusal below would be theatre.
func TestEuidIsTheRealSyscall(t *testing.T) {
	if got, want := euid(), os.Geteuid(); got != want {
		t.Fatalf("euid() = %d, want os.Geteuid() = %d", got, want)
	}
	if got, want := euid(), os.Getuid(); got != want {
		// The binary is never set-uid, so real and effective ids are equal.
		t.Fatalf("euid() = %d, want os.Getuid() = %d", got, want)
	}
}

// TestRootRefusalOnThisHost runs the real dispatch with the real effective uid.
// Under CI (non-root) it must refuse a root command and still serve --version;
// as root it must do neither. It asserts whichever half applies.
func TestRootRefusalOnThisHost(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(context.Background(), []string{"info"}, &stdout, &stderr)
	if os.Geteuid() == 0 {
		if strings.Contains(stderr.String(), "must run as root") {
			t.Fatalf("running as root, but info was refused: %q", stderr.String())
		}
		return
	}
	if code != exitError {
		t.Fatalf("info as uid %d: exit code = %d, want %d", os.Geteuid(), code, exitError)
	}
	if !strings.Contains(stderr.String(), "must run as root") {
		t.Fatalf("info as uid %d: stderr = %q, want the root refusal", os.Geteuid(), stderr.String())
	}
	if strings.Contains(stderr.String(), "not implemented") {
		t.Fatalf("info ran its body as a non-root user: %q", stderr.String())
	}

	// --version must work for any user, including this one.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"--version"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--version as uid %d: exit code = %d, want 0 (stderr: %q)", os.Geteuid(), code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatalf("--version printed nothing")
	}

	// --help must work for any user too.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--help as uid %d: exit code = %d, want 0 (stderr: %q)", os.Geteuid(), code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "panel set <key> <value>") {
		t.Fatalf("--help did not list the set command: %q", stdout.String())
	}
}
