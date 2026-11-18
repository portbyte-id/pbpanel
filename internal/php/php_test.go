package php

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
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
	testPaths = Paths{Root: "/www"}
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

// The spec is small enough to read as a golden value: every field decides what
// the container is.
func TestSpec(t *testing.T) {
	spec := Spec(testPaths, "8.3", "panel/php:8.3-test")
	if err := spec.Validate(testPaths.Root); err != nil {
		t.Fatalf("the php spec breaks the mount rule: %v", err)
	}
	cfg := spec.Config()
	got := strings.Join([]string{
		spec.Name, cfg.Image, cfg.NetworkMode, cfg.RestartPolicy, cfg.StopSignal,
		strings.Join(cfg.Entrypoint, " "), strings.Join(cfg.Cmd, " "),
		strings.Join(cfg.Env, " "), strings.Join(cfg.Binds, " "),
	}, "\n")
	want := strings.Join([]string{
		"panel-php-8.3", "panel/php:8.3-test", "host", "unless-stopped", "SIGQUIT",
		"php-fpm", "-F -y /www/panel/php/8.3/php-fpm.conf -c /www/panel/php/8.3/php.ini",
		"PHP_INI_SCAN_DIR=:/www/panel/php/8.3/conf.d",
		"/www/panel/php/8.3:/www/panel/php/8.3:ro /www/panel/run:/www/panel/run /www/panel/logs/sites:/www/panel/logs/sites /www/services/sites:/www/services/sites",
	}, "\n")
	if got != want {
		t.Errorf("spec:\n--- want\n%s\n--- got\n%s", want, got)
	}
	if Spec(testPaths, "8.3", "panel/php:8.3-test").Hash() == Spec(testPaths, "8.4", "panel/php:8.4-test").Hash() {
		t.Error("two versions share a spec hash")
	}
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
	if strings.Contains(string(Dockerfile("8.3", nil)), "EXPOSE") {
		t.Error("the derived image exposes a port")
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

type fakeEngine struct {
	mu        sync.Mutex
	container *docker.Container
	images    map[string]bool
	exec, run docker.ExecResult
	pullErr   error
	buildErr  error
	calls     []string
}

func (f *fakeEngine) log(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeEngine) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeEngine) ImageInspect(_ context.Context, ref string) (docker.Image, error) {
	if f.images != nil && !f.images[ref] {
		return docker.Image{}, docker.ErrNotFound
	}
	return docker.Image{ID: "sha256:x"}, nil
}
func (f *fakeEngine) ContainerInspect(context.Context, string) (docker.Container, error) {
	if f.container == nil {
		return docker.Container{}, docker.ErrNotFound
	}
	return *f.container, nil
}
func (f *fakeEngine) ContainerCreate(_ context.Context, name string, _ docker.ContainerConfig) (string, error) {
	f.log("create " + name)
	return "id", nil
}
func (f *fakeEngine) ContainerStart(_ context.Context, id string) error {
	f.log("start " + id)
	return nil
}
func (f *fakeEngine) ContainerStop(context.Context, string, time.Duration) error { return nil }
func (f *fakeEngine) ContainerRemove(context.Context, string, bool) error        { return nil }
func (f *fakeEngine) ContainerKill(_ context.Context, id, sig string) error {
	f.log("kill " + id + " " + sig)
	return nil
}
func (f *fakeEngine) Exec(_ context.Context, id string, o docker.ExecOptions) (docker.ExecResult, error) {
	f.log("exec " + id + " " + strings.Join(o.Cmd, " ") + " env=" + strings.Join(o.Env, ","))
	return f.exec, nil
}
func (f *fakeEngine) RunOnce(_ context.Context, name string, cfg docker.ContainerConfig) (docker.ExecResult, error) {
	f.log("run " + name + " " + strings.Join(append(cfg.Entrypoint, cfg.Cmd...), " "))
	if len(cfg.Entrypoint) > 0 && cfg.Entrypoint[0] == "cat" {
		return docker.ExecResult{Stdout: []byte("; production ini\n")}, nil
	}
	return f.run, nil
}
func (f *fakeEngine) ImagePull(_ context.Context, ref string, p docker.Progress) (docker.Image, error) {
	f.log("pull " + ref)
	p("pulling layer 1")
	if f.pullErr != nil {
		return docker.Image{}, f.pullErr
	}
	return docker.Image{ID: "sha256:base", RepoDigests: []string{"php@sha256:abc"}}, nil
}
func (f *fakeEngine) ImageBuild(_ context.Context, o docker.BuildOptions, p docker.Progress) (docker.Image, error) {
	f.log("build " + o.Tag)
	p("Step 1/2")
	return docker.Image{ID: "sha256:derived"}, f.buildErr
}

func running() *docker.Container {
	return &docker.Container{State: docker.ContainerState{Running: true, Status: "running", Pid: 7, StartedAt: time.Unix(100, 0)}}
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

type rig struct {
	m   *Manager
	eng *fakeEngine
	ap  *memApplier
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := &fakeEngine{}
	ap := &memApplier{files: map[string][]byte{}}
	m, err := New(Options{
		Store: st, Engine: eng, Applier: ap, Paths: testPaths, Read: ap.read,
		Scratch:   t.TempDir(),
		Supported: func(context.Context) ([]string, error) { return []string{"8.3", "8.4"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{m: m, eng: eng, ap: ap}
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

func (r *rig) state(t *testing.T, ver string) Version {
	t.Helper()
	v, err := r.m.Get(ctx, ver)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// --- service -----------------------------------------------------------------

func TestValidateExecsInTheRunningContainer(t *testing.T) {
	eng := &fakeEngine{container: running(), exec: docker.ExecResult{Stderr: []byte("NOTICE: configuration file test is successful\n")}}
	if _, err := NewService(eng, testPaths, "8.3", false, fixedImage).Validate(ctx); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	want := "exec panel-php-8.3 php-fpm -t -y /www/panel/php/8.3/php-fpm.conf -c /www/panel/php/8.3/php.ini env=PHP_INI_SCAN_DIR=:/www/panel/php/8.3/conf.d"
	if !eng.called(want) {
		t.Errorf("calls = %v, want %q", eng.calls, want)
	}
}

func TestValidateRunsAOneShotWhenTheContainerIsDown(t *testing.T) {
	eng := &fakeEngine{}
	if _, err := NewService(eng, testPaths, "8.3", false, fixedImage).Validate(ctx); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	if !eng.called("run panel-php-8.3-validate php-fpm -t -y ") {
		t.Errorf("calls = %v, want a one-shot php-fpm -t", eng.calls)
	}
}

func TestValidateRejects(t *testing.T) {
	for name, res := range map[string]docker.ExecResult{
		"non-zero exit":       {ExitCode: 78, Stderr: []byte("ERROR: failed to load configuration file\n")},
		"ini syntax, exit 0":  {Stderr: []byte("PHP:  syntax error, unexpected '=' in /www/panel/php/8.3/php.ini on line 3\nNOTICE: test is successful\n")},
		"bad extension":       {Stdout: []byte("\nWarning: PHP Startup: Unable to load dynamic library 'nope.so'\n")},
		"fpm error with zero": {Stderr: []byte("[17-Nov-2026 12:00:00] ERROR: [pool a] bad value\n")},
	} {
		eng := &fakeEngine{container: running(), exec: res}
		out, err := NewService(eng, testPaths, "8.3", false, fixedImage).Validate(ctx)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if !strings.Contains(out, res.Output()) {
			t.Errorf("%s: output = %q, want the validator's text verbatim", name, out)
		}
	}
	// A notice or a warning about an empty include pattern is not a rejection.
	ok := docker.ExecResult{Stderr: []byte("WARNING: Nothing matches the include pattern '/www/panel/php/8.3/pool.d/*.conf'\nNOTICE: configuration file test is successful\n")}
	if _, err := NewService(&fakeEngine{container: running(), exec: ok}, testPaths, "8.3", false, fixedImage).Validate(ctx); err != nil {
		t.Errorf("a clean test was rejected: %v", err)
	}
}

func TestReloadSignalsARunningContainer(t *testing.T) {
	eng := &fakeEngine{container: running()}
	if err := NewService(eng, testPaths, "8.3", false, fixedImage).Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if !eng.called("kill panel-php-8.3 USR2") {
		t.Errorf("calls = %v, want USR2", eng.calls)
	}
}

// Step 2.7: a version nobody uses stays down through a settings edit, and
// comes up with its first site.
func TestReloadStartsTheContainerOnlyWhenAsked(t *testing.T) {
	eng := &fakeEngine{}
	plain := NewService(eng, testPaths, "8.3", false, fixedImage)
	if err := plain.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := plain.Healthy(ctx); err != nil {
		t.Fatalf("a version with no container is not unhealthy: %v", err)
	}
	if len(eng.calls) != 0 {
		t.Fatalf("a plain reload touched Docker: %v", eng.calls)
	}

	starting := NewService(eng, testPaths, "8.3", true, fixedImage)
	if err := starting.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if !eng.called("create panel-php-8.3") || !eng.called("start id") {
		t.Errorf("calls = %v, want create and start", eng.calls)
	}
	if err := starting.Healthy(ctx); err == nil {
		t.Error("a starting service reported healthy with no container running")
	}
}

type restartingEngine struct {
	fakeEngine
	n int
}

func (r *restartingEngine) ContainerInspect(context.Context, string) (docker.Container, error) {
	r.n++
	c := *r.container
	c.State.StartedAt = time.Unix(int64(100*r.n), 0)
	return c, nil
}

func TestHealthyCatchesARestart(t *testing.T) {
	eng := &restartingEngine{fakeEngine: fakeEngine{container: running()}}
	svc := NewService(eng, testPaths, "8.3", false, fixedImage)
	svc.wait = time.Millisecond
	if err := svc.Healthy(ctx); err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Errorf("Healthy = %v, want a restart to be reported", err)
	}
}

// --- catalogue ---------------------------------------------------------------

func TestListShowsSupportedVersionsAsAbsent(t *testing.T) {
	r := newRig(t)
	list, err := r.m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Version != "8.3" || list[0].State != StateAbsent || !list[0].Supported || list[0].Image != "php:8.3-fpm" {
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
	if v.State != StateReady || v.Digest != "php@sha256:abc" || v.Output != "" || v.UpdatedAt == nil {
		t.Errorf("after Pull: %+v", v)
	}
	if !r.eng.called("pull php:8.3-fpm") || !r.eng.called("build panel/php:8.3-") || len(lines) != 2 {
		t.Errorf("calls = %v, lines = %v", r.eng.calls, lines)
	}
	if err := r.m.Usable(ctx, "8.3"); err != nil {
		t.Errorf("Usable after a pull = %v", err)
	}
}

func TestFailedPullLeavesFailedWithOutput(t *testing.T) {
	for name, set := range map[string]func(*fakeEngine){
		"pull":  func(e *fakeEngine) { e.pullErr = errors.New("manifest unknown") },
		"build": func(e *fakeEngine) { e.buildErr = errors.New("groupadd failed") },
	} {
		r := newRig(t)
		set(r.eng)
		if err := r.m.StartPull(ctx, "8.4"); err != nil {
			t.Fatal(err)
		}
		if err := r.m.Pull(ctx, "8.4", nil, nil); err == nil {
			t.Fatalf("%s: Pull succeeded", name)
		}
		v := r.state(t, "8.4")
		if v.State != StateFailed || !strings.Contains(v.Output, "pulling layer 1") ||
			(!strings.Contains(v.Output, "manifest unknown") && !strings.Contains(v.Output, "groupadd failed")) {
			t.Errorf("%s: %+v, want failed with the output and the error", name, v)
		}
		// A failed version can be pulled again.
		r.eng.pullErr, r.eng.buildErr = nil, nil
		r.pull(t, "8.4")
		if got := r.state(t, "8.4"); got.State != StateReady || got.Output != "" {
			t.Errorf("%s: retry left %+v", name, got)
		}
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
	r.eng.images = map[string]bool{} // every image is gone
	if err := r.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.state(t, "8.3").State; got != StateAbsent {
		t.Errorf("8.3 = %s, want absent: its image is gone", got)
	}
	if got := r.state(t, "8.4"); got.State != StateFailed || !strings.Contains(got.Output, "interrupted") {
		t.Errorf("8.4 = %+v, want failed: its pull was interrupted", got)
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
	want := "/www/panel/php/8.3/php-fpm.conf /www/panel/php/8.3/conf.d/zz-panel.ini /www/panel/php/8.3/php.ini"
	if strings.Join(paths, " ") != want {
		t.Errorf("first BaseFiles = %v", paths)
	}
	if string(r.ap.files["/www/panel/php/8.3/php.ini"]) != "; production ini\n" {
		t.Error("php.ini was not copied from the image")
	}

	// The operator edits php.ini; it is theirs from here on.
	r.ap.files["/www/panel/php/8.3/php.ini"] = []byte("; mine\n")
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
	if !strings.Contains(string(r.ap.files["/www/panel/php/8.3/conf.d/zz-panel.ini"]), "memory_limit = 512M") {
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
	if seen["/www/panel/php/8.3/php.ini"] != 1 || seen["/www/panel/php/8.3/php-fpm.conf"] != 1 {
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

// fixedImage is the image a test service runs.
var fixedImage ImageFunc = func(context.Context) (string, error) { return "panel/php:8.3-test", nil }

// ImageTag and ImageRemove complete the fake as a php.Engine. The fake keeps
// no tags: a build is found by inspect whenever the test set no image map.
func (f *fakeEngine) ImageTag(_ context.Context, src, dst string) error {
	f.log("tag " + src + " " + dst)
	return nil
}

func (f *fakeEngine) ImageRemove(_ context.Context, ref string) error {
	f.log("rmi " + ref)
	return nil
}
