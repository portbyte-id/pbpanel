package nginx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/native"
)

// ErrNoBuild is a start asked for before any build exists.
var ErrNoBuild = errors.New("nginx: no build has been made yet; build one first")

// settle is how long Healthy watches the master after a reload. A config that
// passed `nginx -t` can still make the master exit on a runtime failure; this is
// the window in which that shows. A variable so tests do not wait it out.
var settle = 750 * time.Millisecond

// stopTimeout is how long systemd lets nginx finish its requests after SIGQUIT.
// The unit says the same; this is the budget the panel waits for.
const stopTimeout = 25 * time.Second

// unit is nginx's systemd unit, as the shared helpers name it.
var unit = native.Unit{Product: product, Name: UnitName}

// Runner runs a host command and returns its combined output.
type Runner = native.Runner

// ExecRunner runs commands on this host.
type ExecRunner = native.ExecRunner

// Status is nginx as systemd reports it.
type Status = native.Status

// status asks systemd about the nginx unit.
func status(ctx context.Context, run Runner) (Status, error) { return native.StatusOf(ctx, run, unit) }

// EnsureAction says what EnsureRunning did.
type EnsureAction string

// What EnsureRunning can do.
const (
	// ActionNone: nginx was already running.
	ActionNone EnsureAction = "none"
	// ActionStart: nginx was not running and has been started.
	ActionStart EnsureAction = "start"
)

// EnsureResult is the outcome of EnsureRunning.
type EnsureResult struct {
	Action EnsureAction
	Reason string
}

// Service is nginx as a confedit.Service: it validates with `nginx -t`, reloads
// by asking systemd to signal the master, and checks it is still the same process.
type Service struct {
	p    Paths
	run  Runner
	wait time.Duration
}

// NewService returns the nginx service.
func NewService(p Paths, run Runner) *Service {
	return &Service{p: p, run: run, wait: settle}
}

// Name implements confedit.Service.
func (s *Service) Name() string { return ServiceName }

// Validate runs `nginx -t` with the binary that serves, against the files on
// disk. It needs no running nginx, so a configuration can be checked before nginx
// is first started.
func (s *Service) Validate(ctx context.Context) (string, error) {
	return test(ctx, s.run, s.p.Binary(), s.p, "the configuration")
}

// checkCandidate is the test of a new build: its own binary against the live
// configuration, whether or not anything is running. The binary that serves would
// say nothing about the new one.
func checkCandidate(ctx context.Context, run Runner, p Paths, id string) (string, error) {
	return test(ctx, run, p.BinaryOf(id), p, "the build")
}

func test(ctx context.Context, run Runner, bin string, p Paths, what string) (string, error) {
	out, err := run.Run(ctx, bin, "-t", "-c", p.ConfPath())
	if err == nil {
		return string(out), nil
	}
	if code, ran := native.ExitCode(err); ran {
		return string(out), fmt.Errorf("nginx -t rejected %s (exit %d)", what, code)
	}
	return "", fmt.Errorf("nginx -t could not be run: %w", err)
}

// Reload asks systemd to signal the master. It refuses when nginx is not
// running: a reload that silently did nothing would let an apply report success
// for a config nothing loaded.
func (s *Service) Reload(ctx context.Context) error {
	st, err := status(ctx, s.run)
	if err != nil {
		return fmt.Errorf("nginx reload: %w", err)
	}
	if !st.Active {
		return fmt.Errorf("nginx reload: %s is %s", UnitName, st.State)
	}
	if out, err := s.run.Run(ctx, "systemctl", "reload", UnitName); err != nil {
		return fmt.Errorf("nginx reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Healthy checks that nginx is running and, after a short settle, that it is the
// same process: a master that exited on the new config and was restarted by
// systemd shows as a new run of the unit.
func (s *Service) Healthy(ctx context.Context) error {
	return native.Stable(ctx, s.run, unit, s.wait)
}

// State is nginx as systemd reports it. The bool is false when the unit does not
// exist yet.
func (s *Service) State(ctx context.Context) (Status, bool, error) {
	st, err := status(ctx, s.run)
	return st, st.Loaded, err
}

// ValidateOnly is the nginx service for changes made while nginx is not yet
// running (boot): it validates and does not reload.
type ValidateOnly struct{ s *Service }

// NewValidateOnly returns the validate-only service. It shares the apply lock
// with Service because it has the same name.
func NewValidateOnly(p Paths, run Runner) *ValidateOnly {
	return &ValidateOnly{s: NewService(p, run)}
}

// Name implements confedit.Service.
func (v *ValidateOnly) Name() string { return ServiceName }

// Validate implements confedit.Service.
func (v *ValidateOnly) Validate(ctx context.Context) (string, error) { return v.s.Validate(ctx) }

// Reload does nothing: nginx is about to be started with these files.
func (v *ValidateOnly) Reload(context.Context) error { return nil }

// Healthy does nothing for the same reason.
func (v *ValidateOnly) Healthy(context.Context) error { return nil }

var (
	_ confedit.Service = (*Service)(nil)
	_ confedit.Service = (*ValidateOnly)(nil)
)

// UnitText is the systemd unit that runs nginx. The master runs as root so it can
// bind 80 and 443 and read private keys; the workers drop to the www account the
// build was configured with. Stop is a graceful quit.
func UnitText(p Paths) string {
	return fmt.Sprintf(`# Managed by PB panel. Rewritten when the panel starts; do not edit.
[Unit]
Description=Panel nginx
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=%[1]s -t -c %[2]s
ExecStart=%[1]s -c %[2]s -g "daemon off;"
ExecReload=/bin/kill -HUP $MAINPID
KillSignal=SIGQUIT
KillMode=mixed
TimeoutStopSec=%[3]d
Restart=on-failure
RestartSec=1
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, p.Binary(), p.ConfPath(), int(stopTimeout/time.Second))
}

// DefaultUnitDir is where the unit file is written.
const DefaultUnitDir = "/etc/systemd/system"

// Runtime manages nginx's existence as a service: the unit, whether it runs, and
// whether the operator stopped it.
type Runtime struct {
	p       Paths
	run     Runner
	flags   FlagStore
	unitDir string
	retire  func(ctx context.Context) error
}

// WithRetire sets what runs right before nginx is first started by the unit: it
// removes whatever else holds nginx's ports. A host upgraded from the container
// runtime still has the panel-nginx container serving on 80 and 443, and the
// native nginx cannot bind them until it is gone. It does not run when nginx is
// already running under systemd.
func (r *Runtime) WithRetire(f func(ctx context.Context) error) *Runtime {
	r.retire = f
	return r
}

// NewRuntime returns the nginx runtime.
func NewRuntime(p Paths, run Runner) *Runtime {
	return &Runtime{p: p, run: run, unitDir: DefaultUnitDir}
}

// WithUnitDir writes the unit file somewhere other than systemd's own directory.
// Tests use it; production does not.
func (r *Runtime) WithUnitDir(dir string) *Runtime {
	r.unitDir = dir
	return r
}

// BuildPresent reports whether a build serves: the current link resolves to a
// directory that holds a binary.
func (r *Runtime) BuildPresent(context.Context) (bool, error) {
	_, err := os.Stat(filepath.FromSlash(r.p.Binary()))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// writeUnit makes the unit file and the directories nginx writes to match what
// this panel expects. It reports whether the unit changed.
func (r *Runtime) writeUnit(ctx context.Context) (bool, error) {
	for _, d := range []string{r.p.CacheDir(), r.p.MainLogDir(), r.p.RunDir()} {
		if err := os.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			return false, err
		}
	}
	file := filepath.Join(r.unitDir, UnitName)
	want := UnitText(r.p)
	if have, err := os.ReadFile(file); err == nil && string(have) == want {
		return false, nil
	}
	if err := os.WriteFile(file, []byte(want), 0o644); err != nil {
		return false, err
	}
	if out, err := r.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return true, fmt.Errorf("nginx: systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// EnsureRunning writes the unit, enables it, and starts nginx when it is not
// running. A running nginx is left alone.
func (r *Runtime) EnsureRunning(ctx context.Context) (EnsureResult, error) {
	if ok, err := r.BuildPresent(ctx); err != nil {
		return EnsureResult{}, err
	} else if !ok {
		return EnsureResult{}, ErrNoBuild
	}
	changed, err := r.writeUnit(ctx)
	if err != nil {
		return EnsureResult{}, err
	}
	if out, err := r.run.Run(ctx, "systemctl", "enable", UnitName); err != nil {
		return EnsureResult{}, fmt.Errorf("nginx: systemctl enable: %w: %s", err, strings.TrimSpace(string(out)))
	}
	st, err := status(ctx, r.run)
	if err != nil {
		return EnsureResult{}, err
	}
	if st.Active {
		reason := "nginx is running"
		if changed {
			reason = "nginx is running; the unit changed and applies at its next start"
		}
		return EnsureResult{Action: ActionNone, Reason: reason}, nil
	}
	if r.retire != nil {
		if err := r.retire(ctx); err != nil {
			return EnsureResult{}, fmt.Errorf("nginx: retire the previous runtime: %w", err)
		}
	}
	if out, err := r.run.Run(ctx, "systemctl", "start", UnitName); err != nil {
		return EnsureResult{}, fmt.Errorf("nginx: systemctl start: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return EnsureResult{Action: ActionStart, Reason: "nginx was " + st.State}, nil
}

// StoppedKey is the stored flag that says the operator stopped nginx from the
// panel. While it is set the boot reconcile leaves nginx stopped.
const StoppedKey = "nginx.stopped"

// FlagStore keeps the stopped flag. *images.Catalog satisfies it.
type FlagStore interface {
	Setting(ctx context.Context, name string) (string, bool, error)
	SetSetting(ctx context.Context, name, value string) error
}

// Stopped reports whether the operator stopped nginx from the panel.
func (r *Runtime) Stopped(ctx context.Context) bool {
	if r.flags == nil {
		return false
	}
	v, ok, err := r.flags.Setting(ctx, StoppedKey)
	return err == nil && ok && v == "1"
}

func (r *Runtime) setStopped(ctx context.Context, stopped bool) error {
	if r.flags == nil {
		return nil
	}
	v := "0"
	if stopped {
		v = "1"
	}
	return r.flags.SetSetting(ctx, StoppedKey, v)
}

// WithFlags makes the runtime remember a stop across panel restarts.
func (r *Runtime) WithFlags(f FlagStore) *Runtime {
	r.flags = f
	return r
}

// Start brings nginx up on the build that serves. It clears the stopped flag
// first, so a start that fails part-way still leaves the reconcile free to retry.
func (r *Runtime) Start(ctx context.Context) (EnsureResult, error) {
	if err := r.setStopped(ctx, false); err != nil {
		return EnsureResult{}, err
	}
	return r.EnsureRunning(ctx)
}

// Stop shuts nginx down gracefully and records that this was the operator's
// choice, so neither the next panel start nor a reboot undoes it. Stopping
// nginx that is already stopped only records the choice.
func (r *Runtime) Stop(ctx context.Context) error {
	if err := r.setStopped(ctx, true); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, stopTimeout+10*time.Second)
	defer cancel()
	// Disabled first: a stop that took the panel down halfway must not leave nginx
	// set to come back at boot.
	_, _ = r.run.Run(ctx, "systemctl", "disable", UnitName)
	if out, err := r.run.Run(ctx, "systemctl", "stop", UnitName); err != nil {
		if strings.Contains(string(out), "not loaded") {
			return nil
		}
		return fmt.Errorf("nginx: systemctl stop: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Restart stops nginx and starts it again.
func (r *Runtime) Restart(ctx context.Context) (EnsureResult, error) {
	if err := r.Stop(ctx); err != nil {
		return EnsureResult{}, err
	}
	return r.Start(ctx)
}

// Status is nginx as systemd reports it.
func (r *Runtime) Status(ctx context.Context) (Status, error) { return status(ctx, r.run) }

// restartUnit restarts a running nginx onto the binary the current link names and
// waits until it holds.
func restartUnit(ctx context.Context, run Runner) error {
	if out, err := run.Run(ctx, "systemctl", "restart", UnitName); err != nil {
		return fmt.Errorf("systemctl restart: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return native.Stable(ctx, run, unit, settle)
}
