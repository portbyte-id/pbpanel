//go:build unix

package confedit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// File modes and symlinks mean something only on unix; on Windows these
// assertions would pass with the code under test removed (development plan §1.4).

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestAnExistingFileKeepsItsModeAcrossApplyAndRollback(t *testing.T) {
	e := newEnv(t)
	p := e.seed(t, "secret.conf", "old")
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte("new"), Mode: 0o600}}}); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, p); got != 0o640 {
		t.Errorf("mode after apply = %o, want the file's own 640 (File.Mode is for new files only)", got)
	}

	s := e.svc("nginx")
	s.failValidate = "bad"
	if _, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte("bad")}}, Services: []Service{s}}); err == nil {
		t.Fatal("apply succeeded")
	}
	if got := mode(t, p); got != 0o640 || read(t, p) != "new" {
		t.Errorf("after rollback: mode %o content %q, want 640 and the previous content", got, read(t, p))
	}
}

func TestANewFileGetsTheRequestedModeRegardlessOfUmask(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	def, private := e.path("default.conf"), e.path("private.conf")
	_, err := e.ed.Apply(ctx, Change{Files: []File{
		{Path: def, Content: []byte("x")},
		{Path: private, Content: []byte("x"), Mode: 0o600},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if mode(t, def) != 0o644 || mode(t, private) != 0o600 {
		t.Errorf("modes = %o, %o; want 644 and 600", mode(t, def), mode(t, private))
	}
}

func TestTheJournalAndHistoryAreRootOnly(t *testing.T) {
	e := newEnv(t)
	p := e.seed(t, "nginx.conf", "old")
	s := e.svc("nginx")
	var journalMode, blobMode os.FileMode
	s.onValidate = func() {
		entries, _ := os.ReadDir(filepath.Join(e.history, pendingDir))
		if len(entries) != 1 {
			t.Errorf("%d journals during an apply, want 1", len(entries))
			return
		}
		dir := filepath.Join(e.history, pendingDir, entries[0].Name())
		journalMode = mode(t, dir)
		blobMode = mode(t, filepath.Join(dir, "0"))
	}
	if _, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: p, Content: []byte("new")}}, Services: []Service{s}}); err != nil {
		t.Fatal(err)
	}
	if journalMode != 0o700 || blobMode != 0o600 {
		t.Errorf("journal dir %o, blob %o; want 700 and 600", journalMode, blobMode)
	}
	if got := mode(t, e.ed.blobPath(p, 1)); got != 0o600 {
		t.Errorf("history blob mode = %o, want 600", got)
	}
}

func TestASymlinkedTargetIsRefused(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(filepath.Dir(e.root), "outside.conf")
	if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := e.path("link.conf")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: link, Content: []byte("planted")}}})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("error = %v, want the symlink refused", err)
	}
	if read(t, outside) != "untouched" {
		t.Error("the write went through the symlink")
	}
}

func TestASymlinkedParentDirectoryIsRefused(t *testing.T) {
	e := newEnv(t)
	outsideDir := filepath.Join(filepath.Dir(e.root), "elsewhere")
	if err := os.Mkdir(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, e.path("sites")); err != nil {
		t.Fatal(err)
	}
	// Both an existing level and a level the apply would create below it.
	for _, p := range []string{e.path("sites", "a.conf"), e.path("sites", "deeper", "a.conf")} {
		_, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: p, Content: []byte("planted")}}})
		if err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("%s: error = %v, want the escape refused", p, err)
		}
	}
	entries, _ := os.ReadDir(outsideDir)
	if len(entries) != 0 {
		t.Errorf("the write escaped through a symlinked directory: %v", entries)
	}
}

// Ownership can only be given away by root, which is what the daemon runs as.
// CI runs unprivileged and skips this; the smoke run and a root shell cover it.
func TestAnExistingFileKeepsItsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown")
	}
	e := newEnv(t)
	p := e.seed(t, "pool.conf", "old")
	const uid, gid = 1500, 1500
	if err := os.Chown(p, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: p, Content: []byte("new")}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(info); got.uid != uid || got.gid != gid {
		t.Errorf("owner after apply = %d:%d, want %d:%d", got.uid, got.gid, uid, gid)
	}
}
