package cron

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateValidation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	mod := func(f func(*JobSpec)) JobSpec {
		s := argvSpec("ok")
		f(&s)
		return s
	}
	cases := map[string]JobSpec{
		"empty name":          mod(func(s *JobSpec) { s.Name = "" }),
		"uppercase name":      mod(func(s *JobSpec) { s.Name = "Backup" }),
		"slash in name":       mod(func(s *JobSpec) { s.Name = "a/b" }),
		"dotdot name":         mod(func(s *JobSpec) { s.Name = "a..b" }),
		"leading dash name":   mod(func(s *JobSpec) { s.Name = "-rf" }),
		"long name":           mod(func(s *JobSpec) { s.Name = strings.Repeat("a", 65) }),
		"bad schedule":        mod(func(s *JobSpec) { s.Schedule = "every day" }),
		"no user":             mod(func(s *JobSpec) { s.User = "" }),
		"unknown user":        mod(func(s *JobSpec) { s.User = "nobody-here" }),
		"unknown kind":        mod(func(s *JobSpec) { s.Kind = "shellish" }),
		"empty argv":          mod(func(s *JobSpec) { s.Argv = nil }),
		"relative argv0":      mod(func(s *JobSpec) { s.Argv = []string{"echo", "hi"} }),
		"dotdot argv0":        mod(func(s *JobSpec) { s.Argv = []string{"/bin/../bin/echo"} }),
		"nul in arg":          mod(func(s *JobSpec) { s.Argv = []string{"/bin/echo", "a\x00b"} }),
		"argv with body":      mod(func(s *JobSpec) { s.ScriptBody = "echo hi" }),
		"relative script":     {Name: "s", Schedule: "@daily", Kind: KindScript, User: "www", ScriptPath: "run.sh"},
		"script with argv":    {Name: "s", Schedule: "@daily", Kind: KindScript, User: "www", ScriptPath: "/opt/run.sh", Argv: []string{"/bin/x"}},
		"empty script":        {Name: "s", Schedule: "@daily", Kind: KindScript, User: "www"},
		"relative workdir":    mod(func(s *JobSpec) { s.Workdir = "tmp" }),
		"timeout too small":   mod(func(s *JobSpec) { s.TimeoutSeconds = -1 }),
		"timeout too large":   mod(func(s *JobSpec) { s.TimeoutSeconds = 24*3600 + 1 }),
		"bad overlap":         mod(func(s *JobSpec) { s.Overlap = "maybe" }),
		"retention too large": mod(func(s *JobSpec) { s.Retention = MaxRetention + 1 }),
		"negative retention":  mod(func(s *JobSpec) { s.Retention = -2 }),
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := r.svc.Create(ctx, spec)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Create = %v, want ErrInvalid", err)
			}
		})
	}
	if jobs, _ := r.svc.List(ctx); len(jobs) != 0 {
		t.Fatalf("%d jobs stored after only invalid creates", len(jobs))
	}
}

func TestCreateDefaultsAndRoundTrip(t *testing.T) {
	r := newRig(t)
	j := r.create(t, argvSpec("nightly"))

	if j.ID == 0 || j.Name != "nightly" || j.Kind != KindArgv || j.User != "www" {
		t.Fatalf("job = %+v", j)
	}
	if j.TimeoutSeconds != 3600 || j.Overlap != OverlapSkip || j.Retention != DefaultRetention || !j.Enabled {
		t.Fatalf("defaults not applied: %+v", j)
	}
	if len(j.Argv) != 2 || j.Argv[0] != "/bin/echo" {
		t.Fatalf("argv = %v", j.Argv)
	}
	if !j.CreatedAt.Equal(testStart) {
		t.Fatalf("CreatedAt = %v, want the injected clock %v", j.CreatedAt, testStart)
	}
	next, ok := j.Next(testStart)
	if !ok || !next.Equal(testStart.Truncate(60e9).Add(60e9)) {
		t.Fatalf("Next = %v %v", next, ok)
	}

	got, err := r.svc.Get(context.Background(), j.ID)
	if err != nil || got.Name != "nightly" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := r.svc.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
}

func TestCreateDuplicateName(t *testing.T) {
	r := newRig(t)
	r.create(t, argvSpec("dup"))
	if _, err := r.svc.Create(context.Background(), argvSpec("dup")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Create = %v, want ErrDuplicate", err)
	}
}

func TestShellJobsRequireAllowlist(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	spec := JobSpec{Name: "sh", Schedule: "@daily", Kind: KindShell, User: "www", ScriptBody: "echo hi"}

	if _, err := r.svc.Create(ctx, spec); !errors.Is(err, ErrShellNotAllowed) {
		t.Fatalf("Create with shell disabled = %v, want ErrShellNotAllowed", err)
	}
	r.allowShell.Store(true)
	j := r.create(t, spec)

	// Turning the allowlist off later blocks both updates and runs.
	r.allowShell.Store(false)
	if _, err := r.svc.Update(ctx, j.ID, spec); !errors.Is(err, ErrShellNotAllowed) {
		t.Fatalf("Update with shell disabled = %v, want ErrShellNotAllowed", err)
	}
	res, err := r.svc.RunNow(ctx, j.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	r.svc.Wait()
	run, _ := r.svc.GetRun(ctx, res.Run.ID)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "not enabled") {
		t.Fatalf("run = %+v, want failed because shell jobs are off", run)
	}
	if r.ex.count() != 0 {
		t.Fatal("a disallowed shell job reached the executor")
	}
}

func TestShellJobWritesScriptAndRunsBinSh(t *testing.T) {
	r := newRig(t)
	r.allowShell.Store(true)
	body := "set -e\necho \"$HOME\"\n"
	j := r.create(t, JobSpec{Name: "sh1", Schedule: "@daily", Kind: KindShell, User: "www", ScriptBody: body})

	if _, err := r.svc.RunNow(context.Background(), j.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	r.svc.Wait()

	if r.ex.count() != 1 {
		t.Fatalf("executor called %d times", r.ex.count())
	}
	c := r.ex.call(0)
	wantFile := filepath.Join(r.scripts, "sh1.sh")
	if c.Path != "/bin/sh" || len(c.Args) != 2 || c.Args[0] != "/bin/sh" || c.Args[1] != wantFile {
		t.Fatalf("command = %q %q, want /bin/sh %s", c.Path, c.Args, wantFile)
	}
	got, err := os.ReadFile(wantFile)
	if err != nil || string(got) != body {
		t.Fatalf("script file = %q, %v", got, err)
	}
	if c.User.UID != 1001 || len(c.User.Groups) != 2 {
		t.Fatalf("command user = %+v", c.User)
	}
}

func TestShellJobWithoutScriptDirFails(t *testing.T) {
	r := newRig(t, func(o *Options) { o.ScriptDir = "" })
	r.allowShell.Store(true)
	j := r.create(t, JobSpec{Name: "sh2", Schedule: "@daily", Kind: KindShell, User: "www", ScriptBody: "true"})
	res, _ := r.svc.RunNow(context.Background(), j.ID)
	r.svc.Wait()
	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "script directory") {
		t.Fatalf("run = %+v", run)
	}
}

func TestScriptAndArgvCommands(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	script := r.create(t, JobSpec{Name: "scr", Schedule: "@daily", Kind: KindScript, User: "www", ScriptPath: "/opt/job.sh", Workdir: "/srv"})
	argv := r.create(t, JobSpec{Name: "arg", Schedule: "@daily", Kind: KindArgv, User: "root", Argv: []string{"/usr/bin/touch", "/tmp/x y"}})

	_, _ = r.svc.RunNow(ctx, script.ID)
	r.svc.Wait()
	_, _ = r.svc.RunNow(ctx, argv.ID)
	r.svc.Wait()

	c0, c1 := r.ex.call(0), r.ex.call(1)
	if c0.Path != "/opt/job.sh" || len(c0.Args) != 1 || c0.Dir != "/srv" {
		t.Fatalf("script command = %+v", c0)
	}
	if c1.Path != "/usr/bin/touch" || len(c1.Args) != 2 || c1.Args[1] != "/tmp/x y" || c1.Dir != "/root" {
		t.Fatalf("argv command = %+v (the argument must reach the program as one element)", c1)
	}
	env := strings.Join(c1.Env, " ")
	if !strings.Contains(env, "HOME=/root") || !strings.Contains(env, "USER=root") || !strings.Contains(env, "PATH=") {
		t.Fatalf("env = %v", c1.Env)
	}
}

func TestUpdateAndEnableDisable(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	j := r.create(t, argvSpec("upd"))

	spec := argvSpec("")
	spec.Schedule = "@hourly"
	spec.Overlap = OverlapQueue
	spec.TimeoutSeconds = 30
	spec.Retention = 5
	spec.Argv = []string{"/bin/true"}
	got, err := r.svc.Update(ctx, j.ID, spec)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "upd" || got.Schedule != "@hourly" || got.Overlap != OverlapQueue || got.TimeoutSeconds != 30 ||
		got.Retention != 5 || got.Argv[0] != "/bin/true" || !got.Enabled {
		t.Fatalf("updated job = %+v", got)
	}

	renamed := spec
	renamed.Name = "other"
	if _, err := r.svc.Update(ctx, j.ID, renamed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rename = %v, want ErrInvalid", err)
	}
	bad := spec
	bad.Schedule = "nope"
	if _, err := r.svc.Update(ctx, j.ID, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid update = %v, want ErrInvalid", err)
	}
	if _, err := r.svc.Update(ctx, 404, spec); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing = %v, want ErrNotFound", err)
	}

	off, err := r.svc.SetEnabled(ctx, j.ID, false)
	if err != nil || off.Enabled {
		t.Fatalf("SetEnabled(false) = %+v, %v", off, err)
	}
	disabled := false
	spec.Enabled = &disabled
	if got, _ := r.svc.Update(ctx, j.ID, spec); got.Enabled {
		t.Fatal("Update with Enabled=false left the job enabled")
	}
	on, err := r.svc.SetEnabled(ctx, j.ID, true)
	if err != nil || !on.Enabled {
		t.Fatalf("SetEnabled(true) = %+v, %v", on, err)
	}
	if _, err := r.svc.SetEnabled(ctx, 404, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetEnabled missing = %v", err)
	}
}

func TestListIsOrderedByName(t *testing.T) {
	r := newRig(t)
	r.create(t, argvSpec("zeta"))
	r.create(t, argvSpec("alpha"))
	jobs, err := r.svc.List(context.Background())
	if err != nil || len(jobs) != 2 || jobs[0].Name != "alpha" {
		t.Fatalf("List = %+v, %v", jobs, err)
	}
}

func TestDeleteRemovesRunsLogsAndScript(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.allowShell.Store(true)
	j := r.create(t, JobSpec{Name: "gone", Schedule: "@daily", Kind: KindShell, User: "www", ScriptBody: "echo x"})
	r.ex.handler = func(_ context.Context, c Command) (int, error) {
		_, _ = c.Output.Write([]byte("x\n"))
		return 0, nil
	}
	res, _ := r.svc.RunNow(ctx, j.ID)
	r.svc.Wait()
	run, _ := r.svc.GetRun(ctx, res.Run.ID)
	if _, err := os.Stat(run.LogPath); err != nil {
		t.Fatalf("log file missing before delete: %v", err)
	}

	if err := r.svc.Delete(ctx, j.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.svc.Get(ctx, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if _, err := r.svc.GetRun(ctx, run.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("GetRun after delete = %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.logs, "gone")); !os.IsNotExist(err) {
		t.Fatalf("log directory still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.scripts, "gone.sh")); !os.IsNotExist(err) {
		t.Fatalf("script file still there: %v", err)
	}
	if err := r.svc.Delete(ctx, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
}

func TestRunsOfMissingJob(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Runs(context.Background(), 5, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Runs = %v, want ErrNotFound", err)
	}
	if _, err := r.svc.GetRun(context.Background(), 5); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("GetRun = %v", err)
	}
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New without a Store succeeded")
	}
}
