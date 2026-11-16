package confedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portbyte-id/server-panel/internal/store"
)

// fakeService records the calls an apply makes and fails where it is told to.
type fakeService struct {
	name string
	log  *callLog

	failValidate string // validator output; non-empty makes Validate fail
	failReload   bool
	// failReloadOnce fails only the first Reload, so the rollback's reload works.
	failReloadOnce bool
	failHealth     bool

	// onValidate runs inside Validate, with the new files on disk.
	onValidate func()
	reloads    atomic.Int32
}

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *callLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.calls, " ")
}

func (s *fakeService) Name() string { return s.name }

func (s *fakeService) Validate(context.Context) (string, error) {
	s.log.add("validate:" + s.name)
	if s.onValidate != nil {
		s.onValidate()
	}
	if s.failValidate != "" {
		return s.failValidate, errors.New("configuration test failed")
	}
	return "", nil
}

func (s *fakeService) Reload(context.Context) error {
	s.log.add("reload:" + s.name)
	n := s.reloads.Add(1)
	if s.failReload || (s.failReloadOnce && n == 1) {
		return errors.New("reload signal failed")
	}
	return nil
}

func (s *fakeService) Healthy(context.Context) error {
	s.log.add("health:" + s.name)
	if s.failHealth {
		return errors.New("container is not running")
	}
	return nil
}

type env struct {
	ed      *Editor
	root    string // managed root
	history string
	log     *callLog
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "nginx")
	history := filepath.Join(base, "history")
	for _, d := range []string{root, history} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(base, "panel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ed, err := New(Options{Roots: []string{root}, HistoryDir: history, Store: st})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &env{ed: ed, root: root, history: history, log: &callLog{}}
}

func (e *env) svc(name string) *fakeService { return &fakeService{name: name, log: e.log} }

func (e *env) path(parts ...string) string {
	return filepath.Join(append([]string{e.root}, parts...)...)
}

func (e *env) seed(t *testing.T, rel, content string) string {
	t.Helper()
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// pendingJournals counts journals left on disk. After any apply that returned,
// successful or rolled back, it must be zero.
func (e *env) pendingJournals(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.history, pendingDir))
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func applyErr(t *testing.T, err error) *ApplyError {
	t.Helper()
	var ae *ApplyError
	if !errors.As(err, &ae) {
		t.Fatalf("error = %v (%T), want *ApplyError", err, err)
	}
	return ae
}

func TestApplyWritesValidatesReloadsAndChecksHealthInOrder(t *testing.T) {
	e := newEnv(t)
	existing := e.seed(t, "nginx.conf", "old")
	created := e.path("includes", "a.test", "listen.conf")

	fpm, nginx := e.svc("php-8.3"), e.svc("nginx")
	// By the time a validator runs, every file of the change is on disk: it
	// validates the change as a whole, not a half of it.
	fpm.onValidate = func() {
		if read(t, existing) != "new" || read(t, created) != "listen 80;" {
			t.Error("the validator ran before all files were written")
		}
	}
	res, err := e.ed.Apply(context.Background(), Change{
		Files:    []File{{Path: existing, Content: []byte("new")}, {Path: created, Content: []byte("listen 80;")}},
		Services: []Service{fpm, nginx},
		Reason:   "test",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Each pass covers every service before the next pass starts, in the order
	// the change lists them.
	want := "validate:php-8.3 validate:nginx reload:php-8.3 reload:nginx health:php-8.3 health:nginx"
	if got := e.log.String(); got != want {
		t.Errorf("calls = %q\nwant    %q", got, want)
	}
	if read(t, existing) != "new" || read(t, created) != "listen 80;" {
		t.Error("files do not hold the new content")
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings: %q", res.Warnings)
	}
	if n := e.pendingJournals(t); n != 0 {
		t.Errorf("%d journal(s) left after a successful apply", n)
	}
	// The pre-existing file has its old content as v1 and the new as v2; the
	// created file has only the new content.
	if res.Versions[existing] != 2 || res.Versions[created] != 1 {
		t.Errorf("versions = %v, want existing:2 created:1", res.Versions)
	}
	old, err := e.ed.ReadVersion(context.Background(), existing, 1)
	if err != nil || string(old) != "old" {
		t.Errorf("version 1 of the existing file = %q, %v; want the content found on disk", old, err)
	}
}

// TestFailureAtEveryStageRestoresEveryFile is the pipeline's contract: whatever
// fails, every file is back to the bytes it had, files the apply created are
// gone, and so are the directories it created.
func TestFailureAtEveryStageRestoresEveryFile(t *testing.T) {
	tests := []struct {
		name        string
		arm         func(e *env, a, b *fakeService)
		wantStage   Stage
		wantService string
		wantOutput  string
		wantCalls   string
	}{
		{
			name: "second write fails",
			arm: func(e *env, _, _ *fakeService) {
				real, n := e.ed.write, 0
				e.ed.write = func(p string, d []byte, m fs.FileMode, o owner) error {
					// Journal blobs are written through writeAtomic directly;
					// this counts managed files only. The first two calls are
					// the apply's own writes, later ones are the rollback.
					if n++; n == 2 {
						return errors.New("disk full")
					}
					return real(p, d, m, o)
				}
			},
			wantStage: StageWrite,
			wantCalls: "",
		},
		{
			name: "first validator rejects",
			arm: func(_ *env, a, _ *fakeService) {
				a.failValidate = "nginx: [emerg] unknown directive \"bogus\" in /x:1\n"
			},
			wantStage:   StageValidate,
			wantService: "a",
			wantOutput:  "nginx: [emerg] unknown directive \"bogus\" in /x:1\n",
			// Nothing was reloaded, so nothing is reloaded back.
			wantCalls: "validate:a",
		},
		{
			name:        "second validator rejects",
			arm:         func(_ *env, _, b *fakeService) { b.failValidate = "bad pool" },
			wantStage:   StageValidate,
			wantService: "b",
			wantOutput:  "bad pool",
			wantCalls:   "validate:a validate:b",
		},
		{
			name:        "second reload fails",
			arm:         func(_ *env, _, b *fakeService) { b.failReloadOnce = true },
			wantStage:   StageReload,
			wantService: "b",
			// Both services had been told to reload, so both are reloaded
			// again onto the restored files.
			wantCalls: "validate:a validate:b reload:a reload:b reload:a reload:b",
		},
		{
			name:        "health check fails",
			arm:         func(_ *env, a, _ *fakeService) { a.failHealth = true },
			wantStage:   StageHealth,
			wantService: "a",
			wantCalls:   "validate:a validate:b reload:a reload:b health:a reload:a reload:b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			// Content with bytes a text-mode round trip would mangle.
			original := "server {\r\n\tlisten 80;\x00\xff\n}"
			changed := e.seed(t, "sites/a.test.conf", original)
			removed := e.seed(t, "sites/b.test.conf", "to be deleted")
			created := e.path("includes", "a.test", "proxy.conf")

			a, b := e.svc("a"), e.svc("b")
			tc.arm(e, a, b)
			_, err := e.ed.Apply(context.Background(), Change{
				Files: []File{
					{Path: changed, Content: []byte("broken")},
					{Path: created, Content: []byte("proxy_pass http://127.0.0.1:1;")},
					{Path: removed, Delete: true},
				},
				Services: []Service{a, b},
				Reason:   "test",
			})
			ae := applyErr(t, err)
			if ae.Stage != tc.wantStage || ae.Service != tc.wantService || ae.Output != tc.wantOutput {
				t.Errorf("error = stage %q service %q output %q\nwant    stage %q service %q output %q",
					ae.Stage, ae.Service, ae.Output, tc.wantStage, tc.wantService, tc.wantOutput)
			}
			if ae.RollbackErr != nil {
				t.Errorf("rollback failed: %v", ae.RollbackErr)
			}
			if got := e.log.String(); got != tc.wantCalls {
				t.Errorf("calls = %q\nwant    %q", got, tc.wantCalls)
			}

			if got := read(t, changed); got != original {
				t.Errorf("changed file = %q, want the original bytes %q", got, original)
			}
			if got := read(t, removed); got != "to be deleted" {
				t.Errorf("deleted file = %q, want it back", got)
			}
			if exists(created) || exists(filepath.Dir(created)) || exists(e.path("includes")) {
				t.Error("a file or directory the apply created is still there")
			}
			if n := e.pendingJournals(t); n != 0 {
				t.Errorf("%d journal(s) left after a clean rollback", n)
			}
			// A change that never went live leaves no history.
			if h, _ := e.ed.History(context.Background(), changed); len(h) != 0 {
				t.Errorf("history has %d entries for a rolled-back apply", len(h))
			}
			// No temporary file is left next to the config.
			entries, _ := os.ReadDir(filepath.Dir(changed))
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".tmp") {
					t.Errorf("temporary file left behind: %s", entry.Name())
				}
			}
		})
	}
}

func TestValidatorOutputIsInTheErrorText(t *testing.T) {
	e := newEnv(t)
	p := e.seed(t, "nginx.conf", "ok")
	s := e.svc("nginx")
	s.failValidate = "nginx: [emerg] \"worker_processes\" directive is duplicate"
	_, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: p, Content: []byte("x")}}, Services: []Service{s}})
	for _, want := range []string{"validate nginx", "directive is duplicate", "previous files restored"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestFailedRollbackKeepsTheJournalAndRecoverFinishesIt(t *testing.T) {
	e := newEnv(t)
	changed := e.seed(t, "nginx.conf", "good")
	created := e.path("sites", "new.conf")

	s := e.svc("nginx")
	s.failValidate = "bad"
	// Every write after the apply's own two fails: the rollback cannot restore.
	real, n := e.ed.write, 0
	e.ed.write = func(p string, d []byte, m fs.FileMode, o owner) error {
		if n++; n > 2 {
			return errors.New("read-only file system")
		}
		return real(p, d, m, o)
	}
	_, err := e.ed.Apply(context.Background(), Change{
		Files:    []File{{Path: changed, Content: []byte("bad")}, {Path: created, Content: []byte("new")}},
		Services: []Service{s},
	})
	ae := applyErr(t, err)
	if ae.RollbackErr == nil || !strings.Contains(err.Error(), "rollback FAILED") {
		t.Fatalf("error = %v, want a reported rollback failure", err)
	}
	if read(t, changed) != "bad" {
		t.Fatal("test setup: the unvalidated content should still be on disk")
	}
	if e.pendingJournals(t) != 1 {
		t.Fatal("the journal was removed although the rollback failed")
	}

	// The daemon restarts: a fresh editor on the same directories.
	e.ed.write = real
	restored, err := e.ed.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(restored) != 2 {
		t.Errorf("restored = %q, want both files", restored)
	}
	if read(t, changed) != "good" {
		t.Errorf("after Recover the file holds %q, want the validated content", read(t, changed))
	}
	if exists(created) || exists(filepath.Dir(created)) {
		t.Error("after Recover a file or directory from the unfinished apply remains")
	}
	if e.pendingJournals(t) != 0 {
		t.Error("Recover left the journal behind")
	}
	if again, err := e.ed.Recover(); err != nil || len(again) != 0 {
		t.Errorf("second Recover = %q, %v; want nothing to do", again, err)
	}
}

func TestRecoverDiscardsAJournalWithoutAManifest(t *testing.T) {
	e := newEnv(t)
	p := e.seed(t, "nginx.conf", "live")
	// An apply that died while journaling, before touching any managed file.
	dir := filepath.Join(e.history, pendingDir, "20261117T000000-dead")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := e.ed.Recover()
	if err != nil || len(restored) != 0 {
		t.Fatalf("Recover = %q, %v", restored, err)
	}
	if read(t, p) != "live" || exists(dir) {
		t.Error("an incomplete journal was applied or left behind")
	}
}

func TestRecoverRefusesAJournalPathOutsideTheRoots(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(filepath.Dir(e.root), "outside.conf")
	if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ed.writeJournal([]snapshot{{path: outside, existed: true, content: []byte("planted"), mode: 0o644}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ed.Recover(); err == nil {
		t.Error("Recover accepted a journal naming a path outside the managed roots")
	}
	if read(t, outside) != "untouched" {
		t.Error("Recover wrote outside the managed roots")
	}
}

func TestApplyRefusesBadChanges(t *testing.T) {
	e := newEnv(t)
	inside := e.path("nginx.conf")
	dir := e.path("sites")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := e.svc("nginx")
	sep := string(filepath.Separator)
	tests := []struct {
		name string
		ch   Change
		want string
	}{
		{"no files", Change{}, "no files"},
		{"outside every root", Change{Files: []File{{Path: filepath.Join(filepath.Dir(e.root), "passwd")}}}, "outside the managed"},
		{"the root itself", Change{Files: []File{{Path: e.root}}}, "outside the managed"},
		{"sibling sharing the root as a prefix", Change{Files: []File{{Path: e.root + "x" + sep + "a"}}}, "outside the managed"},
		{"dot-dot", Change{Files: []File{{Path: e.root + sep + ".." + sep + "x"}}}, "clean absolute path"},
		{"relative", Change{Files: []File{{Path: "nginx.conf"}}}, "clean absolute path"},
		{"inside the history directory", Change{Files: []File{{Path: filepath.Join(e.history, "x")}}}, "outside the managed"},
		{"listed twice", Change{Files: []File{{Path: inside}, {Path: inside}}}, "listed twice"},
		{"too large", Change{Files: []File{{Path: inside, Content: make([]byte, MaxFileBytes+1)}}}, "byte limit"},
		{"delete with content", Change{Files: []File{{Path: inside, Delete: true, Content: []byte("x")}}}, "both deleted and written"},
		{"target is a directory", Change{Files: []File{{Path: dir, Content: []byte("x")}}}, "not a regular file"},
		{"service listed twice", Change{Files: []File{{Path: inside}}, Services: []Service{s, s}}, "listed twice"},
		{"nil service", Change{Files: []File{{Path: inside}}, Services: []Service{nil}}, "nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.ed.Apply(context.Background(), tc.ch)
			ae := applyErr(t, err)
			if ae.Stage != StagePrepare || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want a prepare error containing %q", err, tc.want)
			}
			if e.log.String() != "" {
				t.Errorf("a refused change reached a service: %s", e.log)
			}
			if exists(inside) {
				t.Error("a refused change wrote a file")
			}
		})
	}
}

func TestNewRefusesOverlappingHistoryAndRoots(t *testing.T) {
	base := t.TempDir()
	st, err := store.Open(filepath.Join(base, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	root := filepath.Join(base, "panel")
	for _, opts := range []Options{
		{Roots: []string{root}, HistoryDir: filepath.Join(root, "history"), Store: st},
		{Roots: []string{filepath.Join(root, "history", "x")}, HistoryDir: filepath.Join(root, "history"), Store: st},
		{Roots: []string{root}, HistoryDir: root, Store: st},
		{Roots: nil, HistoryDir: root, Store: st},
		{Roots: []string{"relative"}, HistoryDir: root, Store: st},
		{Roots: []string{root}, HistoryDir: filepath.Join(base, "h"), Store: nil},
	} {
		if _, err := New(opts); err == nil {
			t.Errorf("New(%+v) succeeded", opts)
		}
	}
}

func TestHistoryKeepsTheLastTenVersions(t *testing.T) {
	e := newEnv(t)
	p := e.path("nginx.conf")
	ctx := context.Background()
	const edits = HistoryVersions + 5
	for i := 1; i <= edits; i++ {
		res, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: fmt.Appendf(nil, "v%d", i)}}, Reason: fmt.Sprintf("edit %d", i)})
		if err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
		if res.Versions[p] != i {
			t.Fatalf("apply %d recorded version %d", i, res.Versions[p])
		}
	}
	h, err := e.ed.History(ctx, p)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h) != HistoryVersions {
		t.Fatalf("history has %d versions, want %d", len(h), HistoryVersions)
	}
	// Newest first, numbers never reused after a trim.
	if h[0].Version != edits || h[len(h)-1].Version != edits-HistoryVersions+1 || h[0].Reason != fmt.Sprintf("edit %d", edits) {
		t.Errorf("history spans %d..%d (%q)", h[len(h)-1].Version, h[0].Version, h[0].Reason)
	}
	if h[0].Size != int64(len(fmt.Sprintf("v%d", edits))) || h[0].CreatedAt.IsZero() {
		t.Errorf("newest entry = %+v", h[0])
	}
	if _, err := e.ed.ReadVersion(ctx, p, 1); !errors.Is(err, ErrNoVersion) {
		t.Errorf("a trimmed version read as %v, want ErrNoVersion", err)
	}
	// The trimmed content is gone from disk too, not only from the index.
	blobs, err := os.ReadDir(filepath.Dir(e.ed.blobPath(p, 1)))
	if err != nil || len(blobs) != HistoryVersions {
		t.Errorf("%d version files on disk (%v), want %d", len(blobs), err, HistoryVersions)
	}
}

func TestHistorySkipsContentItAlreadyEndsWith(t *testing.T) {
	e := newEnv(t)
	p := e.path("nginx.conf")
	ctx := context.Background()
	for range 3 {
		if _, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte("same")}}}); err != nil {
			t.Fatal(err)
		}
	}
	if h, _ := e.ed.History(ctx, p); len(h) != 1 {
		t.Errorf("three identical applies recorded %d versions, want 1", len(h))
	}
}

func TestHistoryRecordsAnEditMadeBehindThePanelsBack(t *testing.T) {
	e := newEnv(t)
	p := e.path("nginx.conf")
	ctx := context.Background()
	if _, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte("panel")}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("edited over ssh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte("panel again")}}, Reason: "edit"}); err != nil {
		t.Fatal(err)
	}
	h, _ := e.ed.History(ctx, p)
	if len(h) != 3 {
		t.Fatalf("history has %d versions, want 3", len(h))
	}
	got, err := e.ed.ReadVersion(ctx, p, 2)
	if err != nil || string(got) != "edited over ssh" || h[1].Reason != "on disk before: edit" {
		t.Errorf("version 2 = %q (%q), %v; want the out-of-band content", got, h[1].Reason, err)
	}
}

func TestRestoreIsAnApply(t *testing.T) {
	e := newEnv(t)
	p := e.path("nginx.conf")
	ctx := context.Background()
	for _, c := range []string{"one", "two"} {
		if _, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte(c)}}}); err != nil {
			t.Fatal(err)
		}
	}
	s := e.svc("nginx")
	res, err := e.ed.Restore(ctx, p, 1, []Service{s})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if read(t, p) != "one" || res.Versions[p] != 3 {
		t.Errorf("file = %q, version %d; want the old content as a new version 3", read(t, p), res.Versions[p])
	}
	if e.log.String() != "validate:nginx reload:nginx health:nginx" {
		t.Errorf("a restore skipped the pipeline: %s", e.log)
	}

	// An old version that no longer validates is rolled back like any edit.
	s.failValidate = "no longer valid"
	if _, err := e.ed.Restore(ctx, p, 2, []Service{s}); err == nil || read(t, p) != "one" {
		t.Errorf("a rejected restore: err %v, file %q", err, read(t, p))
	}
	if _, err := e.ed.Restore(ctx, p, 99, []Service{s}); !errors.Is(err, ErrNoVersion) {
		t.Errorf("restore of a missing version = %v", err)
	}
}

func TestReadVersionDetectsDamage(t *testing.T) {
	e := newEnv(t)
	p := e.path("nginx.conf")
	ctx := context.Background()
	if _, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte("good")}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.ed.blobPath(p, 1), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ed.ReadVersion(ctx, p, 1); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("a damaged version read as %v", err)
	}
}

func TestAppliesOnOneServiceRunOneAtATime(t *testing.T) {
	e := newEnv(t)
	var active, peak atomic.Int32
	enter := func() {
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		// Work inside the critical section so an unserialised apply would
		// have every chance to overlap.
		for range 50 {
			_ = bytes.Repeat([]byte("x"), 1024)
		}
		active.Add(-1)
	}

	const workers = 8
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker edits its own file but shares the service.
			s := &fakeService{name: "nginx", log: e.log, onValidate: enter}
			p := e.path(fmt.Sprintf("site-%d.conf", i))
			if _, err := e.ed.Apply(context.Background(), Change{Files: []File{{Path: p, Content: []byte("x")}}, Services: []Service{s}}); err != nil {
				t.Errorf("apply %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Errorf("%d applies were inside the same service's validator at once", peak.Load())
	}
}

func TestACanceledRequestStillRollsBack(t *testing.T) {
	e := newEnv(t)
	p := e.seed(t, "nginx.conf", "good")
	ctx, cancel := context.WithCancel(context.Background())
	s := e.svc("nginx")
	s.failHealth = true
	s.onValidate = cancel // the client goes away mid-apply
	_, err := e.ed.Apply(ctx, Change{Files: []File{{Path: p, Content: []byte("bad")}}, Services: []Service{s}})
	if ae := applyErr(t, err); ae.RollbackErr != nil {
		t.Fatalf("rollback failed: %v", ae.RollbackErr)
	}
	if read(t, p) != "good" || s.reloads.Load() != 2 {
		t.Errorf("file = %q, reloads = %d; want the file restored and the service reloaded onto it", read(t, p), s.reloads.Load())
	}
}
