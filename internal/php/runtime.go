package php

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/native"
)

// ErrNoBuild is a start asked for before any build exists.
var ErrNoBuild = errors.New("php: no build has been made yet; build one first")

// DefaultUnitDir is where the unit files are written.
const DefaultUnitDir = "/etc/systemd/system"

// EnsureAction says what EnsureRunning did.
type EnsureAction string

// What EnsureRunning can do.
const (
	// ActionNone: the version was already running.
	ActionNone EnsureAction = "none"
	// ActionStart: the version was not running and has been started.
	ActionStart EnsureAction = "start"
)

// EnsureResult is the outcome of EnsureRunning.
type EnsureResult struct {
	Action EnsureAction
	Reason string
}

// FlagStore keeps the stopped flag. *images.Catalog satisfies it.
type FlagStore interface {
	Setting(ctx context.Context, name string) (string, bool, error)
	SetSetting(ctx context.Context, name, value string) error
}

// StoppedKey is the stored flag that says the operator stopped this version from
// the panel. While it is set the boot reconcile leaves the version stopped. It is
// per version: stopping 8.3 says nothing about 8.4.
func StoppedKey(ver string) string { return "php." + ver + ".stopped" }

// Runtime manages one PHP version's existence as a service: the unit, whether it
// runs, and whether the operator stopped it.
type Runtime struct {
	p       Paths
	run     Runner
	ver     string
	flags   FlagStore
	unitDir string
}

// NewRuntime returns the runtime of a version.
func NewRuntime(p Paths, run Runner, ver string) *Runtime {
	return &Runtime{p: p, run: run, ver: ver, unitDir: DefaultUnitDir}
}

// WithUnitDir writes the unit file somewhere other than systemd's own directory.
// Tests use it; production does not.
func (r *Runtime) WithUnitDir(dir string) *Runtime {
	r.unitDir = dir
	return r
}

// WithFlags makes the runtime remember a stop across panel restarts.
func (r *Runtime) WithFlags(f FlagStore) *Runtime {
	r.flags = f
	return r
}

// BuildPresent reports whether a build serves: the current link resolves to a
// directory that holds an FPM master.
func (r *Runtime) BuildPresent(context.Context) (bool, error) {
	_, err := os.Stat(filepath.FromSlash(r.p.Binary(r.ver)))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// writeUnit makes the unit file and the paths FPM writes to match what this panel
// expects. It reports whether the unit changed.
func (r *Runtime) writeUnit(ctx context.Context) (bool, error) {
	for _, d := range []string{r.p.RunDir(), r.p.SiteLogDir(), path.Dir(r.p.FPMErrorLog(r.ver))} {
		if err := os.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			return false, err
		}
	}
	// systemd refuses a unit whose ReadWritePaths entry does not exist (226/NAMESPACE),
	// so the log is made before the unit can name it.
	if err := createLogFile(filepath.FromSlash(r.p.FPMErrorLog(r.ver))); err != nil {
		return false, fmt.Errorf("php %s: create %s: %w", r.ver, r.p.FPMErrorLog(r.ver), err)
	}
	file := filepath.Join(r.unitDir, UnitName(r.ver))
	want := UnitText(r.p, r.ver)
	if have, err := os.ReadFile(file); err == nil && string(have) == want {
		return false, nil
	}
	if err := os.WriteFile(file, []byte(want), 0o644); err != nil {
		return false, err
	}
	if out, err := r.run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return true, fmt.Errorf("php %s: systemctl daemon-reload: %w: %s", r.ver, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// EnsureRunning writes the unit, enables it, and starts the version when it is not
// running. A running version is left alone.
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
	name := UnitName(r.ver)
	if out, err := r.run.Run(ctx, "systemctl", "enable", name); err != nil {
		return EnsureResult{}, fmt.Errorf("php %s: systemctl enable: %w: %s", r.ver, err, strings.TrimSpace(string(out)))
	}
	st, err := native.StatusOf(ctx, r.run, unit(r.ver))
	if err != nil {
		return EnsureResult{}, err
	}
	if st.Active {
		reason := "PHP " + r.ver + " is running"
		if changed {
			reason = "PHP " + r.ver + " is running; the unit changed and applies at its next start"
		}
		return EnsureResult{Action: ActionNone, Reason: reason}, nil
	}
	if out, err := r.run.Run(ctx, "systemctl", "start", name); err != nil {
		return EnsureResult{}, fmt.Errorf("php %s: systemctl start: %w: %s", r.ver, err, strings.TrimSpace(string(out)))
	}
	return EnsureResult{Action: ActionStart, Reason: "PHP " + r.ver + " was " + st.State}, nil
}

// Stopped reports whether the operator stopped this version from the panel.
func (r *Runtime) Stopped(ctx context.Context) bool {
	if r.flags == nil {
		return false
	}
	v, ok, err := r.flags.Setting(ctx, StoppedKey(r.ver))
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
	return r.flags.SetSetting(ctx, StoppedKey(r.ver), v)
}

// Start brings the version up on the build that serves. It clears the stopped
// flag first, so a start that fails part-way still leaves the reconcile free to
// retry.
func (r *Runtime) Start(ctx context.Context) (EnsureResult, error) {
	if err := r.setStopped(ctx, false); err != nil {
		return EnsureResult{}, err
	}
	return r.EnsureRunning(ctx)
}

// Stop shuts FPM down gracefully and records that this was the operator's choice,
// so neither the next panel start nor a reboot undoes it. Stopping a version that
// is already stopped only records the choice.
func (r *Runtime) Stop(ctx context.Context) error {
	if err := r.setStopped(ctx, true); err != nil {
		return err
	}
	name := UnitName(r.ver)
	ctx, cancel := context.WithTimeout(ctx, stopTimeout+10*time.Second)
	defer cancel()
	// Disabled first: a stop that took the panel down halfway must not leave the
	// version set to come back at boot.
	_, _ = r.run.Run(ctx, "systemctl", "disable", name)
	if out, err := r.run.Run(ctx, "systemctl", "stop", name); err != nil {
		if strings.Contains(string(out), "not loaded") {
			return nil
		}
		return fmt.Errorf("php %s: systemctl stop: %w: %s", r.ver, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Restart stops the version and starts it again.
func (r *Runtime) Restart(ctx context.Context) (EnsureResult, error) {
	if err := r.Stop(ctx); err != nil {
		return EnsureResult{}, err
	}
	return r.Start(ctx)
}

// Status is the version's unit as systemd reports it.
func (r *Runtime) Status(ctx context.Context) (native.Status, error) {
	return native.StatusOf(ctx, r.run, unit(r.ver))
}

// UnitText is the systemd unit that runs one version's FPM master.
//
// Type=notify: the build is compiled with --with-fpm-systemd, so FPM sends READY=1
// and a pool that fails to start fails the unit instead of leaving it "started".
// The master runs as root and each pool drops to www, as the container did.
//
// ProtectHome=read-only, never true: a web root under /home is hidden by `true`
// even when ReadWritePaths names it (step 0 measured this).
//
// The log is a file: under systemd FPM cannot open /proc/self/fd/2 or /dev/stderr
// and exits 78 before it reads a pool, so [global] error_log names a real file that
// the unit may write and writeUnit creates first.
//
// No NoNewPrivileges: a sendmail wrapper relies on a setgid helper.
func UnitText(p Paths, ver string) string {
	readWrite := []string{p.RunDir(), p.SiteLogDir(), p.FPMErrorLog(ver), p.SitesRoot()}
	readWrite = append(readWrite, p.Roots.List()...)
	slices.Sort(readWrite)
	return fmt.Sprintf(`# Managed by PB panel. Rewritten when it differs; do not edit.
[Unit]
Description=PB panel PHP %[1]s (php-fpm)
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
Environment=PHP_INI_SCAN_DIR=:%[2]s
ExecStartPre=%[3]s -t -y %[4]s -c %[5]s
ExecStart=%[3]s -F -y %[4]s -c %[5]s
ExecReload=/bin/kill -USR2 $MAINPID
KillSignal=SIGQUIT
KillMode=mixed
TimeoutStopSec=%[6]d
Restart=on-failure
RestartSec=1
LimitNOFILE=65536
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
ReadWritePaths=%[7]s
InaccessiblePaths=%[8]s

[Install]
WantedBy=multi-user.target
`, ver, p.ConfDir(ver), p.Binary(ver), p.FPMConf(ver), p.IniPath(ver),
		int(stopTimeout/time.Second), strings.Join(readWrite, " "), path.Join(p.Root, "panel", "data"))
}
