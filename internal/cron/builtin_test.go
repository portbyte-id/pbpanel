package cron

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubBuiltin is the function the registered tls.renew built-in runs; each test
// sets what it does.
type stubBuiltin struct {
	mu sync.Mutex
	fn BuiltinFunc
}

func (s *stubBuiltin) set(fn BuiltinFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
}

func (s *stubBuiltin) run(ctx context.Context, out io.Writer) error {
	s.mu.Lock()
	fn := s.fn
	s.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, out)
}

// builtinRig is a rig whose tls.renew built-in is the stub, with the default jobs
// already seeded.
func builtinRig(t *testing.T) (*rig, *stubBuiltin) {
	t.Helper()
	stub := &stubBuiltin{}
	r := newRig(t, func(o *Options) {
		o.Builtins = map[string]BuiltinFunc{BuiltinSSLRenew: stub.run, BuiltinNotifySweep: stub.run}
	})
	if err := r.svc.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	return r, stub
}

func sslRenew(t *testing.T, r *rig) Job {
	t.Helper()
	jobs, err := r.svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, j := range jobs {
		if j.Name == "ssl-renew" {
			return j
		}
	}
	t.Fatal("ssl-renew was not seeded")
	return Job{}
}

func TestBuiltinKindIsRefusedOnCreate(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Create(context.Background(), JobSpec{
		Name: "sneaky", Schedule: "@daily", Kind: KindBuiltin, User: "root",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create(builtin) = %v, want ErrInvalid", err)
	}
	if jobs, _ := r.svc.List(context.Background()); len(jobs) != 0 {
		t.Fatalf("%d jobs stored after a refused builtin create", len(jobs))
	}
}

func TestEnsureDefaultsSeedsTheRenewalJob(t *testing.T) {
	r, _ := builtinRig(t)
	j := sslRenew(t, r)

	if j.Kind != KindBuiltin || j.Builtin != BuiltinSSLRenew {
		t.Fatalf("kind/builtin = %q/%q", j.Kind, j.Builtin)
	}
	if j.Schedule != "17 3 * * *" || j.User != "root" || !j.Enabled {
		t.Fatalf("seeded job = %+v", j)
	}
	if j.TimeoutSeconds != 7200 || j.Overlap != OverlapSkip || j.Retention != DefaultRetention {
		t.Fatalf("policy = timeout %d, overlap %q, retention %d", j.TimeoutSeconds, j.Overlap, j.Retention)
	}
}

// A second start must not undo what the operator changed: schedule, enabled
// state, overlap, timeout and retention all stay.
func TestEnsureDefaultsKeepsOperatorEdits(t *testing.T) {
	r, _ := builtinRig(t)
	j := sslRenew(t, r)

	off := false
	edited, err := r.svc.Update(context.Background(), j.ID, JobSpec{
		Schedule: "5 4 * * 1", TimeoutSeconds: 600, Overlap: OverlapQueue, Retention: 7, Enabled: &off,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if edited.Schedule != "5 4 * * 1" || edited.Enabled || edited.Retention != 7 || edited.TimeoutSeconds != 600 || edited.Overlap != OverlapQueue {
		t.Fatalf("edited = %+v", edited)
	}

	if err := r.svc.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("second EnsureDefaults: %v", err)
	}
	again := sslRenew(t, r)
	if again.ID != j.ID || again.Schedule != "5 4 * * 1" || again.Enabled || again.Retention != 7 ||
		again.TimeoutSeconds != 600 || again.Overlap != OverlapQueue {
		t.Fatalf("second start changed the job: %+v", again)
	}
	if jobs, _ := r.svc.List(context.Background()); len(jobs) != 2 {
		t.Fatalf("%d jobs after two seeds, want 2 (ssl-renew and notify-sweep)", len(jobs))
	}
}

func TestEnsureDefaultsNeedsTheBuiltinRegistered(t *testing.T) {
	r := newRig(t)
	if err := r.svc.EnsureDefaults(context.Background()); err == nil {
		t.Fatal("EnsureDefaults succeeded without the built-in registered")
	}
	if jobs, _ := r.svc.List(context.Background()); len(jobs) != 0 {
		t.Fatalf("%d jobs seeded without their built-in", len(jobs))
	}
}

// A job of another kind already holding the name is not turned into the built-in.
func TestEnsureDefaultsRefusesAForeignJobWithTheName(t *testing.T) {
	r, _ := builtinRig(t)
	ctx := context.Background()
	// Delete refuses a built-in, so the seeded row is removed directly.
	if _, err := r.db.DB().ExecContext(ctx, `DELETE FROM cron_jobs WHERE name IN ('ssl-renew', 'notify-sweep')`); err != nil {
		t.Fatalf("removing the seeded job: %v", err)
	}
	r.create(t, argvSpec("ssl-renew"))

	if err := r.svc.EnsureDefaults(ctx); err == nil {
		t.Fatal("EnsureDefaults accepted a name held by an argv job")
	}
	jobs, _ := r.svc.List(ctx)
	var argv int
	for _, j := range jobs {
		if j.Name == "ssl-renew" && j.Kind == KindArgv {
			argv++
		}
	}
	if argv != 1 {
		t.Fatalf("jobs = %+v, want the argv job untouched", jobs)
	}
}

func TestBuiltinRejectsChangesToKindAccountAndCommand(t *testing.T) {
	r, _ := builtinRig(t)
	ctx := context.Background()
	j := sslRenew(t, r)

	cases := map[string]JobSpec{
		"kind to argv":   {Kind: KindArgv, Argv: []string{"/bin/echo"}},
		"kind to shell":  {Kind: KindShell, ScriptBody: "echo hi"},
		"account":        {User: "www"},
		"argv":           {Argv: []string{"/bin/echo", "hi"}},
		"script path":    {ScriptPath: "/opt/run.sh"},
		"script body":    {ScriptBody: "echo hi"},
		"working folder": {Workdir: "/tmp"},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			change.Schedule = j.Schedule
			_, err := r.svc.Update(ctx, j.ID, change)
			if !errors.Is(err, ErrBuiltin) {
				t.Fatalf("Update = %v, want ErrBuiltin", err)
			}
		})
	}
	if after := sslRenew(t, r); after.Kind != KindBuiltin || after.User != "root" || after.Builtin != BuiltinSSLRenew {
		t.Fatalf("job changed by a refused update: %+v", after)
	}
}

func TestBuiltinSchedulePolicyCanBeEdited(t *testing.T) {
	r, _ := builtinRig(t)
	j := sslRenew(t, r)

	// The form sends the same kind and account back; those must be accepted.
	got, err := r.svc.Update(context.Background(), j.ID, JobSpec{
		Kind: KindBuiltin, User: "root", Schedule: " @daily ", TimeoutSeconds: 900, Retention: 20,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Schedule != "@daily" || got.TimeoutSeconds != 900 || got.Retention != 20 || got.Kind != KindBuiltin || got.Builtin != BuiltinSSLRenew {
		t.Fatalf("updated = %+v", got)
	}

	if _, err := r.svc.Update(context.Background(), j.ID, JobSpec{Schedule: "every day"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad schedule = %v, want ErrInvalid", err)
	}
	if _, err := r.svc.Update(context.Background(), j.ID, JobSpec{Schedule: "@daily", TimeoutSeconds: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad timeout = %v, want ErrInvalid", err)
	}
}

func TestBuiltinCannotBeDeleted(t *testing.T) {
	r, _ := builtinRig(t)
	j := sslRenew(t, r)

	err := r.svc.Delete(context.Background(), j.ID)
	if !errors.Is(err, ErrBuiltin) {
		t.Fatalf("Delete = %v, want ErrBuiltin", err)
	}
	if _, err := r.svc.Get(context.Background(), j.ID); err != nil {
		t.Fatalf("job gone after a refused delete: %v", err)
	}
}

func TestBuiltinRunSucceedsAndKeepsItsOutput(t *testing.T) {
	r, stub := builtinRig(t)
	j := sslRenew(t, r)
	stub.set(func(_ context.Context, out io.Writer) error {
		_, _ = io.WriteString(out, "example.com: renewed\n2 checked: 1 renewed, 0 failed\n")
		return nil
	})

	res, err := r.svc.RunNow(context.Background(), j.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	r.svc.Wait()

	run, err := r.svc.GetRun(context.Background(), res.Run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != StatusSucceeded || run.ExitCode != 0 || run.Source != SourceManual {
		t.Fatalf("run = %+v", run)
	}
	if !strings.Contains(run.OutputTail, "2 checked") {
		t.Fatalf("output tail = %q", run.OutputTail)
	}
	logged, err := os.ReadFile(run.LogPath)
	if err != nil {
		t.Fatalf("reading the run log: %v", err)
	}
	if !strings.Contains(string(logged), "example.com: renewed") {
		t.Fatalf("log = %q", logged)
	}
	if n := r.ex.count(); n != 0 {
		t.Fatalf("the executor ran %d times for a built-in", n)
	}
}

func TestBuiltinErrorFailsTheRun(t *testing.T) {
	r, stub := builtinRig(t)
	j := sslRenew(t, r)
	stub.set(func(_ context.Context, out io.Writer) error {
		_, _ = io.WriteString(out, "a.example.com: failed: boom\n")
		return errors.New("1 certificate(s) failed to renew")
	})

	res, err := r.svc.RunNow(context.Background(), j.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	r.svc.Wait()
	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.Status != StatusFailed || run.ExitCode != 1 || !strings.Contains(run.Error, "failed to renew") {
		t.Fatalf("run = %+v", run)
	}
	if !strings.Contains(run.OutputTail, "boom") {
		t.Fatalf("output tail = %q", run.OutputTail)
	}
}

// A panic ends its run as a failure; the panel and the next run carry on.
func TestBuiltinPanicFailsOnlyItsRun(t *testing.T) {
	r, stub := builtinRig(t)
	j := sslRenew(t, r)
	stub.set(func(context.Context, io.Writer) error { panic("boom") })

	res, err := r.svc.RunNow(context.Background(), j.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	r.svc.Wait()
	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "panicked") {
		t.Fatalf("run = %+v", run)
	}

	stub.set(nil)
	res, err = r.svc.RunNow(context.Background(), j.ID)
	if err != nil {
		t.Fatalf("second RunNow: %v", err)
	}
	r.svc.Wait()
	if run, _ := r.svc.GetRun(context.Background(), res.Run.ID); run.Status != StatusSucceeded {
		t.Fatalf("the run after a panic = %+v", run)
	}
}

func TestBuiltinTimeoutKillsTheRun(t *testing.T) {
	r, stub := builtinRig(t)
	j := sslRenew(t, r)
	if _, err := r.svc.Update(context.Background(), j.ID, JobSpec{Schedule: j.Schedule, TimeoutSeconds: 90}); err != nil {
		t.Fatalf("Update timeout: %v", err)
	}
	stub.set(func(ctx context.Context, out io.Writer) error {
		_, _ = io.WriteString(out, "scanning\n")
		<-ctx.Done()
		return ctx.Err()
	})

	res, _ := r.svc.RunNow(context.Background(), j.ID)
	waitFor(t, "timeout to be armed", func() bool { return r.clk.timeoutWaits() >= 1 })
	r.clk.Advance(90 * time.Second)
	r.svc.Wait()

	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.Status != StatusTimeout || !strings.Contains(run.Error, "90s") {
		t.Fatalf("run = %+v", run)
	}
}
