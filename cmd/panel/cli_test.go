package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// argvCase is one row of the parser table.
type argvCase struct {
	name    string
	argv    []string
	wantCmd string // "" means no command was selected
	wantArg []string
	wantErr bool // any parse error
	usage   bool // the error must be a usageError
	wantIn  string
}

func TestSelectCommand(t *testing.T) {
	cmds := commandSet(io.Discard)

	cases := []argvCase{
		{name: "no arguments", argv: nil, wantErr: true, usage: true, wantIn: "no command given"},
		{name: "help long", argv: []string{"--help"}},
		{name: "help short", argv: []string{"-h"}},
		{name: "help ignores trailing args", argv: []string{"--help", "serve"}},

		{name: "serve", argv: []string{"serve"}, wantCmd: "serve"},
		{name: "serve with daemon flags", argv: []string{"serve", "--dev"}, wantCmd: "serve", wantArg: []string{"--dev"}},
		{name: "install", argv: []string{"install"}, wantCmd: "install"},
		{name: "upgrade", argv: []string{"upgrade"}, wantCmd: "upgrade"},
		{name: "info", argv: []string{"info"}, wantCmd: "info"},
		{name: "reset-password", argv: []string{"reset-password"}, wantCmd: "reset-password"},
		{name: "set", argv: []string{"set", "panel.port", "8443"}, wantCmd: "set", wantArg: []string{"panel.port", "8443"}},
		{name: "firewall restore", argv: []string{"firewall", "restore"}, wantCmd: "firewall restore"},
		{name: "firewall disable", argv: []string{"firewall", "disable"}, wantCmd: "firewall disable"},
		{name: "version", argv: []string{"version"}, wantCmd: "version"},
		{name: "version flag", argv: []string{"--version"}, wantCmd: "version"},

		{name: "set without args", argv: []string{"set"}, wantErr: true, usage: true, wantIn: "missing arguments"},
		{name: "set with one arg", argv: []string{"set", "panel.port"}, wantErr: true, usage: true, wantIn: "missing arguments"},
		{name: "set with three args", argv: []string{"set", "a", "b", "c"}, wantErr: true, usage: true, wantIn: "too many arguments"},
		{name: "info with an argument", argv: []string{"info", "extra"}, wantErr: true, usage: true, wantIn: "too many arguments"},

		{name: "help for a command", argv: []string{"set", "-h"}, wantCmd: "set"},
		{name: "help for a command, long", argv: []string{"serve", "--help"}, wantCmd: "serve"},

		{name: "unknown command", argv: []string{"frobnicate"}, wantErr: true, usage: true, wantIn: `unknown command "frobnicate"`},
		{name: "bare firewall", argv: []string{"firewall"}, wantErr: true, usage: true, wantIn: `unknown command "firewall"`},
		{name: "firewall with an unknown verb", argv: []string{"firewall", "flush"}, wantErr: true, usage: true, wantIn: `unknown command "firewall"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := selectCommand(tc.argv, cmds)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("selectCommand(%q) = %+v, want an error", tc.argv, sel)
				}
				_, isUsage := errors.AsType[*usageError](err)
				if isUsage != tc.usage {
					t.Fatalf("usageError = %v, want %v (err: %v)", isUsage, tc.usage, err)
				}
				if !strings.Contains(err.Error(), tc.wantIn) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.wantIn)
				}
				return
			}

			if err != nil {
				t.Fatalf("selectCommand(%q) returned error: %v", tc.argv, err)
			}
			gotCmd := ""
			if sel.cmd != nil {
				gotCmd = sel.cmd.name
			}
			if gotCmd != tc.wantCmd {
				t.Fatalf("command = %q, want %q", gotCmd, tc.wantCmd)
			}
			if !reflect.DeepEqual(sel.args, tc.wantArg) {
				t.Fatalf("args = %#v, want %#v", sel.args, tc.wantArg)
			}
		})
	}
}

func TestCommandSetIsWellFormed(t *testing.T) {
	cmds := commandSet(io.Discard)
	seen := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		if c.name == "" || c.usage == "" || c.summary == "" {
			t.Errorf("command %+v: name, usage and summary are all required", c)
		}
		if !strings.HasPrefix(c.usage, "panel ") {
			t.Errorf("command %q: usage %q must start with %q", c.name, c.usage, "panel ")
		}
		if seen[c.name] {
			t.Errorf("command %q is registered twice", c.name)
		}
		seen[c.name] = true
		if c.run == nil {
			t.Errorf("command %q has no run function", c.name)
		}
	}
	for _, want := range []string{
		"serve", "install", "upgrade", "info", "reset-password", "set",
		"firewall restore", "firewall disable", "version",
	} {
		if !seen[want] {
			t.Errorf("command %q is missing from the set", want)
		}
	}
}

// TestVersionNeedsNoRoot pins that `version` is the only command exempt from the
// root check.
func TestVersionNeedsNoRoot(t *testing.T) {
	for _, c := range commandSet(io.Discard) {
		if c.noRoot != (c.name == "version") {
			t.Errorf("command %q: noRoot = %v, want %v", c.name, c.noRoot, c.name == "version")
		}
	}
}

// recordingCommand returns a command whose run records its arguments and returns
// the given error, so dispatch can be checked without the real stubs.
func recordingCommand(name string, noRoot bool, runErr error, got *[]string) *command {
	return &command{
		name:    name,
		summary: "test command",
		usage:   "panel " + name,
		maxArgs: -1,
		noRoot:  noRoot,
		run: func(_ context.Context, args []string) error {
			*got = args
			return runErr
		},
	}
}

func TestRunDispatchesAndMapsExitCodes(t *testing.T) {
	boom := errors.New("boom")

	cases := []struct {
		name       string
		argv       []string
		uid        int
		runErr     error
		wantCode   int
		wantCalled bool
		wantArgs   []string
		wantStderr string
	}{
		{
			name: "help exits 0 for a non-root user", argv: []string{"--help"}, uid: 1000,
			wantCode: exitOK,
		},
		{
			name: "command help exits 0 for a non-root user", argv: []string{"work", "-h"}, uid: 1000,
			wantCode: exitOK,
		},
		{
			name: "root command runs as root", argv: []string{"work", "a", "b"}, uid: 0,
			wantCode: exitOK, wantCalled: true, wantArgs: []string{"a", "b"},
		},
		{
			name: "root command refuses a non-root user", argv: []string{"work"}, uid: 1000,
			wantCode: exitError, wantCalled: false, wantStderr: "must run as root",
		},
		{
			name: "noRoot command runs for a non-root user", argv: []string{"version"}, uid: 1000,
			wantCode: exitOK, wantCalled: true,
		},
		{
			name: "run error exits 1", argv: []string{"work"}, uid: 0, runErr: boom,
			wantCode: exitError, wantCalled: true, wantStderr: "boom",
		},
		{
			name: "usage error exits 2", argv: []string{"nope"}, uid: 0,
			wantCode: exitUsage, wantStderr: "unknown command",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := euid
			euid = func() int { return tc.uid }
			t.Cleanup(func() { euid = restore })

			var got []string
			called := false
			wrap := func(name string, noRoot bool) *command {
				c := recordingCommand(name, noRoot, tc.runErr, &got)
				inner := c.run
				c.run = func(ctx context.Context, args []string) error {
					called = true
					return inner(ctx, args)
				}
				return c
			}
			cmds := []*command{wrap("work", false), wrap("version", true)}

			var stdout, stderr bytes.Buffer
			code := runWith(context.Background(), tc.argv, cmds, &stdout, &stderr)

			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr: %q)", code, tc.wantCode, stderr.String())
			}
			if called != tc.wantCalled {
				t.Fatalf("run called = %v, want %v", called, tc.wantCalled)
			}
			if tc.wantCalled && !reflect.DeepEqual(got, tc.wantArgs) {
				t.Fatalf("args = %#v, want %#v", got, tc.wantArgs)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
			if tc.wantCode == exitOK && stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty on success", stderr.String())
			}
		})
	}
}

// TestVersionPrintsVersion is the one command whose output is pinned: it is what
// a release build's smoke run reads.
func TestVersionPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--version"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "dev" {
		t.Fatalf("version output = %q, want %q", got, "dev")
	}
}

// TestFirewallCommandsAreNotStubs pins that M4 landed: both commands must reach
// the database, which a fresh data directory does not have.
func TestFirewallCommandsAreNotStubs(t *testing.T) {
	cmds := commandSet(io.Discard)
	t.Setenv(dataDirEnv, t.TempDir())
	for _, name := range []string{"firewall restore", "firewall disable"} {
		c, _ := lookup(strings.Fields(name), cmds)
		if c == nil {
			t.Fatalf("command %q not found", name)
		}
		err := c.run(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), "panel install") {
			t.Errorf("command %q: error = %v, want the missing-database error", name, err)
		}
	}
}

// TestRecoveryCommandsAreNotStubs pins that step 0.12 landed: the three recovery
// commands must reach the database, not the not-implemented path.
func TestRecoveryCommandsAreNotStubs(t *testing.T) {
	cmds := commandSet(io.Discard)
	t.Setenv(dataDirEnv, t.TempDir())

	for _, name := range []string{"info", "reset-password", "set"} {
		c, _ := lookup(strings.Fields(name), cmds)
		if c == nil {
			t.Fatalf("command %q not found", name)
		}
		args := []string{"panel.port", "8443"}
		if name != "set" {
			args = nil
		}
		// The data directory is empty, so the command must fail for the missing
		// database: that failure is what proves it tried to open one.
		err := c.run(context.Background(), args)
		if err == nil {
			t.Fatalf("command %q succeeded against an empty data directory", name)
		}
		if strings.Contains(err.Error(), "not implemented") {
			t.Fatalf("command %q is still a stub: %v", name, err)
		}
	}
}
