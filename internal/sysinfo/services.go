package sysinfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// Kind is how a service is run, which decides how it is controlled.
type Kind string

const (
	// KindSystemd is a unit controlled with systemctl.
	KindSystemd Kind = "systemd"
	// KindContainer is a Docker container controlled through the Engine API.
	KindContainer Kind = "container"
	// KindApp is a managed app run by panel-supervisord.
	KindApp Kind = "app"
)

// State is the one vocabulary the dashboard sees, whatever the service kind
// reports natively. The native word is kept in Service.Detail.
type State string

const (
	StateRunning  State = "running"
	StateStarting State = "starting"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
	// StateMissing means the unit, container or program does not exist.
	StateMissing State = "missing"
	// StateUnknown means the service could not be asked.
	StateUnknown State = "unknown"
)

// Action is a control verb.
type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

// AppPrefix starts the Service.Name of every app, so an app called "docker" cannot
// be mistaken for the Docker unit.
const AppPrefix = "app:"

var (
	// ErrUnknownService is returned for a name that is not on the allowlist.
	ErrUnknownService = errors.New("sysinfo: unknown service")
	// ErrUnknownAction is returned for an action other than start, stop or restart.
	ErrUnknownAction = errors.New("sysinfo: unknown action")
	// ErrNotAllowed is returned for an action the panel refuses on a service.
	ErrNotAllowed = errors.New("sysinfo: action not allowed on this service")
)

// Service is one row of the dashboard's service grid.
type Service struct {
	Name   string `json:"name"`
	Kind   Kind   `json:"kind"`
	State  State  `json:"state"`
	Detail string `json:"detail"`
	// Actions are the controls Control accepts for this service.
	Actions []Action `json:"actions"`
}

// Commander runs a command and returns what it wrote to stdout. Unlike
// osdep.Runner it hands the output back, because `systemctl show` is the answer.
// The output is returned even when err is non-nil. Implementations receive an
// argument vector, never a shell line.
type Commander interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecCommander runs commands with exec.CommandContext.
type ExecCommander struct{}

// Output implements Commander. A failure carries the command's stderr.
func (ExecCommander) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.Bytes(), fmt.Errorf("sysinfo: %s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("sysinfo: %s %s: %w", name, strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// Engine is the part of the Docker client the service controls use.
// *docker.Client satisfies it.
type Engine interface {
	ContainerInspect(ctx context.Context, id string) (docker.Container, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string, timeout time.Duration) error
}

// AppState is one managed app as the service grid needs it.
type AppState struct {
	Name string
	// State is supervisord's state name (RUNNING, STOPPED, FATAL, ...), or
	// "unknown" or "not loaded", which is what apps.Status.State carries.
	State string
	// PID is the program's main process, 0 when it has none. Usage reads its CPU
	// and memory from /proc/<pid>.
	PID int
}

// Apps is the app manager as the service grid sees it. *apps.Manager has the
// Start, Stop and Restart methods as they are; its List returns richer rows, so
// the wiring supplies a small adapter for List.
type Apps interface {
	List(ctx context.Context) ([]AppState, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
}

// ServicesOptions configure Services.
type ServicesOptions struct {
	// Cmd runs systemctl. Required.
	Cmd Commander
	// Engine reaches Docker. Nil leaves container rows out.
	Engine Engine
	// Containers names the containers to show and control. It is asked on every
	// call because containers come and go. Nil means none.
	Containers func(ctx context.Context) ([]string, error)
	// Units names the panel's own systemd units that come and go with a setting:
	// one `panel-php-<ver>.service` per PHP version. It is asked on every call for
	// the same reason. Nil means none.
	Units func(ctx context.Context) ([]string, error)
	// Apps lists and controls managed apps. Nil leaves app rows out.
	Apps Apps
	// StopTimeout is how long Docker waits for a container to exit after SIGTERM
	// before killing it. Zero means 10 seconds.
	StopTimeout time.Duration
	// Files reads cgroup and /proc files for Usage. Nil means the host's.
	Files FileReader
	// Now is the clock Usage measures CPU intervals with. Nil means time.Now.
	Now func() time.Time
}

// unitPattern is a systemd unit name. A name from the provider is passed to
// systemctl as one argument, so anything with a path separator or a shell
// character is refused here.
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.:-]{0,127}$`)

type unit struct {
	name string // service name as shown and accepted
	unit string // systemd unit
	// noStop and noBlock exist for the panel's own unit (noStop also for nginx): stopping it from its own
	// UI leaves nothing to start it again, and a restart must not wait on the
	// daemon that is asking for it to end.
	noStop, noBlock bool
}

// systemdUnits is the allowlist of units. A name outside it is never passed to
// systemctl.
var systemdUnits = []unit{
	{name: "panel", unit: "panel.service", noStop: true, noBlock: true},
	{name: "panel-supervisord", unit: "panel-supervisord.service"},
	{name: "panel-firewall", unit: "panel-firewall.service"},
	// nginx is stopped from the Nginx page, which records that the operator did it;
	// a bare systemctl stop here would be undone by the next reconcile.
	{name: "panel-nginx", unit: "panel-nginx.service", noStop: true},
	{name: "docker", unit: "docker.service"},
}

// Services reports and controls the services the panel depends on.
type Services struct {
	cmd         Commander
	eng         Engine
	containers  func(context.Context) ([]string, error)
	units       func(context.Context) ([]string, error)
	apps        Apps
	stopTimeout time.Duration
	files       FileReader
	now         func() time.Time
	usage       usageState
}

// NewServices builds Services.
func NewServices(o ServicesOptions) (*Services, error) {
	if o.Cmd == nil {
		return nil, fmt.Errorf("sysinfo: ServicesOptions.Cmd is required")
	}
	s := &Services{cmd: o.Cmd, eng: o.Engine, containers: o.Containers, units: o.Units, apps: o.Apps, stopTimeout: o.StopTimeout}
	if s.stopTimeout <= 0 {
		s.stopTimeout = 10 * time.Second
	}
	s.files, s.now = o.Files, o.Now
	if s.files == nil {
		s.files = OSFiles{}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

func actionsFor(noStop bool) []Action {
	if noStop {
		return []Action{ActionStart, ActionRestart}
	}
	return []Action{ActionStart, ActionStop, ActionRestart}
}

// List reports every service: the systemd units, then the containers, then the
// apps. A service that cannot be asked is a row in state unknown with the reason
// in Detail; List never fails as a whole.
func (s *Services) List(ctx context.Context) []Service {
	out := make([]Service, 0, len(systemdUnits)+8)
	for _, u := range s.allUnits(ctx) {
		st, detail := s.systemdState(ctx, u.unit)
		out = append(out, Service{Name: u.name, Kind: KindSystemd, State: st, Detail: detail, Actions: actionsFor(u.noStop)})
	}
	names, err := s.containerNames(ctx)
	if err != nil {
		out = append(out, Service{Name: "containers", Kind: KindContainer, State: StateUnknown, Detail: err.Error()})
	}
	for _, name := range names {
		st, detail := s.containerState(ctx, name)
		out = append(out, Service{Name: name, Kind: KindContainer, State: st, Detail: detail, Actions: actionsFor(false)})
	}
	if s.apps != nil {
		list, err := s.apps.List(ctx)
		if err != nil {
			out = append(out, Service{Name: "apps", Kind: KindApp, State: StateUnknown, Detail: err.Error()})
		}
		for _, a := range list {
			st := appState(a.State)
			out = append(out, Service{Name: AppPrefix + a.Name, Kind: KindApp, State: st, Detail: a.State, Actions: actionsFor(false)})
		}
	}
	return out
}

// allUnits is the fixed allowlist plus the units the provider names. A PHP
// version's unit is shown under the service name `panel-php-<ver>` and is never
// stopped from here: the PHP page records that the operator chose it, and a bare
// systemctl stop would be undone by the next reconcile.
func (s *Services) allUnits(ctx context.Context) []unit {
	out := append([]unit(nil), systemdUnits...)
	if s.units == nil {
		return out
	}
	names, err := s.units(ctx)
	if err != nil {
		// A provider that cannot answer leaves the fixed rows alone; the cause is
		// in the daemon log, as for the containers.
		return out
	}
	for _, name := range names {
		if !unitPattern.MatchString(name) {
			continue
		}
		out = append(out, unit{name: strings.TrimSuffix(name, ".service"), unit: name, noStop: true})
	}
	return out
}

// Control runs start, stop or restart on one service. The name must be on the
// allowlist: a fixed set of systemd units, the units Options.Units names, the
// containers Options.Containers names, or an app the app manager lists. Anything
// else is ErrUnknownService and nothing is run.
func (s *Services) Control(ctx context.Context, name string, action Action) error {
	switch action {
	case ActionStart, ActionStop, ActionRestart:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownAction, action)
	}
	for _, u := range s.allUnits(ctx) {
		if u.name == name {
			if action == ActionStop && u.noStop {
				return fmt.Errorf("%w: %s cannot be stopped from its own UI", ErrNotAllowed, name)
			}
			args := []string{string(action)}
			if u.noBlock {
				args = append(args, "--no-block")
			}
			args = append(args, u.unit)
			if _, err := s.cmd.Output(ctx, "systemctl", args...); err != nil {
				return err
			}
			return nil
		}
	}
	if app, ok := strings.CutPrefix(name, AppPrefix); ok {
		return s.controlApp(ctx, app, action)
	}
	names, err := s.containerNames(ctx)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == name && s.eng != nil {
			return s.controlContainer(ctx, name, action)
		}
	}
	return fmt.Errorf("%w: %q", ErrUnknownService, name)
}

func (s *Services) controlApp(ctx context.Context, name string, action Action) error {
	if s.apps == nil {
		return fmt.Errorf("%w: %q", ErrUnknownService, AppPrefix+name)
	}
	list, err := s.apps.List(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, a := range list {
		if a.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrUnknownService, AppPrefix+name)
	}
	switch action {
	case ActionStart:
		return s.apps.Start(ctx, name)
	case ActionStop:
		return s.apps.Stop(ctx, name)
	default:
		return s.apps.Restart(ctx, name)
	}
}

func (s *Services) controlContainer(ctx context.Context, name string, action Action) error {
	stop := func() error {
		c, err := s.eng.ContainerInspect(ctx, name)
		if err != nil {
			return err
		}
		if !c.State.Running && !c.State.Restarting {
			return nil
		}
		return s.eng.ContainerStop(ctx, name, s.stopTimeout)
	}
	switch action {
	case ActionStop:
		return stop()
	case ActionStart:
		return s.eng.ContainerStart(ctx, name)
	default:
		if err := stop(); err != nil {
			return err
		}
		return s.eng.ContainerStart(ctx, name)
	}
}

func (s *Services) containerNames(ctx context.Context) ([]string, error) {
	if s.eng == nil || s.containers == nil {
		return nil, nil
	}
	return s.containers(ctx)
}

// systemdState asks `systemctl show` for the unit. show exits 0 for a unit that is
// inactive or does not exist, so a failure here means systemctl itself could not
// run, and that is state unknown.
func (s *Services) systemdState(ctx context.Context, unitName string) (State, string) {
	out, err := s.cmd.Output(ctx, "systemctl", "show", "--no-pager",
		"--property=LoadState,ActiveState,SubState", unitName)
	if err != nil {
		return StateUnknown, err.Error()
	}
	return parseSystemdShow(out)
}

func parseSystemdShow(out []byte) (State, string) {
	kv := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = v
		}
	}
	if kv["LoadState"] == "not-found" {
		return StateMissing, "unit not found"
	}
	active := kv["ActiveState"]
	detail := active
	if sub := kv["SubState"]; sub != "" && sub != active {
		detail = active + " (" + sub + ")"
	}
	switch active {
	case "active", "reloading":
		return StateRunning, detail
	case "activating":
		return StateStarting, detail
	case "deactivating":
		return StateStopping, detail
	case "inactive":
		return StateStopped, detail
	case "failed":
		return StateFailed, detail
	}
	return StateUnknown, "systemctl reported " + fmt.Sprintf("%q", active)
}

func (s *Services) containerState(ctx context.Context, name string) (State, string) {
	c, err := s.eng.ContainerInspect(ctx, name)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return StateMissing, "container does not exist"
		}
		return StateUnknown, err.Error()
	}
	detail := c.State.Status
	switch {
	case c.State.Restarting || c.State.Status == "restarting":
		return StateStarting, detail
	case c.State.Running:
		return StateRunning, detail
	case c.State.Status == "dead":
		return StateFailed, detail
	case c.State.Status == "exited" && c.State.ExitCode != 0:
		return StateFailed, fmt.Sprintf("exited (code %d)", c.State.ExitCode)
	}
	return StateStopped, detail
}

// appState maps supervisord's vocabulary, which apps.Status carries as is.
func appState(raw string) State {
	switch strings.ToUpper(raw) {
	case "RUNNING":
		return StateRunning
	case "STARTING", "BACKOFF":
		return StateStarting
	case "STOPPING":
		return StateStopping
	case "STOPPED", "EXITED":
		return StateStopped
	case "FATAL":
		return StateFailed
	case "NOT LOADED":
		return StateMissing
	}
	return StateUnknown
}
