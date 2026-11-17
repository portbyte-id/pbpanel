package php

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/store"
)

// moduleEngine answers `php -m` with a fixed module list, which is what the
// candidate check reads. Everything else is the plain fake.
type moduleEngine struct {
	*fakeEngine
	modules string
}

func (e *moduleEngine) RunOnce(ctx context.Context, name string, cfg docker.ContainerConfig) (docker.ExecResult, error) {
	if len(cfg.Entrypoint) == 1 && cfg.Entrypoint[0] == "php" {
		e.log("php-m " + name)
		return docker.ExecResult{Stdout: []byte(e.modules)}, nil
	}
	return e.fakeEngine.RunOnce(ctx, name, cfg)
}

// newBuildRig is a manager whose build contexts go to a temporary directory and
// whose `php -m` lists the given modules.
func newBuildRig(t *testing.T, modules string) (*Manager, *moduleEngine, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := &moduleEngine{fakeEngine: &fakeEngine{}, modules: modules}
	ap := &memApplier{files: map[string][]byte{}}
	scratch := t.TempDir()
	m, err := New(Options{
		Store: st, Engine: eng, Applier: ap, Paths: testPaths, Read: ap.read, Scratch: scratch,
		Supported: func(context.Context) ([]string, error) { return []string{"8.3", "8.4"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, eng, scratch
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

func TestPullWithExtensionsWritesItsDockerfileAndServes(t *testing.T) {
	m, eng, scratch := newBuildRig(t, "[PHP Modules]\nCore\nintl\nredis\n")
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
	files, _ := filepath.Glob(filepath.Join(scratch, "8.3", "*", "Dockerfile"))
	if len(files) != 1 {
		t.Fatalf("Dockerfiles = %v, want one", files)
	}
	raw, err := readFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docker-php-ext-install -j$(nproc) intl", "pecl install redis", "libicu-dev"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Dockerfile lacks %q:\n%s", want, raw)
		}
	}
	if !eng.called("php-m ") || !eng.called("build "+v.Current.Tag) {
		t.Errorf("calls = %v", eng.calls)
	}
}

func TestCandidateMissingAnExtensionIsRejectedAndTheOldBuildServes(t *testing.T) {
	m, eng, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
	first := install(t, m)

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
	if !eng.called("rmi " + builds[0].Tag) {
		t.Error("the rejected candidate's image was not removed")
	}
}

// A first install has no FPM config yet: its candidate is checked for modules
// only, and `php-fpm -t` is left to the apply that writes the config.
func TestFirstInstallDoesNotRunFPMTestBeforeItsConfigExists(t *testing.T) {
	m, eng, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
	install(t, m)
	if eng.called("run panel-php-8.3-candidate php-fpm") {
		t.Errorf("the first install ran php-fpm -t: %v", eng.calls)
	}
	if !eng.called("php-m panel-php-8.3-candidate") {
		t.Errorf("the first install did not check the modules: %v", eng.calls)
	}
}

func TestFirstPullThatFailsLeavesTheVersionFailedWithOutput(t *testing.T) {
	m, _, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
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
	m, _, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
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
}

func TestActivateReturnsToAnEarlierBuild(t *testing.T) {
	m, _, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
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
	if _, err := m.Activate(ctx, "8.3", "not-an-id"); err == nil {
		t.Error("a malformed build id was accepted")
	}
}

func TestReconcileAdoptsTheLegacyImageOfAReadyVersion(t *testing.T) {
	m, eng, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
	eng.images = map[string]bool{LegacyImage("8.3"): true}
	if err := m.setState(ctx, Version{Version: "8.3", Image: BaseImage("8.3"), State: StateReady}); err != nil {
		t.Fatal(err)
	}

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile = %v", err)
	}
	v, _ := m.Get(ctx, "8.3")
	if v.State != StateReady || v.Current == nil || !strings.HasPrefix(v.Current.Output, "adopted from") {
		t.Errorf("after reconcile: %+v", v)
	}
	if !eng.called("tag " + LegacyImage("8.3") + " " + v.Current.Tag) {
		t.Errorf("the legacy image was not given the build tag; calls = %v", eng.calls)
	}
}

func TestReconcileMarksAnInterruptedPullFailed(t *testing.T) {
	m, _, _ := newBuildRig(t, "[PHP Modules]\nCore\n")
	if err := m.setState(ctx, Version{Version: "8.3", Image: BaseImage("8.3"), State: StatePulling}); err != nil {
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

// readFile returns a file the test wrote.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
