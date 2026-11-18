package nginx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/store"
)

// fakeRunner records the host commands the nginx service runs and answers them
// from fn, so a test scripts what systemd and nginx say.
type fakeRunner struct {
	calls []string
	fn    func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.fn != nil {
		return f.fn(name, args)
	}
	return nil, nil
}

// exitErr is a command that ran and failed.
type exitErr struct{ code int }

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitErr) ExitCode() int { return e.code }

// show is what `systemctl show` prints for the nginx unit.
func show(load, active string, pid int, since string) []byte {
	return fmt.Appendf(nil, "LoadState=%s\nActiveState=%s\nMainPID=%d\nActiveEnterTimestampMonotonic=%s\n", load, active, pid, since)
}

// unitState scripts systemctl: the unit is in the given state, and every other
// command succeeds.
func unitState(load, active string, pid int, since string) func(string, []string) ([]byte, error) {
	return func(name string, args []string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "show" {
			return show(load, active, pid, since), nil
		}
		return nil, nil
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

func TestValidateRunsTheServingBinaryAgainstTheConfig(t *testing.T) {
	run := &fakeRunner{fn: func(string, []string) ([]byte, error) { return []byte("nginx: test is successful\n"), nil }}
	out, err := NewService(testPaths, run).Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate = %v", err)
	}
	if !strings.Contains(out, "successful") {
		t.Errorf("output = %q", out)
	}
	want := "/www/panel/nginx/current/sbin/nginx -t -c /www/panel/nginx/nginx.conf"
	if !contains(run.calls, want) {
		t.Errorf("calls = %v, want %q", run.calls, want)
	}
}

func TestValidateReturnsNginxOutputVerbatim(t *testing.T) {
	msg := "nginx: [emerg] unknown directive \"bogus\" in /www/panel/nginx/sites/a.test.conf:3\n"
	run := &fakeRunner{fn: func(string, []string) ([]byte, error) { return []byte(msg), exitErr{1} }}
	out, err := NewService(testPaths, run).Validate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("a rejected config validated: %v", err)
	}
	if out != msg {
		t.Errorf("output = %q, want %q", out, msg)
	}
}

func TestValidateThatCannotRunIsNotARejection(t *testing.T) {
	run := &fakeRunner{fn: func(string, []string) ([]byte, error) { return nil, errors.New("no such file or directory") }}
	out, err := NewService(testPaths, run).Validate(context.Background())
	if err == nil || out != "" || !strings.Contains(err.Error(), "could not be run") {
		t.Errorf("Validate = %q, %v; want an error saying nginx -t could not be run", out, err)
	}
}

func TestReloadAsksSystemdAndRefusesWhenDown(t *testing.T) {
	up := &fakeRunner{fn: unitState("loaded", "active", 7, "100")}
	if err := NewService(testPaths, up).Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !contains(up.calls, "systemctl reload "+UnitName) {
		t.Errorf("calls = %v", up.calls)
	}

	down := &fakeRunner{fn: unitState("loaded", "inactive", 0, "0")}
	if err := NewService(testPaths, down).Reload(context.Background()); err == nil {
		t.Error("a reload of a stopped nginx reported success")
	}
	if contains(down.calls, "systemctl reload "+UnitName) {
		t.Errorf("signalled a stopped nginx: %v", down.calls)
	}
}

func TestHealthyDetectsARestartedMaster(t *testing.T) {
	// Each Healthy reads the unit twice; the answers are consumed in order.
	seq := func(answers ...[]byte) *fakeRunner {
		return &fakeRunner{fn: func(string, []string) ([]byte, error) {
			a := answers[0]
			if len(answers) > 1 {
				answers = answers[1:]
			}
			return a, nil
		}}
	}
	svc := func(r *fakeRunner) *Service { s := NewService(testPaths, r); s.wait = 0; return s }

	same := show("loaded", "active", 7, "100")
	if err := svc(seq(same, same)).Healthy(context.Background()); err != nil {
		t.Errorf("a steady master was reported unhealthy: %v", err)
	}
	restarted := show("loaded", "active", 9, "200")
	if err := svc(seq(same, restarted)).Healthy(context.Background()); err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Errorf("a restarted master was not detected: %v", err)
	}
	failed := show("loaded", "failed", 0, "0")
	if err := svc(seq(same, failed)).Healthy(context.Background()); err == nil {
		t.Error("a failed unit was reported healthy")
	}
}

func TestStateReportsAnUnwrittenUnitAsMissing(t *testing.T) {
	run := &fakeRunner{fn: unitState("not-found", "inactive", 0, "0")}
	st, found, err := NewService(testPaths, run).State(context.Background())
	if err != nil || found || st.Active {
		t.Errorf("State = %+v, %v, %v; want not found", st, found, err)
	}
	run = &fakeRunner{fn: unitState("loaded", "active", 7, "100")}
	st, found, _ = NewService(testPaths, run).State(context.Background())
	if !found || !st.Active || st.PID != 7 {
		t.Errorf("State = %+v, %v", st, found)
	}
}

func TestValidateOnlyNeverReloads(t *testing.T) {
	run := &fakeRunner{}
	v := NewValidateOnly(testPaths, run)
	if v.Name() != ServiceName {
		t.Errorf("name = %q: it must share the live service's apply lock", v.Name())
	}
	if err := v.Reload(context.Background()); err != nil {
		t.Error(err)
	}
	if err := v.Healthy(context.Background()); err != nil {
		t.Error(err)
	}
	if len(run.calls) != 0 {
		t.Errorf("ValidateOnly ran commands: %v", run.calls)
	}
}

func TestUnitRunsTheServingBinaryAndQuitsGracefully(t *testing.T) {
	u := UnitText(testPaths)
	for _, want := range []string{
		"ExecStartPre=/www/panel/nginx/current/sbin/nginx -t -c /www/panel/nginx/nginx.conf",
		`ExecStart=/www/panel/nginx/current/sbin/nginx -c /www/panel/nginx/nginx.conf -g "daemon off;"`,
		"ExecReload=/bin/kill -HUP $MAINPID",
		"KillSignal=SIGQUIT",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
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
	g := NewGlobal(openStore(t), ap, NewValidateOnly(testPaths, &fakeRunner{}), testPaths)

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
	g := NewGlobal(openStore(t), ap, NewValidateOnly(testPaths, &fakeRunner{}), testPaths)

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
	g := NewGlobal(openStore(t), ap, NewValidateOnly(testPaths, &fakeRunner{}), testPaths)
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
	g := NewGlobal(openStore(t), ap, NewValidateOnly(testPaths, &fakeRunner{}), testPaths)
	if _, err := g.WriteConf(context.Background(), []byte("events {}\n")); err != nil {
		t.Fatal(err)
	}
	ch := ap.changes[0]
	if ch.Files[0].Path != "/www/panel/nginx/nginx.conf" || string(ch.Files[0].Content) != "events {}\n" || len(ch.Services) != 1 {
		t.Errorf("change = %+v", ch)
	}
}

func TestConfLinksNamesWhatStoppedWorking(t *testing.T) {
	g := NewGlobal(openStore(t), &fakeApplier{}, NewValidateOnly(testPaths, &fakeRunner{}), testPaths)
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
