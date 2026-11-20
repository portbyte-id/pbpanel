//go:build integration && linux

package php

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
	"github.com/portbyte-id/server-panel/internal/store"
)

// noApply is the applier of a test that writes no configuration through confedit.
type noApply struct{}

func (noApply) Apply(context.Context, confedit.Change) (confedit.Result, error) {
	return confedit.Result{}, errors.New("this test does not apply configuration")
}

// requireBuildHost skips the test unless this host can compile PHP and run it
// under systemd: root (the unit is installed where systemd reads it), systemd as
// PID 1, the www account pools run as, and a Docker Engine for the builder. A
// host whose panel already owns the version's unit is skipped rather than
// disturbed: that unit is the host's, not the test's.
func requireBuildHost(t *testing.T, ver string) *docker.Client {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("a native PHP build installs a systemd unit: it needs root")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("systemd is not running on this host")
	}
	if _, err := user.Lookup(WWWUser); err != nil {
		t.Skipf("the pools run as %s, which this host does not have: %v", WWWUser, err)
	}
	unit := filepath.Join(DefaultUnitDir, UnitName(ver))
	if _, err := os.Stat(unit); err == nil {
		t.Skipf("%s is already installed; the test owns that unit, so remove it first", unit)
	}
	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ping(context.Background()); err != nil {
		t.Skipf("no Docker Engine, and the native builder needs one: %v", err)
	}
	return eng
}

// TestRealBuildLifecycle runs the M8 PHP lifecycle natively: a build with
// extensions is checked and swapped under a running systemd unit; a build that
// fails leaves that unit alone; an activation puts the earlier build back.
//
// It compiles the core twice, several minutes each, and needs Docker, network
// access for the PHP sources and PECL, root, systemd and the www account.
// PB_PHP_IT_VERSION picks the series (default 8.3). The test installs
// panel-php-<ver>.service itself and removes it again, so a host whose panel
// already runs that version skips instead.
func TestRealBuildLifecycle(t *testing.T) {
	ver := os.Getenv("PB_PHP_IT_VERSION")
	if ver == "" {
		ver = "8.3"
	}
	if err := CheckVersion(ver); err != nil {
		t.Fatal(err)
	}
	eng := requireBuildHost(t, ver)
	ctx := context.Background()

	root, err := os.MkdirTemp("", "panel-php-it-")
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last in first-out order: the unit goes before the root does.
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p := Paths{Root: root, DataDir: root + "/panel/data"}
	// The pools run as www, so every directory on the way to the socket and the
	// web root must be traversable by it; MkdirTemp makes them 0700.
	for _, dir := range []string{"", p.Dir(ver), p.ConfDir(ver), p.PoolDir(ver), p.RunDir(), p.SiteLogDir(), p.SitesRoot(), filepath.Join(root, "panel", "data")} {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	st, err := store.Open(filepath.Join(root, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m, err := New(Options{
		Store: st, Engine: eng, Applier: noApply{}, Paths: p,
		Supported: func(context.Context) ([]string, error) { return []string{ver}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Remove(context.Background(), ver, nil) })

	// Build A, with no extensions: it serves; nothing runs yet.
	if err := m.StartPull(ctx, ver); err != nil {
		t.Fatal(err)
	}
	if err := m.Pull(ctx, ver, nil, nil); err != nil {
		t.Fatalf("install = %v", err)
	}
	a, err := m.Get(ctx, ver)
	if err != nil || a.Current == nil {
		t.Fatalf("after install: %+v, %v", a, err)
	}

	// The files the unit starts on, written as the confedit path writes them:
	// php.ini copied from the build itself, then the rendered FPM and form files.
	ini, err := os.ReadFile(filepath.FromSlash(p.ProductionIniFile(ver, a.Current.ID)))
	if err != nil {
		t.Fatalf("read php.ini-production of the build: %v", err)
	}
	writeFile(t, p.IniPath(ver), ini)
	fpm, err := p.RenderFPMConf(ver)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.FPMConf(ver), fpm)
	form, err := RenderFormIni(DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.FormIni(ver), form)

	// The first site would start the unit on build A.
	if _, err := m.EnsureRunning(ctx, ver); err != nil {
		t.Fatalf("start on build A: %v", err)
	}
	before, found, err := m.Status(ctx, ver)
	if err != nil || !found || !before.Active {
		t.Fatalf("unit after the start: found=%v active=%v err=%v", found, before.Active, err)
	}
	if mods := builtModules(t, p, ver); strings.Contains(mods, "\nintl\n") {
		t.Fatal("build A already has intl")
	}

	// A build with intl and redis is checked, then swapped under the running unit.
	exts, err := ParseExtensions([]string{"intl", "redis"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Rebuild(ctx, ver, exts, nil)
	if err != nil {
		t.Fatalf("build B = %v", err)
	}
	after, found, err := m.Status(ctx, ver)
	if err != nil || !found || !after.Active {
		t.Fatalf("unit after the swap: found=%v active=%v err=%v", found, after.Active, err)
	}
	// A binary cannot be swapped under a reload: the swap restarts the unit, so
	// the master must be a different process.
	if after.PID == before.PID {
		t.Errorf("the swap kept the old master (pid %d); a restart must replace it", before.PID)
	}
	if mods := builtModules(t, p, ver); !strings.Contains(mods, "\nintl\n") || !strings.Contains(mods, "\nredis\n") {
		t.Errorf("the serving build's modules lack intl or redis:\n%s", mods)
	}
	if cur, _ := m.Get(ctx, ver); cur.Current == nil || cur.Current.ID != b.ID {
		t.Errorf("current after the swap = %+v, want build %s", cur.Current, b.ID)
	}
	if link, _ := readLink(p, ver); link != b.ID {
		t.Errorf("the serving link names %q, want build %s", link, b.ID)
	}

	// A build that cannot be made fails; the unit that serves is untouched.
	bogus, err := ParseExtensions([]string{"m8nosuchpeclpackage"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rebuild(ctx, ver, bogus, nil); err == nil {
		t.Fatal("a build of a package that does not exist succeeded")
	}
	still, found, err := m.Status(ctx, ver)
	if err != nil || !found || !still.Active || still.PID != after.PID {
		t.Errorf("the serving unit changed after a failed build: pid %d -> %d active=%v", after.PID, still.PID, still.Active)
	}
	if cur, _ := m.Get(ctx, ver); cur.Current == nil || cur.Current.ID != b.ID {
		t.Errorf("current moved after a failed build: %+v", cur.Current)
	}

	// Activating build A puts the link back and restarts the unit onto it.
	if _, err := m.Activate(ctx, ver, a.Current.ID); err != nil {
		t.Fatalf("activate A = %v", err)
	}
	back, found, err := m.Status(ctx, ver)
	if err != nil || !found || !back.Active {
		t.Fatalf("unit after activating A: found=%v active=%v err=%v", found, back.Active, err)
	}
	if back.PID == still.PID {
		t.Error("activating A did not restart the unit")
	}
	if link, _ := readLink(p, ver); link != a.Current.ID {
		t.Errorf("the serving link names %q, want build %s", link, a.Current.ID)
	}
	if mods := builtModules(t, p, ver); strings.Contains(mods, "\nintl\n") {
		t.Error("build A is serving but still lists intl")
	}
}

// builtModules runs `php -m` from the build the serving link names, with the
// scan directory the unit gives it.
func builtModules(t *testing.T, p Paths, ver string) string {
	t.Helper()
	out, err := (native.ExecRunner{}).RunEnv(context.Background(),
		[]string{scanDirEnv(p, ver)}, p.CLIBinary(ver),
		"-c", p.IniPath(ver), "-d", "display_startup_errors=1", "-d", "log_errors=0", "-m")
	if err != nil {
		t.Fatalf("php -m: %v: %s", err, out)
	}
	return "\n" + strings.ReplaceAll(string(out), "\r", "") + "\n"
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.FromSlash(path), content, 0o644); err != nil {
		t.Fatal(err)
	}
}
