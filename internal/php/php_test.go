package php

import (
	"context"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
	"github.com/portbyte-id/server-panel/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, got []byte, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file.\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

var (
	testPaths = Paths{Root: "/www", DataDir: "/www/panel/data"}
	ctx       = context.Background()
)

func testPool() Pool {
	return Pool{Name: "a_test", Domain: "a.test", Version: "8.3", Root: "/www/services/sites/a.test", Settings: DefaultPoolSettings()}
}

func TestGoldenFiles(t *testing.T) {
	b, err := testPaths.RenderFPMConf("8.3")
	golden(t, "php-fpm.conf", b, err)
	b, err = RenderFormIni(DefaultSettings())
	golden(t, "zz-panel.ini", b, err)
	b, err = testPaths.RenderPool(testPool())
	golden(t, "pool-dynamic.conf", b, err)

	od := testPool()
	od.Settings.PM = "ondemand"
	b, err = testPaths.RenderPool(od)
	golden(t, "pool-ondemand.conf", b, err)

	st := testPool()
	st.Settings.PM = "static"
	b, err = testPaths.RenderPool(st)
	golden(t, "pool-static.conf", b, err)
}

// No rendered file may open a TCP listener: every `listen` is a socket path.
func TestNoTCPListener(t *testing.T) {
	conf, _ := testPaths.RenderFPMConf("8.3")
	pool, _ := testPaths.RenderPool(testPool())
	for _, line := range strings.Split(string(conf)+string(pool), "\n") {
		if v, ok := strings.CutPrefix(line, "listen = "); ok && !strings.HasPrefix(v, "/www/panel/run/") {
			t.Errorf("listener outside the socket directory: %q", line)
		}
	}
}

func TestPoolName(t *testing.T) {
	long := strings.Repeat("a", 30) + "." + strings.Repeat("b", 30) + ".test"
	for domain, want := range map[string]string{
		"a.test":             "a_test",
		"my-shop.example.co": "my_shop_example_co",
		"xn--bcher-kva.test": "xn__bcher_kva_test",
		"UPPER.test":         "upper_test",
	} {
		if got := PoolName(domain); got != want {
			t.Errorf("PoolName(%q) = %q, want %q", domain, got, want)
		}
	}
	got := PoolName(long)
	if len(got) != maxPoolName || !poolNameRe.MatchString(got) {
		t.Errorf("PoolName(long) = %q (%d chars), want a valid name of %d", got, len(got), maxPoolName)
	}
	if other := PoolName(long + "x"); other == got {
		t.Error("two long domains with the same beginning share a pool name")
	}
	// The mapping is not injective; the collision is the caller's to reject.
	if PoolName("a-b.test") != PoolName("a.b.test") {
		t.Error("expected a-b.test and a.b.test to collide")
	}
	if socket := testPaths.Socket("8.3", got); len(socket) > 100 {
		t.Errorf("socket path %q is too long for a Unix address", socket)
	}
}

func TestSettingsValidate(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	for field, mutate := range map[string]func(*Settings){
		"memory_limit":        func(s *Settings) { s.MemoryLimit = "256M\nextension=evil.so" },
		"upload_max_filesize": func(s *Settings) { s.UploadMaxFilesize = "lots" },
		"post_max_size":       func(s *Settings) { s.PostMaxSize = "" },
		"max_execution_time":  func(s *Settings) { s.MaxExecutionTime = -1 },
		"date_timezone":       func(s *Settings) { s.DateTimezone = "UTC; x" },
	} {
		s := DefaultSettings()
		mutate(&s)
		var ie *InputError
		if err := s.Validate(); !errors.As(err, &ie) || ie.Field != field {
			t.Errorf("%s: Validate = %v, want an InputError on that field", field, err)
		}
		if _, err := RenderFormIni(s); err == nil {
			t.Errorf("%s: an invalid form was rendered", field)
		}
	}
	s := DefaultSettings()
	s.MemoryLimit, s.DateTimezone = "-1", "America/Argentina/Buenos_Aires"
	if err := s.Validate(); err != nil {
		t.Errorf("valid settings rejected: %v", err)
	}
}

func TestPoolSettingsValidate(t *testing.T) {
	for field, mutate := range map[string]func(*PoolSettings){
		"pm":                func(s *PoolSettings) { s.PM = "fast" },
		"max_children":      func(s *PoolSettings) { s.MaxChildren = 0 },
		"min_spare_servers": func(s *PoolSettings) { s.MinSpareServers = 9 },
		"max_spare_servers": func(s *PoolSettings) { s.MaxSpareServers = 0 },
		"start_servers":     func(s *PoolSettings) { s.StartServers = 5 },
		"max_requests":      func(s *PoolSettings) { s.MaxRequests = -1 },
		"idle_timeout":      func(s *PoolSettings) { s.IdleTimeout = 0 },
	} {
		s := DefaultPoolSettings()
		mutate(&s)
		var ie *InputError
		if err := s.Validate(); !errors.As(err, &ie) || ie.Field != field {
			t.Errorf("%s: Validate = %v, want an InputError on that field", field, err)
		}
	}
	// The spare-server relations only bind a dynamic pool.
	s := PoolSettings{PM: "static", MaxChildren: 3, IdleTimeout: 10}
	if err := s.Validate(); err != nil {
		t.Errorf("static pool rejected: %v", err)
	}
}

func TestRenderPoolRefusesUnsafeValues(t *testing.T) {
	for name, mutate := range map[string]func(*Pool){
		"root outside":    func(p *Pool) { p.Root = "/etc" },
		"root with ini":   func(p *Pool) { p.Root = "/www/services/sites/a\nuser = root" },
		"reserved name":   func(p *Pool) { p.Name = idlePool },
		"name with dot":   func(p *Pool) { p.Name = "a.test" },
		"bad version":     func(p *Pool) { p.Version = "8.3/../x" },
		"domain with [":   func(p *Pool) { p.Domain = "a]\n[global" },
		"invalid setting": func(p *Pool) { p.Settings.PM = "x" },
	} {
		p := testPool()
		mutate(&p)
		if _, err := testPaths.RenderPool(p); err == nil {
			t.Errorf("%s: the pool was rendered", name)
		}
	}
}

func TestParseVersions(t *testing.T) {
	got, err := ParseVersions(" 8.4, 8.2,8.10 ,8.2")
	if err != nil || strings.Join(got, ",") != "8.2,8.4,8.10" {
		t.Errorf("ParseVersions = %v, %v", got, err)
	}
	for _, bad := range []string{"", "8", "8.3-fpm", "latest", "../8.3"} {
		if _, err := ParseVersions(bad); err == nil {
			t.Errorf("ParseVersions(%q) was accepted", bad)
		}
	}
}

// --- fakes -------------------------------------------------------------------

// fakeBuild is the build hook: it makes a build directory with both programs in
// it, as the native builder leaves one, and records what it was asked for.
type fakeBuild struct {
	p     Paths
	calls []string
	err   error
}

func (f *fakeBuild) build(_ context.Context, id, ver string, exts []Extension, progress docker.Progress) error {
	f.calls = append(f.calls, "build "+ver+" "+id+" "+strings.Join(ExtensionNames(exts), ","))
	if progress != nil {
		progress("building php " + ver)
	}
	if f.err != nil {
		return f.err
	}
	prefix := filepath.FromSlash(f.p.BuildPrefix(ver, id))
	for _, rel := range []string{"sbin/php-fpm", "bin/php"} {
		file := filepath.Join(prefix, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(prefix, "etc"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(prefix, "version"), []byte("8.3.37\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(prefix, "source-sha256"), []byte("3d678a41fd5aade34760127fef5dd64fd7a77848913fc0f70dde0cf467c94427\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(prefix, "etc", "php.ini-production"), []byte("; production ini\n"), 0o644)
}

// hostRunner answers the host commands of the manager rig: the two validators,
// and systemctl. service_test.go's fakeRunner is the scriptable one the service
// and runtime tests use.
type hostRunner struct {
	calls   []string
	envs    []string
	modules string
	// active is what systemctl says about every unit; fail makes every command
	// fail.
	active bool
	fail   bool
}

func (f *hostRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunEnv(ctx, nil, name, args...)
}

func (f *hostRunner) RunEnv(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	f.envs = append(f.envs, strings.Join(env, ","))
	if f.fail {
		return []byte("boom"), errors.New("exit status 1")
	}
	switch {
	case name == "systemctl" && len(args) > 0 && args[0] == "show":
		state, pid, since := "inactive", 0, "0"
		if f.active {
			state, pid, since = "active", 7, "100"
		}
		return show("loaded", state, pid, since), nil
	case strings.HasSuffix(name, "/sbin/php-fpm"):
		return []byte("NOTICE: configuration file test is successful\n"), nil
	case strings.HasSuffix(name, "/bin/php"):
		return []byte(f.modules), nil
	}
	return nil, nil
}

// memApplier records changes and writes them into a map, or fails them.
type memApplier struct {
	files   map[string][]byte
	err     error
	applied []confedit.Change
}

func (a *memApplier) Apply(_ context.Context, ch confedit.Change) (confedit.Result, error) {
	a.applied = append(a.applied, ch)
	if a.err != nil {
		return confedit.Result{}, a.err
	}
	for _, f := range ch.Files {
		a.files[f.Path] = f.Content
	}
	return confedit.Result{}, nil
}

func (a *memApplier) read(name string) ([]byte, error) {
	if b, ok := a.files[name]; ok {
		return b, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// rig is a Manager over a temporary root, with the build, the host commands and
// the applier faked.
type rig struct {
	m   *Manager
	b   *fakeBuild
	run *hostRunner
	ap  *memApplier
	p   Paths
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigAt(t, tempPaths(t))
}

// newRigAt is newRig over a given Paths layout, so a test can name the files it
// expects.
func newRigAt(t *testing.T, p Paths) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	b := &fakeBuild{p: p}
	run := &hostRunner{modules: "[PHP Modules]\nCore\n"}
	ap := &memApplier{files: map[string][]byte{}}
	m, err := New(Options{
		Store: st, Engine: &fakeEngine{}, Applier: ap, Paths: p, Read: ap.read,
		Build: b.build, Run: run, UnitDir: t.TempDir(),
		Supported: func(context.Context) ([]string, error) { return []string{"8.3", "8.4"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{m: m, b: b, run: run, ap: ap, p: p}
}

// fakeEngine is what php.Options still requires for the Docker client, which is
// now only the native builder's. The rig replaces the build hook, so nothing in
// the runtime path calls it: a test that reaches it has found a leak and says so
// rather than silently passing.
type fakeEngine struct{}

func (fakeEngine) ImageBuild(context.Context, docker.BuildOptions, docker.Progress) (docker.Image, error) {
	return docker.Image{}, errors.New("the native builder builds its own image")
}
func (fakeEngine) ContainerCreate(context.Context, string, docker.ContainerConfig) (string, error) {
	return "", errors.New("the runtime path must not create containers")
}
func (fakeEngine) ContainerArchive(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("the runtime path must not archive a container")
}
func (fakeEngine) ContainerRemove(context.Context, string, bool) error {
	return errors.New("the runtime path must not remove containers")
}
func (fakeEngine) ImageRemove(context.Context, string) error {
	return errors.New("the runtime path must not remove images")
}

func (r *rig) pull(t *testing.T, ver string) {
	t.Helper()
	if err := r.m.StartPull(ctx, ver); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Pull(ctx, ver, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// TestManagerRetireHookFiresOnTheFirstStart wires the retire hook through the
// manager the way cmd/panel does: the first start of a version retires its old
// container before systemd starts the unit, a start while the unit runs does
// not retire again, and both confedit services carry the hook for the versions
// they start.
func TestManagerRetireHookFiresOnTheFirstStart(t *testing.T) {
	r := newRig(t)
	var retired []string
	r.m.retireFn = func(_ context.Context, ver string) error {
		retired = append(retired, ver)
		return nil
	}
	r.pull(t, "8.3")

	r.run.active = false
	if _, err := r.m.EnsureRunning(ctx, "8.3"); err != nil {
		t.Fatal(err)
	}
	if len(retired) != 1 || retired[0] != "8.3" {
		t.Fatalf("retire hook = %v, want one call for 8.3", retired)
	}
	startAt := slices.Index(r.run.calls, "systemctl start panel-php-8.3.service")
	if startAt < 0 {
		t.Fatalf("the version was not started: %v", r.run.calls)
	}

	// While the unit runs the hook stays idle: the container is gone by now and
	// a running version must not be disturbed.
	r.run.active = true
	if _, err := r.m.EnsureRunning(ctx, "8.3"); err != nil {
		t.Fatal(err)
	}
	if len(retired) != 1 {
		t.Errorf("retire hook = %v, want it to run on the first start only", retired)
	}

	// The confedit service the sites model applies with reaches the same hook,
	// start flag or not: only a start runs it, but it must be wired on both.
	for _, start := range []bool{true, false} {
		svc, ok := r.m.Service("8.4", start).(*Service)
		if !ok {
			t.Fatalf("Service returned %T", svc)
		}
		if svc.rt.retire == nil {
			t.Errorf("the service (start=%v) has no retire hook", start)
		}
	}
}

func (r *rig) state(t *testing.T, ver string) Version {
	t.Helper()
	v, err := r.m.Get(ctx, ver)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// --- catalogue ---------------------------------------------------------------

func TestListShowsSupportedVersionsAsAbsent(t *testing.T) {
	r := newRig(t)
	list, err := r.m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Version != "8.3" || list[0].State != StateAbsent || !list[0].Supported || list[0].Image != "8.3" {
		t.Errorf("List = %+v", list)
	}
	if _, err := r.m.Get(ctx, "7.4"); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("Get(7.4) = %v, want ErrUnknownVersion", err)
	}
	if err := r.m.Usable(ctx, "8.3"); !errors.Is(err, ErrNotReady) {
		t.Errorf("Usable = %v, want ErrNotReady", err)
	}
}

func TestPullStateTransitions(t *testing.T) {
	r := newRig(t)
	if err := r.m.StartPull(ctx, "8.3"); err != nil {
		t.Fatal(err)
	}
	if got := r.state(t, "8.3").State; got != StatePulling {
		t.Fatalf("after StartPull: %s, want pulling", got)
	}
	if err := r.m.StartPull(ctx, "8.3"); !errors.Is(err, ErrBusy) {
		t.Errorf("second StartPull = %v, want ErrBusy", err)
	}
	var lines []string
	if err := r.m.Pull(ctx, "8.3", nil, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	v := r.state(t, "8.3")
	if v.State != StateReady || v.Image != "8.3.37" || v.Digest != "3d678a41fd5aade34760127fef5dd64fd7a77848913fc0f70dde0cf467c94427" ||
		v.Output != "" || v.UpdatedAt == nil {
		t.Errorf("after Pull: %+v", v)
	}
	if len(r.b.calls) != 1 || !strings.HasPrefix(r.b.calls[0], "build 8.3 ") || len(lines) != 1 {
		t.Errorf("builds = %v, lines = %v", r.b.calls, lines)
	}
	if err := r.m.Usable(ctx, "8.3"); err != nil {
		t.Errorf("Usable after a pull = %v", err)
	}
}

func TestFailedPullLeavesFailedWithOutput(t *testing.T) {
	r := newRig(t)
	r.b.err = errors.New("the command returned a non-zero code: 2")
	if err := r.m.StartPull(ctx, "8.4"); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Pull(ctx, "8.4", nil, nil); err == nil {
		t.Fatal("Pull succeeded")
	}
	v := r.state(t, "8.4")
	if v.State != StateFailed || !strings.Contains(v.Output, "building php 8.4") ||
		!strings.Contains(v.Output, "the command returned a non-zero code") {
		t.Errorf("%+v, want failed with the output and the error", v)
	}
	// A failed version can be built again.
	r.b.err = nil
	r.pull(t, "8.4")
	if got := r.state(t, "8.4"); got.State != StateReady || got.Output != "" {
		t.Errorf("retry left %+v", got)
	}
}

func TestStartPullRefusesAnUnsupportedVersion(t *testing.T) {
	r := newRig(t)
	if err := r.m.StartPull(ctx, "7.4"); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("StartPull(7.4) = %v", err)
	}
	if err := r.m.StartPull(ctx, "8.3; rm"); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("StartPull(garbage) = %v", err)
	}
}

func TestReconcileCorrectsTheCatalogue(t *testing.T) {
	r := newRig(t)
	r.pull(t, "8.3")
	if err := r.m.StartPull(ctx, "8.4"); err != nil {
		t.Fatal(err)
	}
	// 8.3's build directory is gone; 8.4 was mid-install.
	if err := os.RemoveAll(filepath.FromSlash(r.p.VersionsDir("8.3"))); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.state(t, "8.3").State; got != StateAbsent {
		t.Errorf("8.3 = %s, want absent: its build is gone", got)
	}
	if got := r.state(t, "8.4"); got.State != StateFailed || !strings.Contains(got.Output, "interrupted") {
		t.Errorf("8.4 = %+v, want failed: its install was interrupted", got)
	}
}

// The link is the only thing a swap changes, so the boot reconcile points it back
// at the build the catalogue serves: a host whose link was lost still starts the
// right binary.
func TestReconcileRepairsTheCurrentLink(t *testing.T) {
	r := newRig(t)
	r.pull(t, "8.3")
	cur, err := r.m.CurrentTag(ctx, "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.FromSlash(r.p.CurrentLink("8.3"))); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := readLink(r.p, "8.3")
	if err != nil {
		t.Fatalf("the link was not repaired: %v", err)
	}
	if id, _ := native.IDOfTag(cur, product); got != id {
		t.Errorf("the link names %s, want %s", got, id)
	}
}

// --- files and settings ------------------------------------------------------

func TestBaseFilesProvisionOnceAndLeavePHPIniAlone(t *testing.T) {
	r := newRig(t)
	r.pull(t, "8.3")
	files, err := r.m.BaseFiles(ctx, "8.3")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		r.ap.files[f.Path] = f.Content
	}
	want := r.p.FPMConf("8.3") + " " + r.p.FormIni("8.3") + " " + r.p.IniPath("8.3")
	if strings.Join(paths, " ") != want {
		t.Errorf("first BaseFiles = %v, want %s", paths, want)
	}
	if string(r.ap.files[r.p.IniPath("8.3")]) != "; production ini\n" {
		t.Error("php.ini was not copied from the build")
	}

	// The operator edits php.ini; it is theirs from here on.
	r.ap.files[r.p.IniPath("8.3")] = []byte("; mine\n")
	if files, err = r.m.BaseFiles(ctx, "8.3"); err != nil || len(files) != 0 {
		t.Errorf("second BaseFiles = %v, %v, want nothing to write", files, err)
	}
}

func TestSetSettingsAppliesAndRollsBack(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.SetSettings(ctx, "8.3", DefaultSettings()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("SetSettings on an absent version = %v", err)
	}
	r.pull(t, "8.3")

	s := DefaultSettings()
	s.MemoryLimit = "512M"
	if _, err := r.m.SetSettings(ctx, "8.3", s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.ap.files[r.p.FormIni("8.3")]), "memory_limit = 512M") {
		t.Error("the form's ini was not written")
	}
	if got := r.ap.applied[0].Services[0].Name(); got != "php-8.3" {
		t.Errorf("service = %s", got)
	}

	r.ap.err = &confedit.ApplyError{Stage: confedit.StageValidate, Service: "php-8.3", Output: "bad", Err: errors.New("rejected")}
	s.MemoryLimit = "1G"
	if _, err := r.m.SetSettings(ctx, "8.3", s); err == nil {
		t.Fatal("a rejected apply was reported as success")
	}
	if got, _ := r.m.Settings(ctx, "8.3"); got.MemoryLimit != "512M" {
		t.Errorf("stored memory_limit = %s after a rejected apply, want 512M", got.MemoryLimit)
	}

	s.MemoryLimit = "1G; evil"
	var ie *InputError
	if _, err := r.m.SetSettings(ctx, "8.3", s); !errors.As(err, &ie) {
		t.Errorf("invalid form = %v, want an InputError", err)
	}
}

func TestIniReadAndWrite(t *testing.T) {
	r := newRig(t)
	r.pull(t, "8.3")
	b, err := r.m.ReadIni(ctx, "8.3")
	if err != nil || string(b) != "; production ini\n" {
		t.Fatalf("ReadIni before the first write = %q, %v, want the image's file", b, err)
	}
	if _, err := r.m.WriteIni(ctx, "8.3", []byte("memory_limit = 1G\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ = r.m.ReadIni(ctx, "8.3"); string(b) != "memory_limit = 1G\n" {
		t.Errorf("ReadIni = %q", b)
	}
	// The same apply provisions the panel-owned files, with php.ini listed once.
	seen := map[string]int{}
	for _, f := range r.ap.applied[0].Files {
		seen[f.Path]++
	}
	if seen[r.p.IniPath("8.3")] != 1 || seen[r.p.FPMConf("8.3")] != 1 {
		t.Errorf("files = %v", seen)
	}

	r.ap.err = &confedit.ApplyError{Stage: confedit.StageValidate, Err: errors.New("rejected")}
	if _, err := r.m.WriteIni(ctx, "8.3", []byte("= = =\n")); err == nil {
		t.Error("a rejected ini was reported as written")
	}
	if b, _ = r.m.ReadIni(ctx, "8.3"); string(b) != "memory_limit = 1G\n" {
		t.Errorf("php.ini after a rejected write = %q", b)
	}
}
