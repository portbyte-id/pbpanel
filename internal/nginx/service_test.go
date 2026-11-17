package nginx

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/store"
)

// fakeEngine records the calls the nginx service makes.
type fakeEngine struct {
	container *docker.Container // nil: no container
	exec      docker.ExecResult
	run       docker.ExecResult
	execErr   error

	calls []string
	// inspects is consumed one per ContainerInspect when non-empty, so a test
	// can show a container that changes between two inspections.
	inspects []docker.Container
}

func (f *fakeEngine) ImageInspect(context.Context, string) (docker.Image, error) {
	return docker.Image{ID: "sha256:x"}, nil
}

func (f *fakeEngine) ContainerInspect(context.Context, string) (docker.Container, error) {
	f.calls = append(f.calls, "inspect")
	if len(f.inspects) > 0 {
		c := f.inspects[0]
		f.inspects = f.inspects[1:]
		return c, nil
	}
	if f.container == nil {
		return docker.Container{}, docker.ErrNotFound
	}
	return *f.container, nil
}

func (f *fakeEngine) ContainerCreate(context.Context, string, docker.ContainerConfig) (string, error) {
	return "id", nil
}
func (f *fakeEngine) ContainerStart(context.Context, string) error { return nil }
func (f *fakeEngine) ContainerStop(context.Context, string, time.Duration) error {
	return nil
}
func (f *fakeEngine) ContainerRemove(_ context.Context, id string, _ bool) error {
	f.calls = append(f.calls, "remove "+id)
	return nil
}
func (f *fakeEngine) ContainerKill(_ context.Context, id, sig string) error {
	f.calls = append(f.calls, "kill "+id+" "+sig)
	return nil
}
func (f *fakeEngine) Exec(_ context.Context, id string, o docker.ExecOptions) (docker.ExecResult, error) {
	f.calls = append(f.calls, "exec "+id+" "+strings.Join(o.Cmd, " "))
	return f.exec, f.execErr
}
func (f *fakeEngine) RunOnce(_ context.Context, name string, cfg docker.ContainerConfig) (docker.ExecResult, error) {
	f.calls = append(f.calls, "run "+name+" "+strings.Join(append(cfg.Entrypoint, cfg.Cmd...), " ")+" binds="+strings.Join(cfg.Binds, ","))
	return f.run, nil
}
func (f *fakeEngine) ImageBuild(context.Context, docker.BuildOptions, docker.Progress) (docker.Image, error) {
	return docker.Image{}, nil
}

func running() *docker.Container {
	return &docker.Container{State: docker.ContainerState{Running: true, Status: "running", Pid: 7, StartedAt: time.Unix(100, 0)}}
}

func TestValidateExecsInTheRunningContainer(t *testing.T) {
	eng := &fakeEngine{container: running(), exec: docker.ExecResult{Stderr: []byte("nginx: test is successful\n")}}
	out, err := NewService(eng, testPaths, fixedNginxImage).Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate = %v", err)
	}
	if !strings.Contains(out, "successful") {
		t.Errorf("output = %q", out)
	}
	want := "exec panel-nginx nginx -t -c /www/panel/nginx/nginx.conf"
	if !contains(eng.calls, want) {
		t.Errorf("calls = %v, want %q", eng.calls, want)
	}
}

func TestValidateRunsAOneShotWhenNginxIsDown(t *testing.T) {
	for name, c := range map[string]*docker.Container{
		"no container": nil,
		"stopped":      {State: docker.ContainerState{Status: "exited"}},
	} {
		eng := &fakeEngine{container: c}
		if _, err := NewService(eng, testPaths, fixedNginxImage).Validate(context.Background()); err != nil {
			t.Fatalf("%s: Validate = %v", name, err)
		}
		var ran string
		for _, call := range eng.calls {
			if strings.HasPrefix(call, "run ") {
				ran = call
			}
		}
		if !strings.Contains(ran, "nginx -t -c /www/panel/nginx/nginx.conf") ||
			!strings.Contains(ran, "/www/panel/nginx:/www/panel/nginx:ro") {
			t.Errorf("%s: one-shot call = %q, want nginx -t with the container's mounts", name, ran)
		}
		if !contains(eng.calls, "remove "+validateName) {
			t.Errorf("%s: a stale validation container was not cleared first: %v", name, eng.calls)
		}
	}
}

func TestValidateReturnsNginxOutputVerbatim(t *testing.T) {
	msg := "nginx: [emerg] unknown directive \"bogus\" in /www/panel/nginx/sites/a.test.conf:3\n"
	eng := &fakeEngine{container: running(), exec: docker.ExecResult{ExitCode: 1, Stderr: []byte(msg)}}
	out, err := NewService(eng, testPaths, fixedNginxImage).Validate(context.Background())
	if err == nil {
		t.Fatal("a rejected config validated")
	}
	if out != msg {
		t.Errorf("output = %q, want %q", out, msg)
	}
}

func TestValidateEngineFailureIsNotARejection(t *testing.T) {
	eng := &fakeEngine{container: running(), execErr: errors.New("socket closed")}
	out, err := NewService(eng, testPaths, fixedNginxImage).Validate(context.Background())
	if err == nil || out != "" || !strings.Contains(err.Error(), "could not be run") {
		t.Errorf("Validate = %q, %v; want an error saying nginx -t could not be run", out, err)
	}
}

func TestReloadSignalsTheMasterAndRefusesWhenDown(t *testing.T) {
	eng := &fakeEngine{container: running()}
	if err := NewService(eng, testPaths, fixedNginxImage).Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !contains(eng.calls, "kill panel-nginx HUP") {
		t.Errorf("calls = %v", eng.calls)
	}

	down := &fakeEngine{container: &docker.Container{State: docker.ContainerState{Status: "exited"}}}
	if err := NewService(down, testPaths, fixedNginxImage).Reload(context.Background()); err == nil {
		t.Error("a reload of a stopped container reported success")
	}
	if len(down.calls) > 1 {
		t.Errorf("signalled a stopped container: %v", down.calls)
	}
}

func TestHealthyDetectsARestartedMaster(t *testing.T) {
	same := running()
	restarted := running()
	restarted.State.StartedAt = time.Unix(200, 0)

	svc := func(e *fakeEngine) *Service { s := NewService(e, testPaths, fixedNginxImage); s.wait = 0; return s }

	if err := svc(&fakeEngine{inspects: []docker.Container{*same, *same}}).Healthy(context.Background()); err != nil {
		t.Errorf("a steady master was reported unhealthy: %v", err)
	}
	err := svc(&fakeEngine{inspects: []docker.Container{*same, *restarted}}).Healthy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Errorf("a restarted master was not detected: %v", err)
	}
	exited := docker.Container{State: docker.ContainerState{Status: "exited"}}
	if err := svc(&fakeEngine{inspects: []docker.Container{*same, exited}}).Healthy(context.Background()); err == nil {
		t.Error("an exited container was reported healthy")
	}
}

func TestValidateOnlyNeverReloads(t *testing.T) {
	eng := &fakeEngine{}
	v := NewValidateOnly(eng, testPaths, fixedNginxImage)
	if v.Name() != ServiceName {
		t.Errorf("name = %q: it must share the live service's apply lock", v.Name())
	}
	if err := v.Reload(context.Background()); err != nil {
		t.Error(err)
	}
	if err := v.Healthy(context.Background()); err != nil {
		t.Error(err)
	}
	if contains(eng.calls, "kill panel-nginx HUP") {
		t.Error("ValidateOnly signalled a container")
	}
}

func contains(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// fakeApplier is a confedit.Applier that can be told to fail.
type fakeApplier struct {
	changes []confedit.Change
	err     error
}

func (f *fakeApplier) Apply(_ context.Context, ch confedit.Change) (confedit.Result, error) {
	f.changes = append(f.changes, ch)
	if f.err != nil {
		return confedit.Result{}, f.err
	}
	return confedit.Result{}, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSetSettingsStoresAndApplies(t *testing.T) {
	ap := &fakeApplier{}
	g := NewGlobal(openStore(t), ap, NewValidateOnly(&fakeEngine{}, testPaths, fixedNginxImage), testPaths)

	got, err := g.Settings(context.Background())
	if err != nil || got != DefaultHTTPSettings() {
		t.Fatalf("fresh Settings = %+v, %v; want the defaults", got, err)
	}

	s := DefaultHTTPSettings()
	s.ClientMaxBodySize = "512m"
	if _, err := g.SetSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, _ = g.Settings(context.Background())
	if got.ClientMaxBodySize != "512m" {
		t.Errorf("stored = %+v", got)
	}
	if len(ap.changes) != 1 || len(ap.changes[0].Files) != 2 {
		t.Fatalf("changes = %+v, want one apply of two files", ap.changes)
	}
	files := ap.changes[0].Files
	if files[0].Path != "/www/panel/nginx/panel-main.conf" || files[1].Path != "/www/panel/nginx/panel-http.conf" {
		t.Errorf("paths = %s, %s", files[0].Path, files[1].Path)
	}
	if !strings.Contains(string(files[1].Content), "client_max_body_size 512m;") {
		t.Errorf("panel-http.conf = %s", files[1].Content)
	}
}

func TestRejectedSettingsLeaveNoTrace(t *testing.T) {
	ap := &fakeApplier{err: &confedit.ApplyError{Stage: confedit.StageValidate, Service: "nginx", Output: "[emerg] boom", Err: errors.New("rejected")}}
	g := NewGlobal(openStore(t), ap, NewValidateOnly(&fakeEngine{}, testPaths, fixedNginxImage), testPaths)

	s := DefaultHTTPSettings()
	s.SendTimeout = 5
	_, err := g.SetSettings(context.Background(), s)
	var ae *confedit.ApplyError
	if !errors.As(err, &ae) || ae.Output != "[emerg] boom" {
		t.Fatalf("err = %v, want the apply error with the validator output", err)
	}
	got, _ := g.Settings(context.Background())
	if got != DefaultHTTPSettings() {
		t.Errorf("a failed apply left %+v in the database", got)
	}
}

func TestInvalidSettingsNeverReachTheApplier(t *testing.T) {
	ap := &fakeApplier{}
	g := NewGlobal(openStore(t), ap, NewValidateOnly(&fakeEngine{}, testPaths, fixedNginxImage), testPaths)
	s := DefaultHTTPSettings()
	s.WorkerProcesses = "lots"
	_, err := g.SetSettings(context.Background(), s)
	var se *SettingsError
	if !errors.As(err, &se) || se.Field != "worker_processes" {
		t.Fatalf("err = %v, want a SettingsError on worker_processes", err)
	}
	if len(ap.changes) != 0 {
		t.Error("an invalid change was applied")
	}
}

func TestWriteConfAppliesTheRawFile(t *testing.T) {
	ap := &fakeApplier{}
	g := NewGlobal(openStore(t), ap, NewValidateOnly(&fakeEngine{}, testPaths, fixedNginxImage), testPaths)
	if _, err := g.WriteConf(context.Background(), []byte("events {}\n")); err != nil {
		t.Fatal(err)
	}
	ch := ap.changes[0]
	if ch.Files[0].Path != "/www/panel/nginx/nginx.conf" || string(ch.Files[0].Content) != "events {}\n" || len(ch.Services) != 1 {
		t.Errorf("change = %+v", ch)
	}
}

func TestConfLinksNamesWhatStoppedWorking(t *testing.T) {
	g := NewGlobal(openStore(t), &fakeApplier{}, NewValidateOnly(&fakeEngine{}, testPaths, fixedNginxImage), testPaths)
	full, _ := testPaths.RenderMainConf()
	if m := g.ConfLinks(full); len(m) != 0 {
		t.Errorf("the generated nginx.conf reports %v missing", m)
	}
	trimmed := strings.ReplaceAll(string(full), "include "+testPaths.HTTPIncludePath()+";", "")
	m := g.ConfLinks([]byte(trimmed))
	if len(m) != 1 || m[0] != "the common settings" {
		t.Errorf("missing = %v, want the common settings only", m)
	}
}

// The ssl directory holds the panel's key and the ACME account key, so the
// container may see the site certificates and the challenge webroot and
// nothing above them.
func TestSpecMountsCertificatesAndWebrootButNotTheSSLDir(t *testing.T) {
	spec := Spec(testPaths, "panel/nginx:1.29-test")
	mounts := map[string]bool{}
	for _, m := range spec.Mounts {
		mounts[m.Path] = m.ReadOnly
		if m.Path == testPaths.SSLDir() || strings.HasPrefix(testPaths.SSLDir()+"/", m.Path+"/") {
			t.Errorf("mount %s exposes the whole ssl directory", m.Path)
		}
	}
	for _, p := range []string{testPaths.SiteCertsDir(), testPaths.ACMEWebroot()} {
		if ro, ok := mounts[p]; !ok || !ro {
			t.Errorf("mount %s present=%v readOnly=%v, want a read-only mount", p, ok, ro)
		}
	}
	if err := spec.Validate(testPaths.Root); err != nil {
		t.Errorf("Spec does not validate: %v", err)
	}
}

// A container created before the TLS mounts existed must be recreated, not
// taken for current, or nginx would read certificates it cannot see.
func TestSpecHashChangesWithTheTLSMounts(t *testing.T) {
	spec := Spec(testPaths, "panel/nginx:1.29-test")
	old := spec
	old.Mounts = nil
	for _, m := range spec.Mounts {
		if m.Path != testPaths.SiteCertsDir() && m.Path != testPaths.ACMEWebroot() {
			old.Mounts = append(old.Mounts, m)
		}
	}
	if len(old.Mounts) != len(spec.Mounts)-2 {
		t.Fatalf("mounts = %v", spec.Mounts)
	}
	if old.Hash() == spec.Hash() {
		t.Error("the spec hash ignores the TLS mounts")
	}
	cur := &docker.Container{ImageID: "sha256:x", Labels: old.Config().Labels, State: docker.ContainerState{Running: true}}
	act, _, err := docker.Decide(spec, "sha256:x", cur)
	if err != nil || act != docker.ActionRecreate {
		t.Errorf("Decide = %v, %v; want recreate", act, err)
	}
}

// fixedNginxImage is the build a test service runs.
var fixedNginxImage ImageFunc = func(context.Context) (string, error) { return "panel/nginx:1.29-test", nil }

// ImageTag and ImageRemove complete the fake as an nginx Engine. They record
// nothing: the build tests use the catalogue, not the fake, for state.
func (f *fakeEngine) ImageTag(context.Context, string, string) error { return nil }

func (f *fakeEngine) ImageRemove(context.Context, string) error { return nil }
