package php

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/images"
)

// moduleRunner answers `php -m` with a fixed module list, which is what the
// candidate check reads; `php-fpm -t` and systemctl go to the plain host runner.
type moduleRunner struct {
	*hostRunner
	modules string
}

func (r *moduleRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	if strings.HasSuffix(name, "/bin/php") {
		r.calls = append(r.calls, name+" "+strings.Join(args, " "))
		r.envs = append(r.envs, strings.Join(env, ","))
		return []byte(r.modules), nil
	}
	return r.hostRunner.RunEnv(ctx, env, name, args...)
}

// newBuildRig is a manager whose builds go to a temporary root and whose
// `php -m` lists the given modules.
func newBuildRig(t *testing.T, modules string) (*Manager, *fakeBuild, *moduleRunner, Paths) {
	t.Helper()
	p := tempPaths(t)
	r := newRigAt(t, p)
	r.run.modules = modules
	return r.m, r.b, &moduleRunner{hostRunner: r.run, modules: modules}, p
}

func install(t *testing.T, m *Manager, names ...string) Version {
	t.Helper()
	exts, err := ParseExtensions(names)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartPull(ctx, "8.3"); err != nil {
		t.Fatal(err)
	}
	if err := m.Pull(ctx, "8.3", exts, nil); err != nil {
		t.Fatalf("Pull(%v) = %v", names, err)
	}
	v, err := m.Get(ctx, "8.3")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A build is checked with its own binaries against the live configuration, and
// then served: the catalogue keeps its build, the release it compiled and the
// tarball it came from.
func TestPullWithExtensionsChecksTheCandidateAndServes(t *testing.T) {
	m, b, run, p := newBuildRig(t, "[PHP Modules]\nCore\nintl\nredis\n")
	v := install(t, m, "intl", "redis")

	if v.State != StateReady || v.Current == nil {
		t.Fatalf("after pull: %+v", v)
	}
	if !strings.HasPrefix(v.Current.Tag, "panel/php:8.3-") {
		t.Errorf("tag = %q, want panel/php:8.3-<build id>", v.Current.Tag)
	}
	if got := strings.Join(v.Current.Extensions, ","); got != "intl,redis" {
		t.Errorf("extensions = %q", got)
	}
	if v.Image != "8.3.37" || v.Digest == "" {
		t.Errorf("image/digest = %q/%q, want the release and the tarball sha256", v.Image, v.Digest)
	}
	if len(b.calls) != 1 || !strings.HasSuffix(b.calls[0], "intl,redis") {
		t.Fatalf("builds = %v", b.calls)
	}
	// The check ran the candidate's own php, with the panel's conf.d scanned.
	bin := filepath.ToSlash(p.CLIBinaryOf("8.3", v.Current.ID))
	found := false
	for i, c := range run.calls {
		if strings.HasPrefix(c, bin) {
			found = true
			if run.envs[i] != "PHP_INI_SCAN_DIR=:"+p.ConfDir("8.3") {
				t.Errorf("the module check ran without the scan dir: %q", run.envs[i])
			}
		}
	}
	if !found {
		t.Errorf("the candidate's own php was not run: %v", run.calls)
	}
	// The link names the build that serves.
	if id, err := readLink(p, "8.3"); err != nil || id != v.Current.ID {
		t.Errorf("current link = %q, %v; want %s", id, err, v.Current.ID)
	}
}

func TestCandidateMissingAnExtensionIsRejectedAndTheOldBuildServes(t *testing.T) {
	m, b, _, p := newBuildRig(t, "[PHP Modules]\nCore\n")
	first := install(t, m)

	b.calls = nil
	exts, _ := ParseExtensions([]string{"intl"})
	if _, err := m.Rebuild(ctx, "8.3", exts, nil); err == nil || !strings.Contains(err.Error(), "does not load: intl") {
		t.Fatalf("Rebuild = %v, want the missing module reported", err)
	}
	after, err := m.Get(ctx, "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if after.State != StateReady || after.Current == nil || after.Current.ID != first.Current.ID {
		t.Errorf("the serving build changed after a rejected rebuild: %+v", after.Current)
	}
	builds, _ := m.Builds(ctx, "8.3")
	if builds[0].State != images.StateFailed || !strings.Contains(builds[0].Output, "does not load: intl") {
		t.Errorf("rejected build = %+v", builds[0])
	}
	// The rejected build's directory is gone, and the link still names the one
	// that serves.
	if _, err := os.Stat(filepath.FromSlash(p.BuildPrefix("8.3", builds[0].ID))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the rejected build was kept: %v", err)
	}
	if id, _ := readLink(p, "8.3"); id != first.Current.ID {
		t.Errorf("the link moved to %s after a rejected build", id)
	}
}

// A first install has no FPM config yet: its candidate is checked for modules
// only, and `php-fpm -t` is left to the apply that writes the config.
func TestFirstInstallDoesNotRunFPMTestBeforeItsConfigExists(t *testing.T) {
	m, _, run, p := newBuildRig(t, "[PHP Modules]\nCore\n")
	install(t, m)
	for _, c := range run.calls {
		if strings.Contains(c, "sbin/php-fpm -t -y "+p.FPMConf("8.3")) {
			t.Errorf("the first install ran php-fpm -t: %v", run.calls)
		}
	}
	if len(run.calls) == 0 || !strings.Contains(run.calls[0], "bin/php") {
		t.Errorf("the first install did not check the modules: %v", run.calls)
	}
}

func TestFirstPullThatFailsLeavesTheVersionFailedWithOutput(t *testing.T) {
	m, _, _, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
	exts, _ := ParseExtensions([]string{"intl"})
	if err := m.StartPull(ctx, "8.3"); err != nil {
		t.Fatal(err)
	}
	if err := m.Pull(ctx, "8.3", exts, nil); err == nil {
		t.Fatal("a build that does not load was installed")
	}
	v, _ := m.Get(ctx, "8.3")
	if v.State != StateFailed || !strings.Contains(v.Output, "does not load: intl") || v.Current != nil {
		t.Errorf("version = %+v", v)
	}
}

func TestRemoveIsRefusedWhileASiteUsesTheVersion(t *testing.T) {
	m, _, _, p := newBuildRig(t, "[PHP Modules]\nCore\n")
	install(t, m)

	used := func(context.Context) (bool, error) { return true, nil }
	if err := m.Remove(ctx, "8.3", used); !errors.Is(err, ErrInUse) {
		t.Fatalf("Remove with a site = %v, want ErrInUse", err)
	}
	if v, _ := m.Get(ctx, "8.3"); v.State != StateReady || v.Current == nil {
		t.Errorf("a refused removal changed the version: %+v", v)
	}

	free := func(context.Context) (bool, error) { return false, nil }
	if err := m.Remove(ctx, "8.3", free); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	v, _ := m.Get(ctx, "8.3")
	if v.State != StateAbsent || v.Current != nil {
		t.Errorf("after removal: %+v", v)
	}
	// The builds and the link are gone; the configuration tree is not.
	if _, err := os.Stat(filepath.FromSlash(p.VersionsDir("8.3"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the builds were kept: %v", err)
	}
	if _, err := os.Lstat(filepath.FromSlash(p.CurrentLink("8.3"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the link was kept: %v", err)
	}
	if _, err := os.Stat(filepath.FromSlash(p.Dir("8.3"))); err != nil {
		t.Errorf("the configuration tree was removed: %v", err)
	}
}

func TestActivateReturnsToAnEarlierBuild(t *testing.T) {
	m, _, _, p := newBuildRig(t, "[PHP Modules]\nCore\n")
	first := install(t, m)
	if _, err := m.Rebuild(ctx, "8.3", nil, nil); err != nil {
		t.Fatalf("Rebuild = %v", err)
	}
	if _, err := m.Activate(ctx, "8.3", first.Current.ID); err != nil {
		t.Fatalf("Activate = %v", err)
	}
	v, _ := m.Get(ctx, "8.3")
	if v.Current == nil || v.Current.ID != first.Current.ID {
		t.Errorf("current after activate = %+v, want %s", v.Current, first.Current.ID)
	}
	if id, _ := readLink(p, "8.3"); id != first.Current.ID {
		t.Errorf("the link names %s, want %s", id, first.Current.ID)
	}
	if _, err := m.Activate(ctx, "8.3", "not-an-id"); err == nil {
		t.Error("a malformed build id was accepted")
	}
}

func TestReconcileMarksAnInterruptedPullFailed(t *testing.T) {
	m, _, _, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
	if err := m.setState(ctx, Version{Version: "8.3", Image: "8.3", State: StatePulling}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	v, _ := m.Get(ctx, "8.3")
	if v.State != StateFailed || !strings.Contains(v.Output, "interrupted by a daemon restart") {
		t.Errorf("after reconcile: %+v", v)
	}
}
