//go:build unix

package cron

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// helperEnv marks a re-executed copy of this test binary as the child process
// that a test starts. The exec tests run real programs without a shell, so no
// `sh -c` string appears in the sources (scripts/lint-conventions.sh).
const helperEnv = "PB_CRON_EXEC_HELPER"

func selfAccount() Account {
	return Account{Name: "self", UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Home: "/"}
}

// helperArgv is argv for running this test binary in helper mode: mode and its
// arguments follow the `--` separator.
func helperArgv(mode string, rest ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestExecHelper$", "--", mode}, rest...)
}

// TestExecHelper is not a test. It is the child process the tests above start,
// and it exits in every branch so the testing framework never prints.
func TestExecHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(64)
	}
	mode, rest := args[1], args[2:]
	switch mode {
	case "exit":
		if len(rest) != 1 {
			os.Exit(64)
		}
		_, _ = fmt.Fprintf(os.Stdout, "out %s\n", rest[0])
		_, _ = fmt.Fprintln(os.Stderr, "err")
		os.Exit(3)
	case "env":
		_, _ = fmt.Fprintf(os.Stdout, "[%s][%s]\n", os.Getenv("PANEL_SECRET_TEST"), os.Getenv("FOO"))
		os.Exit(0)
	case "sleep":
		// The grandchild inherits the output pipe. Killing only this process
		// would leave Run blocked on that pipe.
		child := exec.Command("sleep", "60")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		_ = child.Run()
		os.Exit(0)
	}
	os.Exit(64)
}

func TestOSExecutorRunsArgvAndReportsExitCode(t *testing.T) {
	var out bytes.Buffer
	code, err := OSExecutor{}.Run(context.Background(), Command{
		Path: os.Args[0], Args: helperArgv("exit", "a b"),
		Dir: "/", Env: []string{helperEnv + "=1", "PATH=/usr/bin:/bin"}, User: selfAccount(), Output: &out,
	})
	if err != nil || code != 3 {
		t.Fatalf("Run = %d, %v, want 3, nil", code, err)
	}
	if got := out.String(); !strings.Contains(got, "out a b") || !strings.Contains(got, "err") {
		t.Fatalf("output = %q, want stdout and stderr", got)
	}
}

func TestOSExecutorUsesOnlyTheGivenEnvironment(t *testing.T) {
	t.Setenv("PANEL_SECRET_TEST", "leak")
	var out bytes.Buffer
	_, err := OSExecutor{}.Run(context.Background(), Command{
		Path: os.Args[0], Args: helperArgv("env"),
		Dir: "/", Env: []string{helperEnv + "=1", "FOO=bar"}, User: selfAccount(), Output: &out,
	})
	if err != nil || strings.TrimSpace(out.String()) != "[][bar]" {
		t.Fatalf("output = %q, %v", out.String(), err)
	}
}

func TestOSExecutorKillsTheProcessGroupOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The helper forks a child sleep; killing only the helper would leave
		// Run blocked on the child's inherited output pipe.
		_, _ = OSExecutor{}.Run(ctx, Command{
			Path: os.Args[0], Args: helperArgv("sleep"),
			Dir: "/", Env: []string{helperEnv + "=1", "PATH=/usr/bin:/bin"}, User: selfAccount(), Output: &out,
		})
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestOSExecutorStartFailure(t *testing.T) {
	code, err := OSExecutor{}.Run(context.Background(), Command{
		Path: "/nonexistent/prog", Args: []string{"/nonexistent/prog"}, Dir: "/", User: selfAccount(), Output: &bytes.Buffer{},
	})
	if err == nil || code != -1 {
		t.Fatalf("Run = %d, %v, want -1 and an error", code, err)
	}
}

func TestLookupOSUser(t *testing.T) {
	a, err := LookupOSUser("root")
	if err != nil || a.UID != 0 || a.Name != "root" || a.Home == "" || len(a.Groups) == 0 {
		t.Fatalf("LookupOSUser(root) = %+v, %v", a, err)
	}
	if _, err := LookupOSUser("no-such-user-xyz"); err == nil {
		t.Fatal("LookupOSUser of a missing user succeeded")
	}
}
