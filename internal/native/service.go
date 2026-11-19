package native

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner runs a host command and returns its combined output. The error of a
// command that ran and failed carries ExitCode() (*exec.ExitError does).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands on this host.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// ExitCode is a failed command's exit status, and false when the command did not
// run at all.
func ExitCode(err error) (int, bool) {
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}

// Unit is one systemd unit and the product it runs. The product is what the
// errors below call the thing, so a shared helper never reports "nginx" to a PHP
// operator.
type Unit struct {
	Product string // nginx, php
	Name    string // panel-nginx.service
}

// Status is a unit as systemd reports it.
type Status struct {
	// Loaded says systemd knows the unit: false before the first start, when the
	// unit file has not been written.
	Loaded bool `json:"loaded"`
	// Active says the unit is running.
	Active bool `json:"active"`
	// State is systemd's ActiveState: active, inactive, failed, activating...
	State string `json:"state"`
	// PID is the main process, 0 when none runs.
	PID int `json:"pid"`
	// Since identifies this run of the unit: it changes when the unit restarts.
	Since string `json:"since"`
}

// StatusOf asks systemd about the unit.
func StatusOf(ctx context.Context, run Runner, u Unit) (Status, error) {
	out, err := run.Run(ctx, "systemctl", "show", "-p", "LoadState", "-p", "ActiveState", "-p", "MainPID", "-p", "ActiveEnterTimestampMonotonic", u.Name)
	if err != nil {
		return Status{}, fmt.Errorf("%s: ask systemd about %s: %w: %s", u.Product, u.Name, err, strings.TrimSpace(string(out)))
	}
	var st Status
	for line := range strings.SplitSeq(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "LoadState":
			st.Loaded = v == "loaded"
		case "ActiveState":
			st.State = v
		case "MainPID":
			st.PID, _ = strconv.Atoi(v)
		case "ActiveEnterTimestampMonotonic":
			st.Since = v
		}
	}
	st.Active = st.State == "active"
	return st, nil
}

// Stable checks that the unit is running and, after a short settle, that it is
// the same process: a main process that exited on a new configuration and was
// restarted by systemd shows as a new run of the unit.
func Stable(ctx context.Context, run Runner, u Unit, wait time.Duration) error {
	before, err := Running(ctx, run, u)
	if err != nil {
		return err
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return ctx.Err()
	}
	after, err := Running(ctx, run, u)
	if err != nil {
		return err
	}
	if after.PID != before.PID || after.Since != before.Since {
		return fmt.Errorf("%s restarted right after the change: the new configuration made the master exit", u.Product)
	}
	return nil
}

// Running is the unit's status, as an error when it is not active.
func Running(ctx context.Context, run Runner, u Unit) (Status, error) {
	st, err := StatusOf(ctx, run, u)
	if err != nil {
		return st, fmt.Errorf("%s health: %w", u.Product, err)
	}
	if !st.Active {
		return st, fmt.Errorf("%s health: %s is %s", u.Product, u.Name, st.State)
	}
	return st, nil
}
