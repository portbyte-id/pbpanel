package runtimes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeExec struct {
	runs  [][]string
	envs  [][]string
	onRun func(argv []string) error
	lines []string
	// head is what `git rev-parse HEAD` prints; empty means the pinned commit.
	head string
}

func (f *fakeExec) Run(_ context.Context, argv []string, env []string, onLine func(string)) error {
	f.runs = append(f.runs, argv)
	f.envs = append(f.envs, env)
	if argv[0] == "git" && len(argv) > 3 && argv[3] == "rev-parse" {
		head := f.head
		if head == "" {
			head = NVMCommit
		}
		onLine(head)
	} else {
		onLine("output of " + argv[0])
	}
	if f.onRun != nil {
		return f.onRun(argv)
	}
	return nil
}

func newMgr(t *testing.T, ex *fakeExec, users func(context.Context) (map[string][]string, error)) (*Manager, Paths) {
	t.Helper()
	p := Paths{Root: filepath.ToSlash(t.TempDir())}
	return New(Options{Paths: p, Exec: ex, Users: users}), p
}

// install pretends nvm installed a version.
func install(t *testing.T, p Paths, full string) {
	t.Helper()
	bin := filepath.FromSlash(p.NodeBin(full))
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func installNVM(t *testing.T, p Paths) {
	t.Helper()
	dir := filepath.FromSlash(p.NVMDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nvm.sh"), []byte("# nvm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVersionRequestsAreValidated(t *testing.T) {
	m, _ := newMgr(t, &fakeExec{}, nil)
	for _, bad := range []string{"", "latest", "lts/*", "22;rm -rf /", "22.11.0.1", "v22", "22 ", "$(id)", "../22", "1234"} {
		if err := m.Install(context.Background(), bad, func(string) {}); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("Install(%q) = %v, want ErrInvalidVersion", bad, err)
		}
		if _, err := m.NodePath(bad); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("NodePath(%q) = %v, want ErrInvalidVersion", bad, err)
		}
	}
	for _, bad := range []string{"22", "22.11", "v22.11.0", "22.11.0;x", ""} {
		if err := m.Uninstall(context.Background(), bad, func(string) {}); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("Uninstall(%q) = %v, want ErrInvalidVersion", bad, err)
		}
	}
}

func TestInstallClonesNVMOnceThenInstallsThroughOneBashCall(t *testing.T) {
	ex := &fakeExec{}
	m, p := newMgr(t, ex, nil)
	ex.onRun = func(argv []string) error {
		switch argv[0] {
		case "git":
			installNVM(t, p)
		case "bash":
			install(t, p, "22.11.0")
		}
		return nil
	}
	var lines []string
	if err := m.Install(context.Background(), "22", func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(ex.runs) != 3 {
		t.Fatalf("runs: %v", ex.runs)
	}
	wantClone := []string{"git", "clone", "--depth", "1", "--branch", NVMTag, NVMRepo, p.NVMDir()}
	if !reflect.DeepEqual(ex.runs[0], wantClone) {
		t.Errorf("clone argv:\n got %q\nwant %q", ex.runs[0], wantClone)
	}
	wantRun := []string{"bash", "-c", installScript, "bash", p.NVMDir(), "22"}
	wantRev := []string{"git", "-C", p.NVMDir(), "rev-parse", "HEAD"}
	if !reflect.DeepEqual(ex.runs[1], wantRev) {
		t.Errorf("the clone must be checked against the pinned commit:\n got %q\nwant %q", ex.runs[1], wantRev)
	}
	if !reflect.DeepEqual(ex.runs[2], wantRun) {
		t.Errorf("install argv:\n got %q\nwant %q", ex.runs[2], wantRun)
	}
	if strings.Contains(installScript, "22") {
		t.Error("the version must reach the script as an argument, never in its text")
	}
	if len(lines) < 3 || lines[len(lines)-1] != "installed Node.js 22.11.0" {
		t.Errorf("job output: %q", lines)
	}

	// A second install finds nvm and does not clone again.
	ex.runs = nil
	if err := m.Install(context.Background(), "22", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(ex.runs) != 1 || ex.runs[0][0] != "bash" {
		t.Errorf("second install runs: %v", ex.runs)
	}
}

func TestInstallFailureAndMissingResultAreErrors(t *testing.T) {
	ex := &fakeExec{}
	m, p := newMgr(t, ex, nil)
	installNVM(t, p)

	ex.onRun = func([]string) error { return errors.New("exit status 3") }
	if err := m.Install(context.Background(), "22", func(string) {}); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("a failing nvm must surface: %v", err)
	}

	ex.onRun = nil // nvm "succeeds" but nothing appears on disk
	if err := m.Install(context.Background(), "22", func(string) {}); err == nil || !strings.Contains(err.Error(), "no Node.js 22 is installed") {
		t.Fatalf("a silent no-op must not read as success: %v", err)
	}
}

func TestResolveAndNodePathPickTheHighestMatch(t *testing.T) {
	m, p := newMgr(t, &fakeExec{}, nil)
	for _, v := range []string{"22.9.0", "22.11.0", "22.11.1", "20.18.0", "24.1.0"} {
		install(t, p, v)
	}
	// A half-finished install has no binary and is invisible.
	if err := os.MkdirAll(filepath.FromSlash(p.NodeDir("22.99.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	for req, want := range map[string]string{"22": "22.11.1", "22.11": "22.11.1", "22.9": "22.9.0", "20": "20.18.0", "24.1.0": "24.1.0"} {
		if got, ok := m.Resolve(req); !ok || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", req, got, ok, want)
		}
	}
	if _, ok := m.Resolve("18"); ok {
		t.Error("18 resolved")
	}
	if _, err := m.NodePath("18"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("NodePath(18) = %v", err)
	}
	got, err := m.NodePath("22")
	if err != nil || got != p.NodeBin("22.11.1") || !strings.HasSuffix(got, "/v22.11.1/bin/node") {
		t.Errorf("NodePath(22) = %q, %v", got, err)
	}
}

func TestListReportsUsers(t *testing.T) {
	users := func(context.Context) (map[string][]string, error) {
		return map[string][]string{"22.11.0": {"api"}}, nil
	}
	m, p := newMgr(t, &fakeExec{}, users)
	install(t, p, "22.11.0")
	install(t, p, "20.18.0")
	st, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.NVMInstalled || st.NVMTag != NVMTag || len(st.Node) != 2 {
		t.Fatalf("state: %+v", st)
	}
	if st.Node[0].Version != "22.11.0" || !reflect.DeepEqual(st.Node[0].UsedBy, []string{"api"}) || len(st.Node[1].UsedBy) != 0 {
		t.Fatalf("nodes: %+v", st.Node)
	}
	installNVM(t, p)
	if st, _ = m.List(context.Background()); !st.NVMInstalled {
		t.Error("nvm not detected")
	}
}

func TestUninstallRefusesAVersionInUse(t *testing.T) {
	users := func(context.Context) (map[string][]string, error) {
		return map[string][]string{"22.11.0": {"api", "web"}}, nil
	}
	m, p := newMgr(t, &fakeExec{}, users)
	install(t, p, "22.11.0")
	install(t, p, "20.18.0")

	err := m.Uninstall(context.Background(), "22.11.0", func(string) {})
	if !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "api, web") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(p.NodeBin("22.11.0"))); err != nil {
		t.Fatal("a refused removal deleted the version")
	}
	if err := m.Uninstall(context.Background(), "20.18.0", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.FromSlash(p.NodeDir("20.18.0"))); !os.IsNotExist(err) {
		t.Fatal("version still on disk")
	}
	if err := m.Uninstall(context.Background(), "20.18.0", func(string) {}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("second removal: %v", err)
	}
}

func TestOnlyOneOperationRunsAtATime(t *testing.T) {
	ex := &fakeExec{}
	m, p := newMgr(t, ex, nil)
	installNVM(t, p)
	started, release := make(chan struct{}), make(chan struct{})
	ex.onRun = func([]string) error {
		close(started)
		<-release
		install(t, p, "22.11.0")
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- m.Install(context.Background(), "22", func(string) {}) }()
	<-started
	if err := m.Install(context.Background(), "20", func(string) {}); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent install: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLineWriterSplitsProgressOutput(t *testing.T) {
	var got []string
	w := &lineWriter{emit: func(s string) { got = append(got, s) }}
	_, _ = w.Write([]byte("Downloading 10%\rDownloading 50%\rdone\nnext"))
	w.flush()
	want := []string{"Downloading 10%", "Downloading 50%", "done", "next"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A tag that was moved to another commit is refused, and nothing is sourced.
func TestInstallRefusesAMovedTag(t *testing.T) {
	ex := &fakeExec{head: "0000000000000000000000000000000000000000"}
	m, p := newMgr(t, ex, nil)
	ex.onRun = func(argv []string) error {
		if argv[0] == "git" && argv[1] == "clone" {
			installNVM(t, p)
		}
		return nil
	}
	err := m.Install(context.Background(), "22", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(filepath.FromSlash(p.NVMDir())); !os.IsNotExist(statErr) {
		t.Fatal("the unpinned checkout was left in place")
	}
	for _, run := range ex.runs {
		if run[0] == "bash" {
			t.Fatal("nvm was run from an unpinned checkout")
		}
	}
}
