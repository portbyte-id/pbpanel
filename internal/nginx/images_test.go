//go:build unix

package nginx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
)

func init() { settle = 0 }

// nativePaths is a Paths rooted in a temp directory, so builds, links and the unit
// file are real files.
func nativePaths(t *testing.T) Paths {
	t.Helper()
	return Paths{Root: filepath.ToSlash(t.TempDir())}
}

// fakeBuilder makes a build directory with a binary in it, as DockerBuilder would.
type fakeBuilder struct {
	p   Paths
	err error
}

func (b *fakeBuilder) Build(_ context.Context, id, _ string, _ docker.Progress) error {
	if b.err != nil {
		return b.err
	}
	bin := filepath.FromSlash(b.p.BinaryOf(id))
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	return os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
}

// writeConf puts an nginx.conf where the check expects one.
func writeConf(t *testing.T, p Paths) {
	t.Helper()
	conf := filepath.FromSlash(p.ConfPath())
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("events {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nginxHost scripts a host: `nginx -t` answers per test, and systemctl reports the
// unit as active or not.
type nginxHost struct {
	testOut  string
	testCode int // 0: passes
	active   bool
	// restartFails makes the next that many restarts fail.
	restartFails int
	fakeRunner
}

func newHost() *nginxHost {
	h := &nginxHost{}
	h.fn = func(name string, args []string) ([]byte, error) {
		switch {
		case strings.HasSuffix(name, "/sbin/nginx") && len(args) > 0 && args[0] == "-t":
			if h.testCode != 0 {
				return []byte(h.testOut), exitErr{h.testCode}
			}
			return []byte("nginx: test is successful\n"), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "show":
			if h.active {
				return show("loaded", "active", 7, "100"), nil
			}
			return show("loaded", "inactive", 0, "0"), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "restart":
			if h.restartFails > 0 {
				h.restartFails--
				return []byte("Job failed"), exitErr{1}
			}
		}
		return nil, nil
	}
	return h
}

func newImages(t *testing.T, p Paths, h *nginxHost) *Images {
	t.Helper()
	imgs, err := NewImages(p, h, &fakeBuilder{p: p}, images.NewCatalog(openStore(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	return imgs
}

// The first build is made while the panel is still writing nginx's include
// files: a config that points at them cannot be checked yet, so the check does
// not run, and the apply that writes the files validates the configuration.
func TestFirstBuildIsNotCheckedAgainstAConfigThatIsNotWrittenYet(t *testing.T) {
	p, h := nativePaths(t), newHost()
	h.testCode, h.testOut = 1, "nginx: [emerg] open() \"/www/panel/nginx/panel-main.conf\" failed\n"
	imgs := newImages(t, p, h)

	first, err := imgs.Build(context.Background(), nil)
	if err != nil {
		t.Fatalf("first build = %v; nothing serves, so nothing should be checked", err)
	}
	for _, c := range h.calls {
		if strings.Contains(c, " -t ") {
			t.Errorf("the first build ran nginx -t: %v", h.calls)
		}
	}
	if got, err := readLink(p); err != nil || got != first.ID {
		t.Errorf("the link names %q (%v), want the first build %s", got, err, first.ID)
	}
}

// A host upgraded from the container runtime has a configuration on disk that
// still names the image's files. No native build serves yet, so the first build
// is not checked against it; the reconcile rewrites the files and validates them.
func TestFirstNativeBuildIsNotCheckedAgainstAContainerEraConfig(t *testing.T) {
	p, h := nativePaths(t), newHost()
	writeConf(t, p)
	h.testCode, h.testOut = 1, "nginx: [emerg] open() \"/etc/nginx/mime.types\" failed\n"
	imgs := newImages(t, p, h)

	if _, err := imgs.Build(context.Background(), nil); err != nil {
		t.Fatalf("first native build = %v; no native build serves, so nothing should be checked", err)
	}
	for _, c := range h.calls {
		if strings.Contains(c, " -t ") {
			t.Errorf("the first native build ran nginx -t: %v", h.calls)
		}
	}
	// From now on a native build serves, and the next one is checked.
	if _, err := imgs.Build(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "rejected the build") {
		t.Errorf("the second build = %v, want it checked and rejected", err)
	}
}

// The candidate is checked with its own binary: the binary that serves would say
// nothing about the new one.
func TestCandidateIsCheckedWithItsOwnBinary(t *testing.T) {
	p, h := nativePaths(t), newHost()
	writeConf(t, p)
	imgs := newImages(t, p, h)
	ctx := context.Background()

	if _, err := imgs.Build(ctx, nil); err != nil {
		t.Fatal(err)
	}
	h.calls = nil
	second, err := imgs.Build(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := p.BinaryOf(second.ID) + " -t -c " + p.ConfPath()
	if !contains(h.calls, want) {
		t.Errorf("calls = %v, want %q", h.calls, want)
	}
	if contains(h.calls, p.Binary()+" -t -c "+p.ConfPath()) {
		t.Errorf("the candidate was checked with the binary that serves: %v", h.calls)
	}
}

func TestRejectedUpdateKeepsTheBuildThatServes(t *testing.T) {
	p, h := nativePaths(t), newHost()
	writeConf(t, p)
	imgs := newImages(t, p, h)
	ctx := context.Background()
	first, err := imgs.Build(ctx, nil)
	if err != nil {
		t.Fatalf("first build = %v", err)
	}

	h.testCode, h.testOut = 1, "nginx: [emerg] unknown directive \"bogus\"\n"
	if _, err := imgs.Build(ctx, nil); err == nil || !strings.Contains(err.Error(), "rejected the build") {
		t.Fatalf("update with a broken build = %v", err)
	}
	cur, ok, err := imgs.Current(ctx)
	if err != nil || !ok || cur.ID != first.ID {
		t.Errorf("current after a rejected update = %+v (ok=%v err=%v), want %s", cur, ok, err, first.ID)
	}
	if got, _ := readLink(p); got != first.ID {
		t.Errorf("the link names %s after a rejected update, want %s", got, first.ID)
	}
	builds, _ := imgs.Builds(ctx)
	if builds[0].State != images.StateFailed || !strings.Contains(builds[0].Output, "unknown directive") {
		t.Errorf("rejected update = %+v", builds[0])
	}
	if _, err := os.Stat(filepath.FromSlash(p.BuildDir(builds[0].ID))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the rejected build's files were kept: %v", err)
	}
}

func TestRollbackPutsThePreviousBuildBack(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	ctx := context.Background()
	first, _ := imgs.Build(ctx, nil)
	second, _ := imgs.Build(ctx, nil)

	if _, err := imgs.Activate(ctx, first.ID); err != nil {
		t.Fatalf("rollback = %v", err)
	}
	cur, _, _ := imgs.Current(ctx)
	if cur.ID != first.ID {
		t.Errorf("after rollback, current = %s, want %s", cur.ID, first.ID)
	}
	if got, _ := readLink(p); got != first.ID {
		t.Errorf("after rollback the link names %s, want %s", got, first.ID)
	}
	builds, _ := imgs.Builds(ctx)
	for _, b := range builds {
		if b.ID == second.ID && !b.Previous {
			t.Error("the build rolled back from is not kept as previous")
		}
	}
}

// A running nginx is restarted onto the new binary; a stopped one has nothing to
// restart, and the boot reconcile starts it on the link.
func TestSwapRestartsARunningNginxAndLeavesAStoppedOneAlone(t *testing.T) {
	restart := "systemctl restart " + UnitName

	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	ctx := context.Background()
	if _, err := imgs.Build(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := imgs.Build(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if contains(h.calls, restart) {
		t.Errorf("a build restarted a stopped nginx: %v", h.calls)
	}

	p2, h2 := nativePaths(t), newHost()
	h2.active = true
	imgs2 := newImages(t, p2, h2)
	if _, err := imgs2.Build(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !contains(h2.calls, restart) {
		t.Errorf("a build did not restart the running nginx: %v", h2.calls)
	}
}

// A new build whose restart does not hold must leave the old one serving.
func TestFailedRestartPutsTheOldBuildBack(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	ctx := context.Background()
	first, err := imgs.Build(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.active, h.restartFails = true, 1
	h.calls = nil

	if _, err := imgs.Build(ctx, nil); err == nil || !strings.Contains(err.Error(), "swap") {
		t.Fatalf("a build whose restart failed = %v, want a swap error", err)
	}
	if got, _ := readLink(p); got != first.ID {
		t.Errorf("the link names %s, want the build that served before (%s)", got, first.ID)
	}
	restarts := 0
	for _, c := range h.calls {
		if c == "systemctl restart "+UnitName {
			restarts++
		}
	}
	if restarts != 2 {
		t.Errorf("restarts = %d, want 2 (onto the new build, then back): %v", restarts, h.calls)
	}
	cur, _, _ := imgs.Current(ctx)
	if cur.ID != first.ID {
		t.Errorf("current = %s, want %s", cur.ID, first.ID)
	}
}

func TestNoBuildYet(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	if ok, err := imgs.Present(context.Background()); err != nil || ok {
		t.Errorf("Present before any build = %v, %v", ok, err)
	}
	rt := NewRuntime(p, h).WithUnitDir(t.TempDir())
	if ok, err := rt.BuildPresent(context.Background()); err != nil || ok {
		t.Errorf("BuildPresent = %v, %v", ok, err)
	}
	if _, err := rt.EnsureRunning(context.Background()); !errors.Is(err, ErrNoBuild) {
		t.Errorf("EnsureRunning before a build = %v, want ErrNoBuild", err)
	}
}

func TestBuildDirectoryOfTheServingBuildIsNeverRemoved(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	first, err := imgs.Build(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (dirStore{p}).ImageRemove(context.Background(), first.Tag); err == nil {
		t.Error("the build the link names was removed")
	}
	if _, err := os.Stat(filepath.FromSlash(p.BinaryOf(first.ID))); err != nil {
		t.Errorf("the serving build's binary is gone: %v", err)
	}
}

// A link lost or pointing elsewhere is put back at boot to the build the catalogue
// says serves.
func TestReconcileRepointsTheLink(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	ctx := context.Background()
	first, err := imgs.Build(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.FromSlash(p.CurrentLink())); err != nil {
		t.Fatal(err)
	}
	ok, err := imgs.Reconcile(ctx)
	if err != nil || !ok {
		t.Fatalf("Reconcile = %v, %v", ok, err)
	}
	if got, _ := readLink(p); got != first.ID {
		t.Errorf("link = %q, want %s", got, first.ID)
	}
}

// A build whose files have gone is dropped at boot instead of being started.
func TestReconcileDropsABuildWhoseFilesAreGone(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	ctx := context.Background()
	first, err := imgs.Build(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.FromSlash(p.BuildDir(first.ID))); err != nil {
		t.Fatal(err)
	}
	ok, err := imgs.Reconcile(ctx)
	if err != nil || ok {
		t.Fatalf("Reconcile = %v, %v; want no build serving", ok, err)
	}
}

func TestEnsureRunningWritesTheUnitAndStartsNginx(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	if _, err := imgs.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	unitDir := t.TempDir()
	rt := NewRuntime(p, h).WithUnitDir(unitDir)

	res, err := rt.EnsureRunning(context.Background())
	if err != nil || res.Action != ActionStart {
		t.Fatalf("EnsureRunning = %+v, %v; want a start", res, err)
	}
	for _, want := range []string{"systemctl daemon-reload", "systemctl enable " + UnitName, "systemctl start " + UnitName} {
		if !contains(h.calls, want) {
			t.Errorf("calls = %v, want %q", h.calls, want)
		}
	}
	unit, err := os.ReadFile(filepath.Join(unitDir, UnitName))
	if err != nil || string(unit) != UnitText(p) {
		t.Errorf("unit file = %q, %v", unit, err)
	}
	for _, d := range []string{p.CacheDir(), p.MainLogDir(), p.RunDir()} {
		if st, err := os.Stat(filepath.FromSlash(d)); err != nil || !st.IsDir() {
			t.Errorf("%s was not created: %v", d, err)
		}
	}

	// A running nginx is left alone and the unit is not rewritten.
	h.active = true
	h.calls = nil
	res, err = rt.EnsureRunning(context.Background())
	if err != nil || res.Action != ActionNone {
		t.Fatalf("EnsureRunning on a running nginx = %+v, %v", res, err)
	}
	if contains(h.calls, "systemctl start "+UnitName) || contains(h.calls, "systemctl daemon-reload") {
		t.Errorf("a running nginx was disturbed: %v", h.calls)
	}
}

// The container a host ran before must be gone before systemd starts nginx, or the
// ports are taken; and a nginx that already runs is never disturbed by it.
func TestRetireRunsBeforeTheFirstStartOnly(t *testing.T) {
	p, h := nativePaths(t), newHost()
	imgs := newImages(t, p, h)
	if _, err := imgs.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	retired := 0
	rt := NewRuntime(p, h).WithUnitDir(t.TempDir()).WithRetire(func(context.Context) error {
		retired++
		// By now nothing may have been started.
		if contains(h.calls, "systemctl start "+UnitName) {
			t.Error("nginx was started before the old container was retired")
		}
		return nil
	})
	if _, err := rt.EnsureRunning(context.Background()); err != nil || retired != 1 {
		t.Fatalf("EnsureRunning = %v, retired %d times", err, retired)
	}
	h.active = true
	if _, err := rt.EnsureRunning(context.Background()); err != nil || retired != 1 {
		t.Errorf("a running nginx was retired again: %v, %d", err, retired)
	}

	failing := NewRuntime(p, newHost()).WithUnitDir(t.TempDir()).WithRetire(func(context.Context) error {
		return errors.New("a container named panel-nginx exists but the panel did not make it")
	})
	if _, err := failing.EnsureRunning(context.Background()); err == nil || !strings.Contains(err.Error(), "retire") {
		t.Errorf("a failed retirement started nginx anyway: %v", err)
	}
}

func TestStopIsRememberedAndDisablesTheUnit(t *testing.T) {
	p, h := nativePaths(t), newHost()
	cat := images.NewCatalog(openStore(t))
	rt := NewRuntime(p, h).WithUnitDir(t.TempDir()).WithFlags(cat)
	ctx := context.Background()

	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !rt.Stopped(ctx) {
		t.Error("a stop was not remembered")
	}
	if !contains(h.calls, "systemctl disable "+UnitName) || !contains(h.calls, "systemctl stop "+UnitName) {
		t.Errorf("calls = %v", h.calls)
	}
	// Starting clears the flag even when there is nothing to start.
	if _, err := rt.Start(ctx); !errors.Is(err, ErrNoBuild) {
		t.Errorf("Start with no build = %v", err)
	}
	if rt.Stopped(ctx) {
		t.Error("a start left the stopped flag set")
	}
}
