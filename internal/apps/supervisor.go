package apps

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Process is supervisord's view of one program.
type Process struct {
	Name          string `json:"name"`
	Group         string `json:"group"`
	State         string `json:"state"`
	PID           int    `json:"pid"`
	Start         int64  `json:"start"`
	Now           int64  `json:"now"`
	ExitStatus    int    `json:"exitstatus"`
	SpawnErr      string `json:"spawnerr"`
	Description   string `json:"description"`
	StdoutLogfile string `json:"stdout_logfile"`
}

// Running reports whether the program is up or on its way up.
func (p Process) Running() bool { return p.State == "RUNNING" || p.State == "STARTING" }

// ConfigDiff is what reloadConfig found on disk compared with what is loaded.
type ConfigDiff struct {
	Added, Changed, Removed []string
}

// Supervisor is the part of supervisord the panel uses. *Client implements it;
// tests substitute a fake.
type Supervisor interface {
	State(ctx context.Context) (string, error)
	Processes(ctx context.Context) ([]Process, error)
	ReloadConfig(ctx context.Context) (ConfigDiff, error)
	AddGroup(ctx context.Context, name string) error
	RemoveGroup(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
}

var _ Supervisor = (*Client)(nil)

// State is supervisord's own state name: RUNNING, FATAL, RESTARTING or SHUTDOWN.
func (c *Client) State(ctx context.Context) (string, error) {
	v, err := c.call(ctx, "supervisor.getState")
	if err != nil {
		return "", err
	}
	if m, ok := unwrap(v).(map[string]any); ok {
		return asString(m["statename"]), nil
	}
	return "", fmt.Errorf("supervisord getState: unexpected reply %T", v)
}

// Processes lists every program supervisord has loaded.
func (c *Client) Processes(ctx context.Context) ([]Process, error) {
	v, err := c.call(ctx, "supervisor.getAllProcessInfo")
	if err != nil {
		return nil, err
	}
	list, ok := unwrap(v).([]any)
	if !ok {
		return nil, fmt.Errorf("supervisord getAllProcessInfo: unexpected reply %T", v)
	}
	out := make([]Process, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("supervisord getAllProcessInfo: unexpected entry %T", item)
		}
		out = append(out, processFrom(m))
	}
	return out, nil
}

func processFrom(m map[string]any) Process {
	p := Process{
		Name: asString(m["name"]), Group: asString(m["group"]), State: asString(m["statename"]),
		SpawnErr: asString(m["spawnerr"]), Description: asString(m["description"]),
		StdoutLogfile: asString(m["stdout_logfile"]),
	}
	p.PID, _ = asInt(m["pid"])
	p.ExitStatus, _ = asInt(m["exitstatus"])
	if n, ok := asInt(m["start"]); ok {
		p.Start = int64(n)
	}
	if n, ok := asInt(m["now"]); ok {
		p.Now = int64(n)
	}
	return p
}

// ReloadConfig makes supervisord re-read its files and report what differs from
// what it runs. It starts and stops nothing.
func (c *Client) ReloadConfig(ctx context.Context) (ConfigDiff, error) {
	v, err := c.call(ctx, "supervisor.reloadConfig")
	if err != nil {
		return ConfigDiff{}, err
	}
	// The reply is [[added, changed, removed]]. A bare [added, changed, removed]
	// is accepted too, for the same reason unwrap exists.
	outer, ok := unwrap(v).([]any)
	if !ok {
		return ConfigDiff{}, fmt.Errorf("supervisord reloadConfig: unexpected reply %T", v)
	}
	if len(outer) == 1 {
		if inner, ok := outer[0].([]any); ok {
			outer = inner
		}
	}
	if len(outer) != 3 {
		return ConfigDiff{}, fmt.Errorf("supervisord reloadConfig: expected three lists, got %d", len(outer))
	}
	return ConfigDiff{Added: stringList(outer[0]), Changed: stringList(outer[1]), Removed: stringList(outer[2])}, nil
}

func stringList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// AddGroup loads a program from the pending config. It does not start it.
func (c *Client) AddGroup(ctx context.Context, name string) error {
	_, err := c.call(ctx, "supervisor.addProcessGroup", name)
	return mapFault(err)
}

// RemoveGroup stops a program and forgets it.
func (c *Client) RemoveGroup(ctx context.Context, name string) error {
	_, err := c.call(ctx, "supervisor.removeProcessGroup", name)
	return mapFault(err)
}

// Start starts a program and waits for it to stay up for its startsecs.
func (c *Client) Start(ctx context.Context, name string) error {
	_, err := c.call(ctx, "supervisor.startProcess", name, true)
	return mapFault(err)
}

// Stop stops a program and waits for it to exit.
func (c *Client) Stop(ctx context.Context, name string) error {
	_, err := c.call(ctx, "supervisor.stopProcess", name, true)
	return mapFault(err)
}

// reloader is supervisord as the config editor sees it (confedit.Service): the
// program files are the config, and "reload" is what `supervisorctl update` does.
type reloader struct {
	sv        Supervisor
	autostart func(ctx context.Context, name string) bool

	mu       sync.Mutex
	warnings []string
}

func (r *reloader) Name() string { return "supervisord" }

// Validate has supervisord parse the files now on disk. A file it rejects comes
// back as the error, verbatim; nothing is started or stopped by it.
func (r *reloader) Validate(ctx context.Context) (string, error) {
	if _, err := r.sv.ReloadConfig(ctx); err != nil {
		return err.Error(), err
	}
	return "", nil
}

// Reload brings the running programs in line with the files: removed programs
// are stopped and forgotten, added and changed ones are loaded, and a program
// that was running before the change, or is new with autostart set, is started.
//
// A program that will not stay up is not a failed apply. The configuration is
// correct and the operator needs it saved to fix the program, so the failure is
// kept as a warning for the manager to report.
func (r *reloader) Reload(ctx context.Context) error {
	diff, err := r.sv.ReloadConfig(ctx)
	if err != nil {
		return err
	}
	procs, err := r.sv.Processes(ctx)
	if err != nil {
		return err
	}
	wasRunning := map[string]bool{}
	for _, p := range procs {
		wasRunning[p.Name] = p.Running()
	}

	for _, name := range diff.Removed {
		if err := r.sv.RemoveGroup(ctx, name); err != nil && !errors.Is(err, ErrNoProcess) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	for _, name := range append(append([]string{}, diff.Added...), diff.Changed...) {
		if err := r.sv.AddGroup(ctx, name); err != nil {
			return fmt.Errorf("load %s: %w", name, err)
		}
	}
	added := map[string]bool{}
	for _, n := range diff.Added {
		added[n] = true
	}
	for _, name := range append(append([]string{}, diff.Added...), diff.Changed...) {
		if wasRunning[name] || (added[name] && r.autostart(ctx, name)) {
			if err := r.sv.Start(ctx, name); err != nil && !errors.Is(err, ErrAlreadyRunning) {
				r.warn(fmt.Sprintf("%s did not start: %v", name, err))
			}
		}
	}
	return nil
}

// Healthy asks supervisord whether it is running.
func (r *reloader) Healthy(ctx context.Context) error {
	state, err := r.sv.State(ctx)
	if err != nil {
		return err
	}
	if state != "RUNNING" {
		return fmt.Errorf("supervisord is %s", state)
	}
	return nil
}

func (r *reloader) warn(s string) {
	r.mu.Lock()
	r.warnings = append(r.warnings, s)
	r.mu.Unlock()
}

// takeWarnings returns and clears the warnings of the apply that just ran.
func (r *reloader) takeWarnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.warnings
	r.warnings = nil
	return out
}
