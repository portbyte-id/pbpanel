//go:build linux

package fm

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

type env struct {
	t     *testing.T
	svc   *Service
	panel string
	work  string // a scratch directory the tests operate in
	now   time.Time
	mu    sync.Mutex
	lim   Limits
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, panel: filepath.Join(root, "panel"), work: filepath.Join(root, "work"), now: time.Now(), lim: DefaultLimits()}
	for _, d := range []string{e.panel, e.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(root, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.svc, err = New(Options{
		Store: st, PanelDir: e.panel,
		Limits: func(context.Context) Limits { e.mu.Lock(); defer e.mu.Unlock(); return e.lim },
		Now:    func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) p(rel ...string) string { return filepath.Join(append([]string{e.work}, rel...)...) }

func (e *env) write(rel, content string, mode os.FileMode) string {
	e.t.Helper()
	full := e.p(rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		e.t.Fatal(err)
	}
	return full
}

func (e *env) read(full string) string {
	e.t.Helper()
	b, err := os.ReadFile(full)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) setLimits(fn func(*Limits)) { e.mu.Lock(); fn(&e.lim); e.mu.Unlock() }

var bg = context.Background()

func TestListAndStat(t *testing.T) {
	e := newEnv(t)
	e.write("b.txt", "bb", 0o640)
	e.write("a.txt", "a", 0o644)
	e.write(".hidden", "h", 0o600)
	e.write("sub/inner", "i", 0o644)
	if err := os.Symlink("sub", e.p("link")); err != nil {
		t.Fatal(err)
	}

	l, err := e.svc.List(e.work, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range l.Entries {
		names = append(names, en.Name)
	}
	if got := strings.Join(names, ","); got != "link,sub,a.txt,b.txt" {
		t.Errorf("order = %s (folders and links to folders first, hidden off)", got)
	}
	l, _ = e.svc.List(e.work, ListOptions{Hidden: true})
	if l.Total != 5 {
		t.Errorf("hidden total = %d, want 5", l.Total)
	}
	l, _ = e.svc.List(e.work, ListOptions{Hidden: true, Offset: 4, Limit: 10})
	if len(l.Entries) != 1 || l.Total != 5 {
		t.Errorf("page = %d entries of %d", len(l.Entries), l.Total)
	}
	l, _ = e.svc.List(e.work, ListOptions{Sort: "size", Desc: true})
	if l.Entries[2].Name != "b.txt" {
		t.Errorf("size desc: %+v", l.Entries)
	}

	en, err := e.svc.Stat(e.p("link"))
	if err != nil || en.Kind != KindSymlink || en.Target != "sub" || en.TargetKind != KindDir {
		t.Errorf("Stat(link) = %+v, %v", en, err)
	}
	if en, _ = e.svc.Stat(e.p("b.txt")); en.Mode != 0o640 || en.Size != 2 || en.Owner == "" {
		t.Errorf("Stat(b.txt) = %+v", en)
	}
	if _, err := e.svc.Stat(e.p("nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat(missing) = %v", err)
	}
	if _, err := e.svc.Disks(); err != nil {
		t.Errorf("Disks: %v", err)
	}
}

func TestCreateRenameCopyMove(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.Mkdir(e.p("d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Mkdir(e.p("d"), 0o755); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate mkdir = %v", err)
	}
	if err := e.svc.Touch(e.p("d", "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Rename(e.p("d", "f"), "g"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Rename(e.p("d", "g"), "a/b"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("rename to a/b = %v", err)
	}
	e.write("d/g", "data", 0o755)
	e.write("d/sub/deep", "deep", 0o600)
	_ = os.Symlink("g", e.p("d", "lnk"))

	if err := e.svc.Mkdir(e.p("dest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Copy(bg, e.p("d"), e.p("dest"), nil); err != nil {
		t.Fatal(err)
	}
	if got := e.read(e.p("dest", "d", "sub", "deep")); got != "deep" {
		t.Errorf("copied content = %q", got)
	}
	if tgt, err := os.Readlink(e.p("dest", "d", "lnk")); err != nil || tgt != "g" {
		t.Errorf("copied symlink = %q, %v", tgt, err)
	}
	if err := e.svc.Copy(bg, e.p("d"), e.p("dest"), nil); !errors.Is(err, ErrExists) {
		t.Errorf("copy onto existing = %v", err)
	}
	if err := e.svc.Copy(bg, e.p("d"), e.p("d", "sub"), nil); !errors.Is(err, ErrIntoItself) {
		t.Errorf("copy into itself = %v", err)
	}
	if err := e.svc.Move(bg, e.p("d"), e.p("d", "sub"), nil); !errors.Is(err, ErrIntoItself) {
		t.Errorf("move into itself = %v", err)
	}
	if err := e.svc.Mkdir(e.p("m"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Move(bg, e.p("d"), e.p("m"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.p("d")); !os.IsNotExist(err) {
		t.Errorf("source survived the move")
	}
	if got := e.read(e.p("m", "d", "g")); got != "data" {
		t.Errorf("moved content = %q", got)
	}
}

// shm is a directory on another filesystem than the test's temp dir, or the
// test is skipped.
func shm(t *testing.T, e *env) string {
	t.Helper()
	// /dev is refused by the policy, so look for another mount we may write to.
	for _, base := range []string{"/run/user/" + strconv.Itoa(os.Getuid()), "/run/lock", "/var/tmp", "/mnt/d"} {
		d, err := os.MkdirTemp(base, "fmtest")
		if err != nil {
			continue
		}
		if e.svc.SameDevice(d, e.work) {
			_ = os.RemoveAll(d)
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(d) })
		return d
	}
	t.Skip("no writable directory on a different filesystem")
	return ""
}

func TestMoveAcrossDevices(t *testing.T) {
	e := newEnv(t)
	other := shm(t, e)
	e.write("src/a", "alpha", 0o751)
	e.write("src/sub/b", "beta", 0o600)
	if err := e.svc.Move(bg, e.p("src"), other, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.p("src")); !os.IsNotExist(err) {
		t.Error("source survived a cross-device move")
	}
	if got := e.read(filepath.Join(other, "src", "sub", "b")); got != "beta" {
		t.Errorf("content = %q", got)
	}
	if st, _ := os.Stat(filepath.Join(other, "src", "a")); st.Mode().Perm() != 0o751 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
}

func TestChmodChown(t *testing.T) {
	e := newEnv(t)
	e.write("t/a", "a", 0o600)
	e.write("t/s/b", "b", 0o600)
	_ = os.Symlink("a", e.p("t", "l"))

	if n, err := e.svc.Chmod(bg, e.p("t", "a"), 0o640, false, "", nil); err != nil || n != 1 {
		t.Fatalf("single chmod = %d, %v", n, err)
	}
	if _, err := e.svc.Chmod(bg, e.p("t"), 0o755, true, "", nil); !errors.Is(err, ErrConfirm) {
		t.Errorf("recursive chmod without confirm = %v", err)
	}
	n, err := e.svc.Chmod(bg, e.p("t"), 0o755, true, "t", nil)
	if err != nil || n != 5 {
		t.Fatalf("recursive chmod = %d, %v", n, err)
	}
	if st, _ := os.Stat(e.p("t", "s", "b")); st.Mode().Perm() != 0o755 {
		t.Errorf("recursive mode = %v", st.Mode().Perm())
	}
	if st, _ := os.Lstat(e.p("t", "l")); st.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced")
	}
	if _, err := e.svc.Chmod(bg, e.p("t", "l"), 0o700, false, "", nil); !errors.Is(err, ErrSymlink) {
		t.Errorf("chmod of a symlink = %v", err)
	}
	// Recursing through a link, even one that points at a protected tree, is refused.
	_ = os.Symlink("/etc", e.p("etc-link"))
	if _, err := e.svc.Chmod(bg, e.p("etc-link"), 0o777, true, "etc-link", nil); err == nil {
		t.Error("recursive chmod through a symlink to /etc succeeded")
	}
	if _, err := e.svc.Chmod(bg, "/etc", 0o777, true, "etc", nil); !errors.Is(err, ErrProtected) {
		t.Errorf("recursive chmod of /etc = %v", err)
	}
	if _, err := e.svc.Chown(bg, e.p("t", "a"), -1, -1, false, "", nil); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("chown with nothing = %v", err)
	}
	uid := os.Getuid()
	if _, err := e.svc.Chown(bg, e.p("t", "a"), uid, os.Getgid(), false, "", nil); err != nil {
		t.Errorf("chown to self: %v", err)
	}
}

// The race test of plan 3.2: a path component is replaced by a symlink to a
// victim after the target was chosen. Acting through the parent descriptor
// means the operation hits the link, never the victim.
func TestSymlinkSwapDoesNotRedirect(t *testing.T) {
	e := newEnv(t)
	victim := e.write("victim", "precious", 0o600)

	for i := 0; i < 200; i++ {
		target := e.write("dir/target", "x", 0o644)
		p, err := e.svc.parentOf(target)
		if err != nil {
			t.Fatal(err)
		}
		// The swap happens after the parent is open and before the operation.
		_ = os.Remove(target)
		if err := os.Symlink(victim, target); err != nil {
			t.Fatal(err)
		}
		if err := chmodAt(p.fd, p.base, 0o777); !errors.Is(err, ErrSymlink) {
			t.Fatalf("chmodAt through a swapped link = %v", err)
		}
		if err := e.svc.removeTree(bg, p.fd, p.base, nil); err != nil {
			t.Fatal(err)
		}
		p.close()
		if _, err := os.Stat(victim); err != nil {
			t.Fatalf("victim deleted by a swapped link: %v", err)
		}
		if st, _ := os.Stat(victim); st.Mode().Perm() != 0o600 {
			t.Fatalf("victim chmodded to %v", st.Mode().Perm())
		}
	}

	// A directory swapped for a symlink mid-walk is refused, not entered.
	e.write("tree/sub/f", "f", 0o600)
	_ = os.MkdirAll(e.p("outside"), 0o755)
	e.write("outside/keep", "keep", 0o600)
	p, _ := e.svc.parentOf(e.p("tree"))
	_ = os.RemoveAll(e.p("tree", "sub"))
	_ = os.Symlink(e.p("outside"), e.p("tree", "sub"))
	if err := e.svc.removeTree(bg, p.fd, p.base, nil); err != nil {
		t.Fatalf("removing tree with a link in it: %v", err)
	}
	p.close()
	if e.read(e.p("outside", "keep")) != "keep" {
		t.Error("recursive delete followed a symlink out of the tree")
	}
}

func TestTrashRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.write("proj/a.txt", "A", 0o640)
	e.write("proj/sub/b.sh", "B", 0o751)
	_ = os.Symlink("a.txt", e.p("proj", "lnk"))
	_ = os.Chtimes(e.p("proj", "a.txt"), time.Unix(1700000000, 0), time.Unix(1700000000, 0))

	if _, err := e.svc.Trash(bg, e.p("proj"), "", nil); !errors.Is(err, ErrConfirm) {
		t.Fatalf("trash dir without confirm = %v", err)
	}
	if _, err := e.svc.Trash(bg, "/etc", "etc", nil); !errors.Is(err, ErrProtected) {
		t.Fatalf("trash /etc = %v", err)
	}
	it, err := e.svc.Trash(bg, e.p("proj"), "proj", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.p("proj")); !os.IsNotExist(err) {
		t.Fatal("original survived the delete")
	}
	items, _ := e.svc.TrashList(bg)
	if len(items) != 1 || items[0].ID != it.ID || !items[0].IsDir || items[0].Size != 2 {
		t.Fatalf("trash list = %+v", items)
	}

	// Restoring over a recreated name is refused; a new name works.
	e.write("proj/other", "o", 0o644)
	if _, err := e.svc.Restore(bg, it.ID, "", nil); !errors.Is(err, ErrExists) {
		t.Fatalf("restore onto an existing path = %v", err)
	}
	got, err := e.svc.Restore(bg, it.ID, "proj-restored", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != e.p("proj-restored") {
		t.Errorf("restored to %s", got)
	}
	st, _ := os.Stat(e.p("proj-restored", "sub", "b.sh"))
	if st.Mode().Perm() != 0o751 {
		t.Errorf("mode after restore = %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(e.p("proj-restored", "a.txt")); st.ModTime().Unix() != 1700000000 || st.Mode().Perm() != 0o640 {
		t.Errorf("file after restore: mtime %v mode %v", st.ModTime(), st.Mode().Perm())
	}
	if tgt, _ := os.Readlink(e.p("proj-restored", "lnk")); tgt != "a.txt" {
		t.Errorf("symlink after restore = %q", tgt)
	}
	if left, _ := e.svc.TrashList(bg); len(left) != 0 {
		t.Errorf("bin not empty after restore: %+v", left)
	}
}

func TestTrashAcrossDevicesPreservesEverything(t *testing.T) {
	e := newEnv(t)
	other := shm(t, e)
	src := filepath.Join(other, "item")
	_ = os.MkdirAll(filepath.Join(src, "d"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "d", "f"), []byte("x"), 0o640)
	_ = os.Chmod(filepath.Join(src, "d", "f"), 0o640)
	if !e.svc.TrashNeedsJob(src) {
		t.Fatal("expected a cross-device trash to need a job")
	}
	it, err := e.svc.Trash(bg, src, "item", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source survived")
	}
	if _, err := e.svc.Restore(bg, it.ID, "", nil); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(src, "d", "f")); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("restored file: %v %v", st, err)
	}
}

func TestTrashPurgeAndLimits(t *testing.T) {
	e := newEnv(t)
	e.setLimits(func(l *Limits) { l.TrashRetentionDays = 7; l.TrashMaxBytes = 100 })
	e.write("old", "12345", 0o644)
	e.write("new", "12345", 0o644)
	if _, err := e.svc.Trash(bg, e.p("old"), "", nil); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.now = e.now.Add(5 * 24 * time.Hour)
	e.mu.Unlock()
	if _, err := e.svc.Trash(bg, e.p("new"), "", nil); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.now = e.now.Add(3 * 24 * time.Hour) // old is now 8 days, new is 3
	e.mu.Unlock()
	n, err := e.svc.Purge(bg)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	items, _ := e.svc.TrashList(bg)
	if len(items) != 1 || items[0].Name != "new" {
		t.Fatalf("after purge: %+v", items)
	}
	if err := e.svc.DeletePermanently(bg, items[0].ID, "wrong", nil); !errors.Is(err, ErrConfirm) {
		t.Errorf("permanent delete without the name = %v", err)
	}
	if err := e.svc.DeletePermanently(bg, items[0].ID, "new", nil); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(e.svc.trashDir); len(ents) != 0 {
		t.Errorf("trash dir not empty: %v", ents)
	}

	// A single item over the size limit is refused with a pointer to permanent delete.
	e.write("big", strings.Repeat("x", 200), 0o644)
	if _, err := e.svc.Trash(bg, e.p("big"), "", nil); !errors.Is(err, ErrTooLarge) {
		t.Errorf("trash over the limit = %v", err)
	}
	// And the oldest is evicted when the total would exceed it.
	e.setLimits(func(l *Limits) { l.TrashMaxBytes = 12 })
	for _, n := range []string{"p1", "p2", "p3"} {
		e.write(n, "123456", 0o644)
		if _, err := e.svc.Trash(bg, e.p(n), "", nil); err != nil {
			t.Fatal(err)
		}
		e.mu.Lock()
		e.now = e.now.Add(time.Minute)
		e.mu.Unlock()
	}
	items, _ = e.svc.TrashList(bg)
	if len(items) != 2 {
		t.Errorf("bin kept %d items, want 2 within 12 bytes", len(items))
	}
}

func TestEditor(t *testing.T) {
	e := newEnv(t)
	full := e.write("conf.txt", "hello\n", 0o640)
	f, err := e.svc.OpenFile(bg, full)
	if err != nil || f.Content != "hello\n" || f.Mode != 0o640 {
		t.Fatalf("open = %+v, %v", f, err)
	}
	base := &Base{Size: f.Size, MTime: f.MTime, Hash: f.Hash}
	saved, err := e.svc.SaveFile(bg, full, "hello world\n", base)
	if err != nil {
		t.Fatal(err)
	}
	if e.read(full) != "hello world\n" {
		t.Error("save did not write")
	}
	if st, _ := os.Stat(full); st.Mode().Perm() != 0o640 {
		t.Errorf("save changed the mode to %v", st.Mode().Perm())
	}
	if ents, _ := os.ReadDir(e.work); len(ents) != 1 {
		t.Errorf("temporary file left behind: %v", ents)
	}

	// Another writer changes the file: the stale save conflicts and writes nothing.
	if err := os.WriteFile(full, []byte("theirs\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SaveFile(bg, full, "mine\n", &Base{Size: saved.Size, MTime: saved.MTime, Hash: saved.Hash})
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Current.Content != "theirs\n" {
		t.Fatalf("stale save = %v", err)
	}
	if e.read(full) != "theirs\n" {
		t.Error("conflicting save modified the file")
	}

	// Binary and oversized files are refused.
	bin := e.p("bin")
	_ = os.WriteFile(bin, []byte("ab\x00cd"), 0o644)
	if _, err := e.svc.OpenFile(bg, bin); !errors.Is(err, ErrBinary) {
		t.Errorf("open binary = %v", err)
	}
	e.setLimits(func(l *Limits) { l.EditorMaxBytes = 4 })
	if _, err := e.svc.OpenFile(bg, full); !errors.Is(err, ErrTooLarge) {
		t.Errorf("open oversize = %v", err)
	}
	if _, err := e.svc.SaveFile(bg, full, "12345", nil); !errors.Is(err, ErrTooLarge) {
		t.Errorf("save oversize = %v", err)
	}
	e.setLimits(func(l *Limits) { l.EditorMaxBytes = 2 << 20 })
	if _, err := e.svc.SaveFile(bg, e.p("new.txt"), "fresh", nil); err != nil {
		t.Errorf("create via save: %v", err)
	}

	// The config trees belong to confedit.
	cfg := filepath.Join(e.panel, "nginx", "nginx.conf")
	_ = os.MkdirAll(filepath.Dir(cfg), 0o755)
	_ = os.WriteFile(cfg, []byte("events {}"), 0o644)
	var re *RedirectError
	if _, err := e.svc.OpenFile(bg, cfg); !errors.As(err, &re) {
		t.Errorf("open under nginx = %v", err)
	}
	if _, err := e.svc.SaveFile(bg, cfg, "x", nil); !errors.As(err, &re) {
		t.Errorf("save under nginx = %v", err)
	}
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestUploadResumeAndChecks(t *testing.T) {
	e := newEnv(t)
	e.setLimits(func(l *Limits) { l.ChunkMaxBytes = 10 })
	data := []byte("0123456789abcdefghij-klmnop") // 27 bytes
	u, err := e.svc.UploadStart(bg, e.work, "out.bin", int64(len(data)), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UploadChunk(bg, u.ID, 0, sum(data[:10]), bytes.NewReader(data[:10])); err != nil {
		t.Fatal(err)
	}
	// Corrupted chunk: rejected, offset unchanged.
	if _, err := e.svc.UploadChunk(bg, u.ID, 10, sum(data[10:20]), strings.NewReader("XXXXXXXXXX")); !errors.Is(err, ErrChecksum) {
		t.Errorf("corrupt chunk = %v", err)
	}
	// Wrong offset reports where the server is.
	var oe *OffsetError
	if _, err := e.svc.UploadChunk(bg, u.ID, 5, sum(data[5:15]), bytes.NewReader(data[5:15])); !errors.As(err, &oe) || oe.Received != 10 {
		t.Errorf("bad offset = %v", err)
	}
	// Oversize chunk and missing checksum.
	if _, err := e.svc.UploadChunk(bg, u.ID, 10, sum(data[10:]), bytes.NewReader(data[10:])); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize chunk = %v", err)
	}
	if _, err := e.svc.UploadChunk(bg, u.ID, 10, "", bytes.NewReader(data[10:20])); !errors.Is(err, ErrChecksum) {
		t.Errorf("missing checksum = %v", err)
	}
	// "Connection killed": a fresh status read tells the client where to resume.
	st, _ := e.svc.UploadStatus(bg, u.ID)
	if st.Received != 10 {
		t.Fatalf("status received = %d", st.Received)
	}
	if _, err := e.svc.UploadComplete(bg, u.ID, ""); !errors.As(err, &oe) {
		t.Errorf("complete before all bytes = %v", err)
	}
	for off := int64(10); off < int64(len(data)); {
		end := min(off+10, int64(len(data)))
		if _, err := e.svc.UploadChunk(bg, u.ID, off, sum(data[off:end]), bytes.NewReader(data[off:end])); err != nil {
			t.Fatal(err)
		}
		off = end
	}
	if _, err := e.svc.UploadComplete(bg, u.ID, "00"+sum(data)[2:]); !errors.Is(err, ErrChecksum) {
		t.Errorf("wrong whole-file checksum = %v", err)
	}
	got, err := e.svc.UploadComplete(bg, u.ID, sum(data))
	if err != nil || got != e.p("out.bin") {
		t.Fatalf("complete = %q, %v", got, err)
	}
	if e.read(e.p("out.bin")) != string(data) {
		t.Error("assembled content differs")
	}
	if ents, _ := os.ReadDir(e.svc.uploadDir); len(ents) != 0 {
		t.Errorf("part file left behind: %v", ents)
	}

	// An existing target needs overwrite.
	if _, err := e.svc.UploadStart(bg, e.work, "out.bin", 1, false); !errors.Is(err, ErrExists) {
		t.Errorf("start over an existing file = %v", err)
	}
	if _, err := e.svc.UploadStart(bg, "/proc", "x", 1, false); !errors.Is(err, ErrProtected) {
		t.Errorf("upload into /proc = %v", err)
	}
	if _, err := e.svc.UploadStart(bg, e.work, "../x", 1, false); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("upload with a path in the name = %v", err)
	}
	if _, err := e.svc.UploadStart(bg, e.work, "huge", 1<<62, false); !errors.Is(err, ErrNoSpace) {
		t.Errorf("upload larger than the disk = %v", err)
	}
}

func TestUploadAcrossDevices(t *testing.T) {
	e := newEnv(t)
	other := shm(t, e)
	data := []byte("cross device payload")
	u, err := e.svc.UploadStart(bg, other, "f", int64(len(data)), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UploadChunk(bg, u.ID, 0, sum(data), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UploadComplete(bg, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	if e.read(filepath.Join(other, "f")) != string(data) {
		t.Error("content differs")
	}
}

// Sweep must never delete an upload whose complete is in flight, and a complete
// that loses to a sweep reports not-found rather than corrupting anything.
func TestUploadSweepVersusComplete(t *testing.T) {
	e := newEnv(t)
	e.setLimits(func(l *Limits) { l.UploadSweepAge = time.Hour })
	data := bytes.Repeat([]byte("z"), 1<<20)
	var ids []string
	for i := 0; i < 20; i++ {
		u, err := e.svc.UploadStart(bg, e.work, "f"+string(rune('a'+i)), int64(len(data)), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.UploadChunk(bg, u.ID, 0, sum(data), bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	e.mu.Lock()
	e.now = e.now.Add(3 * time.Hour) // every upload is now stale
	e.mu.Unlock()

	var wg sync.WaitGroup
	results := make([]error, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = e.svc.UploadComplete(bg, id, "")
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = e.svc.SweepUploads(bg) }()
	wg.Wait()

	for i, err := range results {
		name := e.p("f" + string(rune('a'+i)))
		switch {
		case err == nil:
			if b, rerr := os.ReadFile(name); rerr != nil || !bytes.Equal(b, data) {
				t.Errorf("completed upload %d is damaged: %v", i, rerr)
			}
		case errors.Is(err, ErrNotFound):
			if _, serr := os.Stat(name); serr == nil {
				t.Errorf("upload %d reported not found but the file exists", i)
			}
		default:
			t.Errorf("upload %d: %v", i, err)
		}
	}
	if n, _ := e.svc.SweepUploads(bg); n != 0 {
		t.Errorf("second sweep removed %d", n)
	}
	if ents, _ := os.ReadDir(e.svc.uploadDir); len(ents) != 0 {
		t.Errorf("uploads dir not empty: %v", ents)
	}
}

// --- archives -------------------------------------------------------------

type zipEnt struct {
	name, body string
	mode       os.FileMode
}

func makeZip(t *testing.T, path string, ents ...zipEnt) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, en := range ents {
		h := &zip.FileHeader{Name: en.name, Method: zip.Deflate}
		h.SetMode(en.mode)
		w, _ := zw.CreateHeader(h)
		_, _ = io.WriteString(w, en.body)
	}
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

type tarEnt struct {
	h    tar.Header
	body string
}

func makeTarGz(t *testing.T, path string, ents ...tarEnt) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, en := range ents {
		h := en.h
		if h.Typeflag == 0 || h.Typeflag == tar.TypeReg {
			h.Typeflag, h.Size = tar.TypeReg, int64(len(en.body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(tw, en.body)
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveCreateAndExtractRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.write("src/a.txt", "A", 0o640)
	e.write("src/d/b.txt", "B", 0o755)
	_ = os.Symlink("a.txt", e.p("src", "lnk"))
	for _, format := range []string{FormatZip, FormatTarGz} {
		ext := map[string]string{FormatZip: ".zip", FormatTarGz: ".tar.gz"}[format]
		arc := e.p("out" + ext)
		if err := e.svc.CreateArchive(bg, e.work, []string{"src"}, arc, format, nil); err != nil {
			t.Fatalf("%s create: %v", format, err)
		}
		dest := e.p("x-" + format)
		_ = os.Mkdir(dest, 0o755)
		if err := e.svc.Extract(bg, arc, dest, false, nil); err != nil {
			t.Fatalf("%s extract: %v", format, err)
		}
		if got := e.read(filepath.Join(dest, "src", "d", "b.txt")); got != "B" {
			t.Errorf("%s: content %q", format, got)
		}
		if st, _ := os.Stat(filepath.Join(dest, "src", "d", "b.txt")); st.Mode().Perm() != 0o755 {
			t.Errorf("%s: mode %v", format, st.Mode().Perm())
		}
		if tgt, _ := os.Readlink(filepath.Join(dest, "src", "lnk")); tgt != "a.txt" {
			t.Errorf("%s: symlink %q", format, tgt)
		}
		// A second extract onto the same files is a collision unless overwrite is set.
		if err := e.svc.Extract(bg, arc, dest, false, nil); !errors.Is(err, ErrExists) {
			t.Errorf("%s: re-extract = %v", format, err)
		}
		if err := e.svc.Extract(bg, arc, dest, true, nil); err != nil {
			t.Errorf("%s: re-extract with overwrite: %v", format, err)
		}
	}
	// The archive does not contain itself when it is written inside its source.
	if err := e.svc.CreateArchive(bg, e.work, []string{"src"}, e.p("src", "self.zip"), FormatZip, nil); err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.OpenReader(e.p("src", "self.zip"))
	for _, f := range zr.File {
		if strings.Contains(f.Name, "self.zip") {
			t.Errorf("archive contains itself: %s", f.Name)
		}
	}
	_ = zr.Close()
}

func TestStreamFolder(t *testing.T) {
	e := newEnv(t)
	e.write("src/a.txt", "A", 0o644)
	e.write("src/d/b.txt", "BB", 0o644)
	var buf bytes.Buffer
	if err := e.svc.StreamFolder(bg, &buf, e.p("src"), FormatTarGz); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = string(b)
	}
	if got["src/a.txt"] != "A" || got["src/d/b.txt"] != "BB" || len(got) != 4 {
		t.Errorf("archive contents = %v", got)
	}
}

// The hostile corpus of plan 3.9: every archive is rejected, and nothing is
// written anywhere (inside or outside the target).
func TestExtractRejectsHostileArchives(t *testing.T) {
	e := newEnv(t)
	type tc struct {
		name  string
		build func(path string)
		limit func(*Limits)
	}
	tg := func(ents ...tarEnt) func(string) { return func(p string) { makeTarGz(t, p, ents...) } }
	zp := func(ents ...zipEnt) func(string) { return func(p string) { makeZip(t, p, ents...) } }
	cases := []tc{
		{name: "zip-slip.zip", build: zp(zipEnt{"ok.txt", "ok", 0o644}, zipEnt{"../escape.txt", "x", 0o644})},
		{name: "zip-slip-deep.zip", build: zp(zipEnt{"a/../../escape.txt", "x", 0o644})},
		{name: "absolute.zip", build: zp(zipEnt{"/tmp/fm-abs-escape", "x", 0o644})},
		{name: "zip-symlink-abs.zip", build: zp(zipEnt{"l", "/etc", os.ModeSymlink | 0o777})},
		{name: "zip-symlink-up.zip", build: zp(zipEnt{"l", "../../etc", os.ModeSymlink | 0o777})},
		{name: "zip-through-link.zip", build: zp(zipEnt{"l", "sub", os.ModeSymlink | 0o777}, zipEnt{"l/f", "x", 0o644})},
		{name: "slip.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "../escape.txt"}, body: "x"})},
		{name: "abs.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "/tmp/fm-abs-escape"}, body: "x"})},
		{name: "symlink-out.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "../../.."}})},
		{name: "symlink-abs.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc"}})},
		{name: "symlink-then-write.tar.gz", build: tg(
			tarEnt{h: tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "."}},
			tarEnt{h: tar.Header{Name: "l/pwn"}, body: "x"})},
		{name: "hardlink-out.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}})},
		{name: "hardlink-up.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "../victim"}})},
		{name: "device.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}})},
		{name: "fifo.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}})},
		{name: "bomb.zip", build: zp(zipEnt{"zeros", strings.Repeat("\x00", 4<<20), 0o644}), limit: func(l *Limits) { l.ArchiveMaxBytes = 1 << 20 }},
		{name: "bomb.tar.gz", build: tg(tarEnt{h: tar.Header{Name: "zeros"}, body: strings.Repeat("\x00", 4<<20)}), limit: func(l *Limits) { l.ArchiveMaxBytes = 1 << 20 }},
		{name: "many.tar.gz", build: tg(
			tarEnt{h: tar.Header{Name: "1"}}, tarEnt{h: tar.Header{Name: "2"}}, tarEnt{h: tar.Header{Name: "3"}}),
			limit: func(l *Limits) { l.ArchiveMaxEntries = 2 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e.setLimits(func(l *Limits) { *l = DefaultLimits() })
			if c.limit != nil {
				e.setLimits(c.limit)
			}
			sandbox := t.TempDir()
			dest := filepath.Join(sandbox, "dest")
			_ = os.MkdirAll(filepath.Join(dest, "sub"), 0o755)
			victim := filepath.Join(sandbox, "victim")
			_ = os.WriteFile(victim, []byte("v"), 0o644)
			arc := filepath.Join(sandbox, c.name)
			c.build(arc)

			err := e.svc.Extract(bg, arc, dest, true, nil)
			if !errors.Is(err, ErrArchive) {
				t.Fatalf("Extract = %v, want ErrArchive", err)
			}
			// Nothing written: dest holds only its preexisting sub, and the
			// sandbox only dest, the archive and the victim.
			var found []string
			_ = filepath.WalkDir(sandbox, func(p string, d os.DirEntry, _ error) error {
				found = append(found, strings.TrimPrefix(p, sandbox))
				return nil
			})
			want := map[string]bool{"": true, "/dest": true, "/dest/sub": true, "/victim": true, "/" + c.name: true}
			for _, f := range found {
				if !want[f] {
					t.Errorf("hostile archive left %q behind", f)
				}
			}
			if _, err := os.Stat("/tmp/fm-abs-escape"); err == nil {
				t.Error("absolute-path entry escaped to /tmp")
				_ = os.Remove("/tmp/fm-abs-escape")
			}
			if b, _ := os.ReadFile(victim); string(b) != "v" {
				t.Error("victim file changed")
			}
		})
	}
}

func TestExtractRefusesProtectedTargetAndBadFormat(t *testing.T) {
	e := newEnv(t)
	makeZip(t, e.p("a.zip"), zipEnt{"f", "x", 0o644})
	if err := e.svc.Extract(bg, e.p("a.zip"), "/proc", false, nil); err == nil {
		t.Error("extract into /proc succeeded")
	}
	e.write("a.rar", "x", 0o644)
	if err := e.svc.Extract(bg, e.p("a.rar"), e.work, false, nil); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("extract of .rar = %v", err)
	}
}

func TestDownloadRefusesNonRegular(t *testing.T) {
	e := newEnv(t)
	e.write("f", "data", 0o644)
	f, info, err := e.svc.OpenDownload(e.p("f"))
	if err != nil || info.Size() != 4 {
		t.Fatalf("OpenDownload = %v, %v", info, err)
	}
	_ = f.Close()
	if _, _, err := e.svc.OpenDownload(e.work); !errors.Is(err, ErrIsDir) {
		t.Errorf("download of a directory = %v", err)
	}
	if _, _, err := e.svc.OpenDownload("/dev/null"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("download of /dev/null = %v", err)
	}
}

func TestProtectedOperationsRefused(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Trash(bg, "/", "", nil); !errors.Is(err, ErrProtected) {
		t.Errorf("trash / = %v", err)
	}
	if err := e.svc.Move(bg, "/etc", e.work, nil); !errors.Is(err, ErrProtected) {
		t.Errorf("move /etc = %v", err)
	}
	if err := e.svc.Rename("/usr", "usr2"); !errors.Is(err, ErrProtected) {
		t.Errorf("rename /usr = %v", err)
	}
	if err := e.svc.Mkdir("/proc/x", 0o755); !errors.Is(err, ErrProtected) {
		t.Errorf("mkdir under /proc = %v", err)
	}
	if err := e.svc.Touch("/sys/x", 0o644); !errors.Is(err, ErrProtected) {
		t.Errorf("touch under /sys = %v", err)
	}
	// A link to /etc is deleted as a link; /etc is untouched.
	_ = os.Symlink("/etc", e.p("etc-link"))
	if _, err := e.svc.Trash(bg, e.p("etc-link"), "", nil); err != nil {
		t.Errorf("trash a link to /etc: %v", err)
	}
	if _, err := os.Stat("/etc/hostname"); err != nil && !os.IsNotExist(err) {
		t.Errorf("/etc damaged: %v", err)
	}
	// Direct access to the bin and upload areas is not allowed.
	if _, err := e.svc.Trash(bg, e.svc.trashDir, "trash", nil); !errors.Is(err, ErrProtected) {
		t.Errorf("trash the trash = %v", err)
	}
}
