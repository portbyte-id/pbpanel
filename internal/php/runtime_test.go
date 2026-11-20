package php

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/store"
)

// runtimePaths is Paths rooted in a temp directory, so the unit, the log and the
// current link are real files.
func runtimePaths(t *testing.T) Paths {
	t.Helper()
	return tempPaths(t)
}

// tempPaths is Paths rooted in a temp directory with the data directory the
// layout would give it, which a unit cannot be written without.
func tempPaths(t *testing.T) Paths {
	t.Helper()
	root := filepath.ToSlash(t.TempDir())
	return Paths{Root: root, DataDir: root + "/panel/data"}
}

// installBuild makes the current link point at a build that has an FPM master.
func installBuild(t *testing.T, p Paths, ver string) {
	t.Helper()
	id := "20261119174430-00a640"
	bin := filepath.FromSlash(p.BinaryOf(ver, id))
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.FromSlash(p.CurrentLink(ver))
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.FromSlash(p.BuildPrefix(ver, id)), link); err != nil {
		t.Fatal(err)
	}
}

// flagStore keeps settings in memory, as *images.Catalog does on disk.
type flagStore map[string]string

func (f flagStore) Setting(_ context.Context, name string) (string, bool, error) {
	v, ok := f[name]
	return v, ok, nil
}
func (f flagStore) SetSetting(_ context.Context, name, value string) error {
	f[name] = value
	return nil
}

func TestEnsureRunningWritesTheUnitAndStartsTheVersion(t *testing.T) {
	p := runtimePaths(t)
	installBuild(t, p, "8.3")
	unitDir := t.TempDir()
	run := &fakeRunner{fn: unitState("loaded", "inactive", 0, "0")}
	rt := NewRuntime(p, run, "8.3").WithUnitDir(unitDir)

	res, err := rt.EnsureRunning(context.Background())
	if err != nil || res.Action != ActionStart {
		t.Fatalf("EnsureRunning = %+v, %v; want a start", res, err)
	}
	for _, want := range []string{"systemctl daemon-reload", "systemctl enable panel-php-8.3.service", "systemctl start panel-php-8.3.service"} {
		if !contains(run.calls, want) {
			t.Errorf("calls = %v, want %q", run.calls, want)
		}
	}
	unit, err := os.ReadFile(filepath.Join(unitDir, UnitName("8.3")))
	if err != nil || string(unit) != UnitText(p, "8.3") {
		t.Errorf("unit file = %q, %v", unit, err)
	}
	// The log the unit lists in ReadWritePaths exists before systemd reads it, or
	// the unit fails with 226/NAMESPACE.
	logFile := filepath.FromSlash(p.FPMErrorLog("8.3"))
	st, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("the FPM log was not created: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o640 {
		t.Errorf("the FPM log is %o, want 0640", perm)
	}
	for _, d := range []string{p.RunDir(), p.SiteLogDir()} {
		if st, err := os.Stat(filepath.FromSlash(d)); err != nil || !st.IsDir() {
			t.Errorf("%s was not created: %v", d, err)
		}
	}
}

// The directory the unit hides is the one the panel was given, not a path worked out
// from the root: a layout that moves the data must move the confinement with it.
func TestUnitTextHidesTheDataDirItWasGiven(t *testing.T) {
	p := testPaths
	p.DataDir = "/srv/pb/state"
	u := UnitText(p, "8.3")
	if !strings.Contains(u, "InaccessiblePaths=/srv/pb/state\n") {
		t.Errorf("the unit does not hide the given data dir:\n%s", u)
	}
	if strings.Contains(u, "/www/panel/data") {
		t.Errorf("the unit still names the path derived from the root:\n%s", u)
	}
}

// A unit without a data dir would leave the database readable by the pool, so none
// is written and the version is not started.
func TestEnsureRunningRefusesWithoutADataDir(t *testing.T) {
	p := runtimePaths(t)
	installBuild(t, p, "8.3")
	p.DataDir = ""
	unitDir := t.TempDir()
	run := &fakeRunner{fn: unitState("loaded", "inactive", 0, "0")}

	_, err := NewRuntime(p, run, "8.3").WithUnitDir(unitDir).EnsureRunning(context.Background())
	if !errors.Is(err, ErrNoDataDir) {
		t.Fatalf("EnsureRunning error = %v, want ErrNoDataDir", err)
	}
	if _, err := os.Stat(filepath.Join(unitDir, UnitName("8.3"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a unit was written without a data dir: %v", err)
	}
	if contains(run.calls, "systemctl start panel-php-8.3.service") {
		t.Errorf("the version was started: %v", run.calls)
	}
}

func TestEnsureRunningLeavesARunningVersionAloneAndRewritesOnlyOnChange(t *testing.T) {
	p := runtimePaths(t)
	installBuild(t, p, "8.3")
	unitDir := t.TempDir()
	run := &fakeRunner{fn: unitState("loaded", "active", 7, "100")}
	rt := NewRuntime(p, run, "8.3").WithUnitDir(unitDir)

	if _, err := rt.EnsureRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(unitDir, UnitName("8.3")))
	if err != nil {
		t.Fatal(err)
	}
	// A second pass finds the same text and must not rewrite or reload.
	run.calls = nil
	res, err := rt.EnsureRunning(context.Background())
	if err != nil || res.Action != ActionNone {
		t.Fatalf("EnsureRunning on a running version = %+v, %v", res, err)
	}
	if contains(run.calls, "systemctl daemon-reload") || contains(run.calls, "systemctl start panel-php-8.3.service") {
		t.Errorf("a running version was disturbed: %v", run.calls)
	}
	if again, _ := os.ReadFile(filepath.Join(unitDir, UnitName("8.3"))); string(again) != string(first) {
		t.Error("the unit was rewritten although nothing changed")
	}

	// A settings change adds an allowed root; the unit is rewritten, the running
	// version is not restarted by this call.
	roots := rootedPHPPaths(t, "/www/wwwroot")
	roots.Root = p.Root
	run.calls = nil
	if _, err := NewRuntime(roots, run, "8.3").WithUnitDir(unitDir).EnsureRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !contains(run.calls, "systemctl daemon-reload") {
		t.Errorf("a changed unit was not reloaded into systemd: %v", run.calls)
	}
	if contains(run.calls, "systemctl start panel-php-8.3.service") {
		t.Errorf("a running version was started again: %v", run.calls)
	}
	if again, _ := os.ReadFile(filepath.Join(unitDir, UnitName("8.3"))); string(again) == string(first) {
		t.Error("the unit was not rewritten although the allowed roots changed")
	}
}

func TestEnsureRunningWithoutABuildIsErrNoBuild(t *testing.T) {
	p := runtimePaths(t)
	rt := NewRuntime(p, &fakeRunner{}, "8.3").WithUnitDir(t.TempDir())
	if _, err := rt.EnsureRunning(context.Background()); !errors.Is(err, ErrNoBuild) {
		t.Errorf("EnsureRunning with no build = %v, want ErrNoBuild", err)
	}
}

// The container a host ran before must be gone before systemd starts FPM, or the
// new master races it for the pool sockets; and a version that already runs under
// systemd is never disturbed by it.
func TestRetireRunsBeforeTheFirstStartOnly(t *testing.T) {
	p := runtimePaths(t)
	installBuild(t, p, "8.3")
	unitDir := t.TempDir()
	retired := 0
	start := "systemctl start panel-php-8.3.service"
	run := &fakeRunner{fn: unitState("loaded", "inactive", 0, "0")}
	rt := NewRuntime(p, run, "8.3").WithUnitDir(unitDir).WithRetire(func(context.Context) error {
		retired++
		if contains(run.calls, start) {
			t.Error("the version was started before the old container was retired")
		}
		return nil
	})
	ctx := context.Background()
	if _, err := rt.EnsureRunning(ctx); err != nil || retired != 1 {
		t.Fatalf("EnsureRunning = %v, retired %d times", err, retired)
	}
	run.fn = unitState("loaded", "active", 7, "100")
	if _, err := rt.EnsureRunning(ctx); err != nil || retired != 1 {
		t.Errorf("a running version was retired again: %v, %d", err, retired)
	}

	failing := NewRuntime(p, &fakeRunner{fn: unitState("loaded", "inactive", 0, "0")}, "8.3").
		WithUnitDir(unitDir).
		WithRetire(func(context.Context) error {
			return errors.New("a container named panel-php-8.3 exists but the panel did not make it")
		})
	if _, err := failing.EnsureRunning(ctx); err == nil || !strings.Contains(err.Error(), "retire") {
		t.Errorf("a failed retirement started the version anyway: %v", err)
	}
}

func TestStopRecordsTheFlagAndDisablesTheUnit(t *testing.T) {
	p := runtimePaths(t)
	flags := flagStore{}
	run := &fakeRunner{fn: unitState("loaded", "active", 7, "100")}
	rt := NewRuntime(p, run, "8.3").WithUnitDir(t.TempDir()).WithFlags(flags)
	ctx := context.Background()

	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !rt.Stopped(ctx) || flags[StoppedKey("8.3")] != "1" {
		t.Errorf("a stop was not remembered: %v", flags)
	}
	// Per version: stopping 8.3 says nothing about 8.4.
	if NewRuntime(p, run, "8.4").WithFlags(flags).Stopped(ctx) {
		t.Error("a stop of 8.3 stopped 8.4 too")
	}
	// Disable comes before stop: a stop that took the panel down halfway must not
	// leave the version set to come back at boot.
	di, si := slices.Index(run.calls, "systemctl disable panel-php-8.3.service"), slices.Index(run.calls, "systemctl stop panel-php-8.3.service")
	if di < 0 || si < 0 || di > si {
		t.Errorf("calls = %v, want disable before stop", run.calls)
	}

	// Starting clears the flag even when there is nothing to start.
	installBuild(t, p, "8.3")
	if _, err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if rt.Stopped(ctx) {
		t.Error("a start left the stopped flag set")
	}
}

func TestStopToleratesAnUnloadedUnit(t *testing.T) {
	run := &fakeRunner{fn: func(name string, args []string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "stop" {
			return []byte("Failed to stop panel-php-8.3.service: Unit panel-php-8.3.service not loaded."), exitErr{5}
		}
		return nil, nil
	}}
	rt := NewRuntime(runtimePaths(t), run, "8.3").WithUnitDir(t.TempDir())
	if err := rt.Stop(context.Background()); err != nil {
		t.Errorf("Stop of an unloaded unit = %v, want nil", err)
	}
}

// Step 0: ProtectHome=true hides /home from ReadWritePaths as well, so a web root
// there would be unusable. read-only is the only value that works.
func TestUnitTextConfinesTheMasterWithoutHidingAllowedRoots(t *testing.T) {
	p := rootedPHPPaths(t, "/home/testsite", "/www/wwwroot")
	u := UnitText(p, "8.3")

	if !strings.Contains(u, "ProtectHome=read-only") {
		t.Errorf("the unit lacks ProtectHome=read-only:\n%s", u)
	}
	for line := range strings.SplitSeq(u, "\n") {
		if strings.TrimSpace(line) == "ProtectHome=true" {
			t.Errorf("the unit sets ProtectHome=true, which hides an allowed root under /home:\n%s", u)
		}
	}
	for _, want := range []string{
		"Type=notify",
		"Environment=PHP_INI_SCAN_DIR=:/www/panel/php/8.3/conf.d",
		"ExecStartPre=/www/panel/php/8.3/current/sbin/php-fpm -t -y /www/panel/php/8.3/php-fpm.conf -c /www/panel/php/8.3/php.ini",
		"ExecStart=/www/panel/php/8.3/current/sbin/php-fpm -F -y /www/panel/php/8.3/php-fpm.conf -c /www/panel/php/8.3/php.ini",
		"ExecReload=/bin/kill -USR2 $MAINPID",
		"KillSignal=SIGQUIT",
		"KillMode=mixed",
		"TimeoutStopSec=25",
		"Restart=on-failure",
		"ProtectSystem=strict",
		"PrivateTmp=true",
		"InaccessiblePaths=/www/panel/data",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit lacks %q:\n%s", want, u)
		}
	}
	// NoNewPrivileges would break a sendmail wrapper that relies on a setgid helper.
	if strings.Contains(u, "NoNewPrivileges") {
		t.Errorf("the unit sets NoNewPrivileges:\n%s", u)
	}
	// The log is a file the unit may write, and every allowed root with it.
	rw := ""
	for line := range strings.SplitSeq(u, "\n") {
		if v, ok := strings.CutPrefix(line, "ReadWritePaths="); ok {
			rw = v
		}
	}
	for _, want := range []string{p.FPMErrorLog("8.3"), p.RunDir(), p.SiteLogDir(), p.SitesRoot(), "/home/testsite", "/www/wwwroot"} {
		if !slices.Contains(strings.Fields(rw), want) {
			t.Errorf("ReadWritePaths lacks %s: %q", want, rw)
		}
	}
}

// The unit text is sorted so an unchanged configuration renders identical text and
// writeUnit leaves the file alone.
func TestUnitTextIsStableAcrossRootOrder(t *testing.T) {
	a := rootedPHPPaths(t, "/www/b", "/www/a")
	b := rootedPHPPaths(t, "/www/a", "/www/b")
	if UnitText(a, "8.3") != UnitText(b, "8.3") {
		t.Error("the unit text depends on the order of the allowed roots")
	}
	if UnitText(testPaths, "8.3") == UnitText(testPaths, "8.4") {
		t.Error("two versions share a unit text")
	}
}

func TestRuntimeStatusReadsTheUnit(t *testing.T) {
	rt := NewRuntime(testPaths, &fakeRunner{fn: unitState("loaded", "active", 7, "100")}, "8.3")
	st, err := rt.Status(context.Background())
	if err != nil || !st.Active || st.PID != 7 || st.Since != "100" {
		t.Errorf("Status = %+v, %v", st, err)
	}
}

// *images.Catalog is what production passes as the FlagStore; a real catalogue
// over a temp database proves the interface still matches it.
func TestFlagStoreIsSatisfiedByTheCatalogue(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rt := NewRuntime(runtimePaths(t), &fakeRunner{}, "8.3").WithUnitDir(t.TempDir()).WithFlags(images.NewCatalog(st))
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !rt.Stopped(context.Background()) {
		t.Error("the stopped flag did not survive a round trip through the catalogue")
	}
}
