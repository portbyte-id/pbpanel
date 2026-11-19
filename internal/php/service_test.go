package php

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeRunner records the host commands the service and runtime run and answers
// them from fn, so a test scripts what systemd and PHP say.
type fakeRunner struct {
	calls []string
	envs  []string
	fn    func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) RunEnv(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	f.envs = append(f.envs, strings.Join(env, ","))
	if f.fn != nil {
		return f.fn(name, args)
	}
	return nil, nil
}

// exitErr is a command that ran and failed.
type exitErr struct{ code int }

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitErr) ExitCode() int { return e.code }

// show is what `systemctl show` prints for a unit.
func show(load, active string, pid int, since string) []byte {
	return fmt.Appendf(nil, "LoadState=%s\nActiveState=%s\nMainPID=%d\nActiveEnterTimestampMonotonic=%s\n", load, active, pid, since)
}

// unitState scripts systemctl: the unit is in the given state and every other
// command succeeds.
func unitState(load, active string, pid int, since string) func(string, []string) ([]byte, error) {
	return func(name string, args []string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "show" {
			return show(load, active, pid, since), nil
		}
		return nil, nil
	}
}

// phpHost scripts a host: the two validators answer from the test, and systemctl
// reports the unit as active or not.
func phpHost(active bool, validator func(name string) ([]byte, error)) *fakeRunner {
	return &fakeRunner{fn: func(name string, args []string) ([]byte, error) {
		switch {
		case strings.HasSuffix(name, "/sbin/php-fpm") || strings.HasSuffix(name, "/bin/php"):
			if validator != nil {
				return validator(name)
			}
			return nil, nil
		case name == "systemctl" && len(args) > 0 && args[0] == "show":
			if active {
				return show("loaded", "active", 7, "100"), nil
			}
			return show("loaded", "inactive", 0, "0"), nil
		}
		return nil, nil
	}}
}

func TestValidateRunsTheBuildsOwnBinariesWithTheScanDir(t *testing.T) {
	run := phpHost(false, func(string) ([]byte, error) { return []byte("NOTICE: configuration file test is successful\n"), nil })
	if _, err := NewService(run, testPaths, "8.3", false).Validate(ctx); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	want := []string{
		"/www/panel/php/8.3/current/sbin/php-fpm -t -y /www/panel/php/8.3/php-fpm.conf -c /www/panel/php/8.3/php.ini",
		"/www/panel/php/8.3/current/bin/php -c /www/panel/php/8.3/php.ini -d display_startup_errors=1 -d log_errors=0 -r ",
	}
	for i, w := range want {
		if len(run.calls) <= i || run.calls[i] != w {
			t.Errorf("calls = %v, want %q", run.calls, w)
		}
		if run.envs[i] != "PHP_INI_SCAN_DIR=:/www/panel/php/8.3/conf.d" {
			t.Errorf("command %d ran without the scan dir: %q", i, run.envs[i])
		}
	}
}

func TestValidateRejects(t *testing.T) {
	for name, answer := range map[string]func(string) ([]byte, error){
		"non-zero exit": func(string) ([]byte, error) {
			return []byte("ERROR: failed to load configuration file\n"), exitErr{78}
		},
		"ini syntax, exit 0": func(string) ([]byte, error) {
			return []byte("PHP:  syntax error, unexpected '=' in /www/panel/php/8.3/php.ini on line 3\nNOTICE: test is successful\n"), nil
		},
		"bad extension": func(string) ([]byte, error) {
			return []byte("\nWarning: PHP Startup: Unable to load dynamic library 'nope.so'\n"), nil
		},
		"fpm error with zero": func(string) ([]byte, error) {
			return []byte("[17-Nov-2026 12:00:00] ERROR: [pool a] bad value\n"), nil
		},
	} {
		run := phpHost(true, answer)
		out, err := NewService(run, testPaths, "8.3", false).Validate(ctx)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if out == "" {
			t.Errorf("%s: no output returned", name)
		}
	}
	// A notice about an empty include pattern is not a rejection.
	ok := phpHost(true, func(string) ([]byte, error) {
		return []byte("WARNING: Nothing matches the include pattern '/www/panel/php/8.3/pool.d/*.conf'\nNOTICE: configuration file test is successful\n"), nil
	})
	if _, err := NewService(ok, testPaths, "8.3", false).Validate(ctx); err != nil {
		t.Errorf("a clean test was rejected: %v", err)
	}
}

func TestValidateThatCannotRunIsNotARejection(t *testing.T) {
	run := phpHost(true, func(string) ([]byte, error) { return nil, errors.New("no such file or directory") })
	_, err := NewService(run, testPaths, "8.3", false).Validate(ctx)
	if err == nil || !strings.Contains(err.Error(), "could not be run") {
		t.Errorf("Validate = %v, want an error saying the validator could not be run", err)
	}
}

// A runner that cannot carry an environment would silently ignore conf.d, so the
// service says so instead of validating the wrong files.
func TestValidateNeedsARunnerThatCanSetTheScanDir(t *testing.T) {
	plain := &struct{ Runner }{}
	_, err := NewService(plain, testPaths, "8.3", false).Validate(ctx)
	if err == nil || !strings.Contains(err.Error(), "PHP_INI_SCAN_DIR") {
		t.Errorf("Validate with a plain Runner = %v, want it to name the missing scan dir", err)
	}
}

func TestReloadRefusesAStoppedUnitAndSignalsARunningOne(t *testing.T) {
	down := phpHost(false, nil)
	if err := NewService(down, testPaths, "8.3", false).Reload(ctx); err == nil || !strings.Contains(err.Error(), "is inactive") {
		t.Fatalf("Reload of a stopped unit = %v, want a refusal", err)
	}
	if contains(down.calls, "systemctl reload "+UnitName("8.3")) {
		t.Errorf("a stopped unit was signalled: %v", down.calls)
	}

	up := phpHost(true, nil)
	if err := NewService(up, testPaths, "8.3", false).Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if !contains(up.calls, "systemctl reload "+UnitName("8.3")) {
		t.Errorf("calls = %v, want a reload", up.calls)
	}
}

// A first site is the one change that brings a version up: the service built with
// start writes the unit and starts it instead of refusing.
func TestReloadWithStartBringsTheVersionUp(t *testing.T) {
	p := runtimePaths(t)
	installBuild(t, p, "8.3")
	run := &fakeRunner{fn: unitState("loaded", "inactive", 0, "0")}
	svc := NewService(run, p, "8.3", true).WithUnitDir(t.TempDir())
	if err := svc.Reload(ctx); err != nil {
		t.Fatalf("Reload with start = %v", err)
	}
	if contains(run.calls, "systemctl reload panel-php-8.3.service") {
		t.Errorf("a stopped unit was signalled instead of started: %v", run.calls)
	}
	if !contains(run.calls, "systemctl start panel-php-8.3.service") {
		t.Errorf("calls = %v, want a start", run.calls)
	}
}

func TestHealthyCatchesARestartedMaster(t *testing.T) {
	// Healthy reads the unit once for its own status check and twice more inside
	// Stable, so each case gives three answers.
	seq := func(answers ...[]byte) *fakeRunner {
		return &fakeRunner{fn: func(string, []string) ([]byte, error) {
			a := answers[0]
			if len(answers) > 1 {
				answers = answers[1:]
			}
			return a, nil
		}}
	}
	svc := func(r *fakeRunner) *Service { s := NewService(r, testPaths, "8.3", false); s.wait = 0; return s }

	same := show("loaded", "active", 7, "100")
	if err := svc(seq(same, same, same)).Healthy(ctx); err != nil {
		t.Errorf("a steady master was reported unhealthy: %v", err)
	}
	// A changed PID and a changed start time each mean the master died on the new
	// configuration and systemd brought it back.
	for name, after := range map[string][]byte{
		"new PID":   show("loaded", "active", 9, "100"),
		"new Since": show("loaded", "active", 7, "200"),
	} {
		if err := svc(seq(same, same, after)).Healthy(ctx); err == nil || !strings.Contains(err.Error(), "restarted") {
			t.Errorf("%s: a restarted master was not detected: %v", name, err)
		}
	}
	if err := svc(seq(same, same, show("loaded", "failed", 0, "0"))).Healthy(ctx); err == nil {
		t.Error("a failed unit was reported healthy")
	}
	// A version nobody uses has no unit; nothing was reloaded, so it is healthy.
	if err := svc(seq(show("not-found", "inactive", 0, "0"))).Healthy(ctx); err != nil {
		t.Errorf("a version with no unit is not unhealthy: %v", err)
	}
}

// Status is what the HTTP layer reads for a version's `container` row.
func TestStatusReadsTheUnit(t *testing.T) {
	st, found, err := NewService(phpHost(true, nil), testPaths, "8.3", false).Status(ctx)
	if err != nil || !found || !st.Active || st.State != "active" || st.PID != 7 || st.Since != "100" {
		t.Errorf("Status of a running unit = %+v, %v, %v", st, found, err)
	}
	if _, found, _ := NewService(phpHost(false, nil), testPaths, "8.3", false).Status(ctx); !found {
		t.Error("a unit systemd knows was reported as absent")
	}
	if _, found, _ := NewService(&fakeRunner{fn: unitState("not-found", "inactive", 0, "0")}, testPaths, "8.3", false).Status(ctx); found {
		t.Error("a unit systemd does not know was reported as loaded")
	}
}
