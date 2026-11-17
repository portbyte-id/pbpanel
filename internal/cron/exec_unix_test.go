//go:build unix

package cron

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func selfAccount() Account {
	return Account{Name: "self", UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Home: "/"}
}

func TestOSExecutorRunsArgvAndReportsExitCode(t *testing.T) {
	var out bytes.Buffer
	code, err := OSExecutor{}.Run(context.Background(), Command{
		Path: "/bin/sh", Args: []string{"/bin/sh", "-c", `echo "out $1"; echo err >&2; exit 3`, "sh", "a b"},
		Dir: "/", Env: []string{"PATH=/usr/bin:/bin"}, User: selfAccount(), Output: &out,
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
		Path: "/bin/sh", Args: []string{"/bin/sh", "-c", `echo "[$PANEL_SECRET_TEST][$FOO]"`},
		Dir: "/", Env: []string{"FOO=bar"}, User: selfAccount(), Output: &out,
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
		// The shell forks a child sleep; killing only the shell would leave Wait
		// blocked on the child's inherited output pipe.
		_, _ = OSExecutor{}.Run(ctx, Command{
			Path: "/bin/sh", Args: []string{"/bin/sh", "-c", "sleep 60 & wait"},
			Dir: "/", Env: []string{"PATH=/usr/bin:/bin"}, User: selfAccount(), Output: &out,
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
