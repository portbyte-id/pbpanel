package sysinfo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

type fakeCmd struct {
	calls [][]string
	// show maps a unit name to the `systemctl show` output returned for it.
	show map[string]string
	err  map[string]error // by unit name
}

func (f *fakeCmd) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	unitName := args[len(args)-1]
	if err := f.err[unitName]; err != nil {
		return nil, err
	}
	if args[0] == "show" {
		return []byte(f.show[unitName]), nil
	}
	return nil, nil
}

func show(load, active, sub string) string {
	return fmt.Sprintf("LoadState=%s\nActiveState=%s\nSubState=%s\n", load, active, sub)
}

type fakeEngine struct {
	containers map[string]docker.Container
	inspectErr map[string]error
	calls      []string
	stopErr    error
}

func (e *fakeEngine) ContainerInspect(_ context.Context, id string) (docker.Container, error) {
	if err := e.inspectErr[id]; err != nil {
		return docker.Container{}, err
	}
	c, ok := e.containers[id]
	if !ok {
		return docker.Container{}, &docker.APIError{Op: "inspect", Status: 404, Message: "no such container"}
	}
	return c, nil
}

func (e *fakeEngine) ContainerStart(_ context.Context, id string) error {
	e.calls = append(e.calls, "start "+id)
	return nil
}

func (e *fakeEngine) ContainerStop(_ context.Context, id string, d time.Duration) error {
	e.calls = append(e.calls, fmt.Sprintf("stop %s %v", id, d))
	return e.stopErr
}

type fakeApps struct {
	list  []AppState
	err   error
	calls []string
}

func (a *fakeApps) List(context.Context) ([]AppState, error) { return a.list, a.err }
func (a *fakeApps) Start(_ context.Context, n string) error {
	a.calls = append(a.calls, "start "+n)
	return nil
}
func (a *fakeApps) Stop(_ context.Context, n string) error {
	a.calls = append(a.calls, "stop "+n)
	return nil
}
func (a *fakeApps) Restart(_ context.Context, n string) error {
	a.calls = append(a.calls, "restart "+n)
	return nil
}

func running() docker.Container {
	return docker.Container{State: docker.ContainerState{Status: "running", Running: true}}
}

func newServices(t *testing.T, cmd *fakeCmd, eng *fakeEngine, apps *fakeApps) *Services {
	t.Helper()
	o := ServicesOptions{
		Cmd:        cmd,
		Containers: func(context.Context) ([]string, error) { return []string{"panel-php-8.3", "panel-php-8.4"}, nil },
	}
	if eng != nil {
		o.Engine = eng
	}
	if apps != nil {
		o.Apps = apps
	}
	s, err := NewServices(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func byName(list []Service) map[string]Service {
	m := map[string]Service{}
	for _, s := range list {
		m[s.Name] = s
	}
	return m
}

func TestListMapsEveryKind(t *testing.T) {
	cmd := &fakeCmd{
		show: map[string]string{
			"panel.service":             show("loaded", "active", "running"),
			"panel-supervisord.service": show("loaded", "failed", "failed"),
			"panel-firewall.service":    show("loaded", "active", "exited"),
			"panel-nginx.service":       show("loaded", "active", "running"),
			"docker.service":            show("not-found", "inactive", "dead"),
		},
	}
	eng := &fakeEngine{containers: map[string]docker.Container{
		"panel-php-8.3": running(),
		// panel-php-8.4 is not created.
	}}
	apps := &fakeApps{list: []AppState{{Name: "blog", State: "RUNNING"}, {Name: "api", State: "FATAL"}, {Name: "x", State: "not loaded"}}}
	got := byName(newServices(t, cmd, eng, apps).List(context.Background()))

	for name, want := range map[string]struct {
		kind  Kind
		state State
	}{
		"panel":             {KindSystemd, StateRunning},
		"panel-supervisord": {KindSystemd, StateFailed},
		"panel-firewall":    {KindSystemd, StateRunning},
		"panel-nginx":       {KindSystemd, StateRunning},
		"docker":            {KindSystemd, StateMissing},
		"panel-php-8.3":     {KindContainer, StateRunning},
		"panel-php-8.4":     {KindContainer, StateMissing},
		"app:blog":          {KindApp, StateRunning},
		"app:api":           {KindApp, StateFailed},
		"app:x":             {KindApp, StateMissing},
	} {
		s, ok := got[name]
		if !ok || s.Kind != want.kind || s.State != want.state {
			t.Errorf("%s = %+v, want %v/%v", name, s, want.kind, want.state)
		}
	}
	if got["panel-firewall"].Detail != "active (exited)" || got["docker"].Detail != "unit not found" {
		t.Errorf("details: %+v %+v", got["panel-firewall"], got["docker"])
	}
	if got["app:api"].Detail != "FATAL" {
		t.Errorf("app detail = %q", got["app:api"].Detail)
	}
	if !reflect.DeepEqual(got["panel"].Actions, []Action{ActionStart, ActionRestart}) {
		t.Errorf("panel actions = %v", got["panel"].Actions)
	}
	if !reflect.DeepEqual(got["docker"].Actions, []Action{ActionStart, ActionStop, ActionRestart}) {
		t.Errorf("docker actions = %v", got["docker"].Actions)
	}
	// One `systemctl show` per unit, exact argv.
	want := []string{"systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState", "panel.service"}
	if !reflect.DeepEqual(cmd.calls[0], want) || len(cmd.calls) != 5 {
		t.Errorf("calls = %q", cmd.calls)
	}
}

func TestListContainerStates(t *testing.T) {
	cmd := &fakeCmd{show: map[string]string{}}
	eng := &fakeEngine{
		containers: map[string]docker.Container{
			"panel-php-8.3": {State: docker.ContainerState{Status: "exited", ExitCode: 137}},
			"panel-php-8.4": {State: docker.ContainerState{Status: "restarting", Running: true, Restarting: true}},
		},
	}
	got := byName(newServices(t, cmd, eng, nil).List(context.Background()))
	if s := got["panel-php-8.3"]; s.State != StateFailed || s.Detail != "exited (code 137)" {
		t.Errorf("nginx = %+v", s)
	}
	if s := got["panel-php-8.4"]; s.State != StateStarting {
		t.Errorf("php = %+v", s)
	}

	eng.containers["panel-php-8.3"] = docker.Container{State: docker.ContainerState{Status: "exited"}}
	eng.inspectErr = map[string]error{"panel-php-8.4": errors.New("dial unix /var/run/docker.sock: connect: no such file")}
	got = byName(newServices(t, cmd, eng, nil).List(context.Background()))
	if s := got["panel-php-8.3"]; s.State != StateStopped {
		t.Errorf("clean exit = %+v", s)
	}
	if s := got["panel-php-8.4"]; s.State != StateUnknown || !strings.Contains(s.Detail, "docker.sock") {
		t.Errorf("engine down = %+v", s)
	}
}

func TestListToleratesFailures(t *testing.T) {
	cmd := &fakeCmd{show: map[string]string{}, err: map[string]error{"panel.service": errors.New("systemctl: not found")}}
	apps := &fakeApps{err: errors.New("supervisord is down")}
	list := newServices(t, cmd, nil, apps).List(context.Background())
	got := byName(list)
	if s := got["panel"]; s.State != StateUnknown || !strings.Contains(s.Detail, "systemctl") {
		t.Errorf("panel = %+v", s)
	}
	if s := got["apps"]; s.State != StateUnknown || s.Kind != KindApp {
		t.Errorf("apps row = %+v", s)
	}
	// No engine: no container rows at all.
	if _, ok := got["panel-php-8.3"]; ok {
		t.Error("container row without an engine")
	}
	// An empty `show` (no ActiveState) is unknown, never "stopped".
	if s := got["docker"]; s.State != StateUnknown {
		t.Errorf("empty show = %+v", s)
	}
}

func TestControlSystemdArgv(t *testing.T) {
	cmd := &fakeCmd{}
	s := newServices(t, cmd, nil, nil)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		action Action
		want   []string
	}{
		{"panel-supervisord", ActionStart, []string{"systemctl", "start", "panel-supervisord.service"}},
		{"panel-firewall", ActionStop, []string{"systemctl", "stop", "panel-firewall.service"}},
		{"docker", ActionRestart, []string{"systemctl", "restart", "docker.service"}},
		{"panel", ActionRestart, []string{"systemctl", "restart", "--no-block", "panel.service"}},
		{"panel", ActionStart, []string{"systemctl", "start", "--no-block", "panel.service"}},
	} {
		cmd.calls = nil
		if err := s.Control(ctx, tc.name, tc.action); err != nil {
			t.Fatalf("%s %s: %v", tc.action, tc.name, err)
		}
		if len(cmd.calls) != 1 || !reflect.DeepEqual(cmd.calls[0], tc.want) {
			t.Errorf("%s %s: argv = %q, want %q", tc.action, tc.name, cmd.calls, tc.want)
		}
	}

	cmd.calls = nil
	if err := s.Control(ctx, "panel", ActionStop); !errors.Is(err, ErrNotAllowed) || len(cmd.calls) != 0 {
		t.Fatalf("stop panel: err=%v calls=%q", err, cmd.calls)
	}

	cmd.err = map[string]error{"docker.service": errors.New("Job failed")}
	if err := s.Control(ctx, "docker", ActionStart); err == nil || !strings.Contains(err.Error(), "Job failed") {
		t.Fatalf("systemctl failure not returned: %v", err)
	}
}

func TestControlRejectsOffAllowlist(t *testing.T) {
	cmd := &fakeCmd{}
	eng := &fakeEngine{containers: map[string]docker.Container{"evil": running()}}
	apps := &fakeApps{list: []AppState{{Name: "blog"}}}
	s := newServices(t, cmd, eng, apps)
	ctx := context.Background()

	for _, name := range []string{
		"sshd", "sshd.service", "panel.service", "../docker", "docker; reboot", "",
		"evil",         // a real container, but not in Options.Containers
		"app:missing",  // not a listed app
		"blog",         // an app without the prefix
		"panel-nginx2", // near miss
	} {
		if err := s.Control(ctx, name, ActionRestart); !errors.Is(err, ErrUnknownService) {
			t.Errorf("%q: err = %v", name, err)
		}
	}
	for _, action := range []Action{"", "reload", "kill", "enable", "START"} {
		if err := s.Control(ctx, "docker", action); !errors.Is(err, ErrUnknownAction) {
			t.Errorf("action %q: err = %v", action, err)
		}
	}
	if len(cmd.calls) != 0 || len(eng.calls) != 0 || len(apps.calls) != 0 {
		t.Fatalf("something ran: %q %q %q", cmd.calls, eng.calls, apps.calls)
	}
}

func TestControlContainer(t *testing.T) {
	cmd := &fakeCmd{}
	eng := &fakeEngine{containers: map[string]docker.Container{
		"panel-php-8.3": running(),
		"panel-php-8.4": {State: docker.ContainerState{Status: "exited"}},
	}}
	s := newServices(t, cmd, eng, nil)
	ctx := context.Background()

	if err := s.Control(ctx, "panel-php-8.3", ActionStop); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eng.calls, []string{"stop panel-php-8.3 10s"}) {
		t.Fatalf("stop: %q", eng.calls)
	}

	eng.calls = nil
	if err := s.Control(ctx, "panel-php-8.3", ActionRestart); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eng.calls, []string{"stop panel-php-8.3 10s", "start panel-php-8.3"}) {
		t.Fatalf("restart running: %q", eng.calls)
	}

	// Restarting a stopped container only starts it; stopping one is a no-op.
	eng.calls = nil
	if err := s.Control(ctx, "panel-php-8.4", ActionRestart); err != nil {
		t.Fatal(err)
	}
	if err := s.Control(ctx, "panel-php-8.4", ActionStop); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eng.calls, []string{"start panel-php-8.4"}) {
		t.Fatalf("restart stopped: %q", eng.calls)
	}

	eng.calls = nil
	eng.stopErr = errors.New("cannot stop")
	if err := s.Control(ctx, "panel-php-8.3", ActionRestart); err == nil || len(eng.calls) != 1 {
		t.Fatalf("restart must not start after a failed stop: err=%v calls=%q", err, eng.calls)
	}
	if len(cmd.calls) != 0 {
		t.Fatalf("a container was controlled through systemctl: %q", cmd.calls)
	}
}

func TestControlApp(t *testing.T) {
	apps := &fakeApps{list: []AppState{{Name: "blog", State: "RUNNING"}}}
	s := newServices(t, &fakeCmd{}, nil, apps)
	ctx := context.Background()
	for _, a := range []Action{ActionStart, ActionStop, ActionRestart} {
		if err := s.Control(ctx, "app:blog", a); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(apps.calls, []string{"start blog", "stop blog", "restart blog"}) {
		t.Fatalf("calls = %q", apps.calls)
	}
	apps.err = errors.New("supervisord is down")
	if err := s.Control(ctx, "app:blog", ActionStart); err == nil {
		t.Fatal("list failure swallowed")
	}
}

func TestParseSystemdShow(t *testing.T) {
	for _, tc := range []struct {
		in    string
		state State
		det   string
	}{
		{show("loaded", "active", "running"), StateRunning, "active (running)"},
		{show("loaded", "reloading", "reload"), StateRunning, "reloading (reload)"},
		{show("loaded", "activating", "start"), StateStarting, "activating (start)"},
		{show("loaded", "deactivating", "stop-sigterm"), StateStopping, "deactivating (stop-sigterm)"},
		{show("loaded", "inactive", "dead"), StateStopped, "inactive (dead)"},
		{show("loaded", "failed", "failed"), StateFailed, "failed"},
		{show("not-found", "inactive", "dead"), StateMissing, "unit not found"},
		{"", StateUnknown, `systemctl reported ""`},
	} {
		if st, d := parseSystemdShow([]byte(tc.in)); st != tc.state || d != tc.det {
			t.Errorf("%q -> %v %q, want %v %q", tc.in, st, d, tc.state, tc.det)
		}
	}
}

func TestNewServicesRequiresCmd(t *testing.T) {
	if _, err := NewServices(ServicesOptions{}); err == nil {
		t.Fatal("nil Cmd accepted")
	}
}
