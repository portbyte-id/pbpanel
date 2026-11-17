package cron

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTickFiresMatchingEnabledJobsOncePerMinute(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	every5 := argvSpec("every5")
	every5.Schedule = "*/5 * * * *"
	r.create(t, every5)
	off := argvSpec("off")
	disabled := false
	off.Enabled = &disabled
	r.create(t, off)
	daily := argvSpec("daily")
	daily.Schedule = "@daily"
	r.create(t, daily)

	r.svc.Tick(ctx, time.Date(2026, 11, 17, 10, 4, 59, 0, time.UTC))
	r.svc.Wait()
	if r.ex.count() != 0 {
		t.Fatalf("fired at 10:04: %d runs", r.ex.count())
	}

	r.svc.Tick(ctx, time.Date(2026, 11, 17, 10, 5, 0, 0, time.UTC))
	r.svc.Wait()
	if r.ex.count() != 1 {
		t.Fatalf("at 10:05 want exactly the */5 job, got %d runs", r.ex.count())
	}

	// A second tick inside the same minute, or for an earlier one, does nothing.
	r.svc.Tick(ctx, time.Date(2026, 11, 17, 10, 5, 40, 0, time.UTC))
	r.svc.Tick(ctx, time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	r.svc.Wait()
	if r.ex.count() != 1 {
		t.Fatalf("duplicate tick fired again: %d runs", r.ex.count())
	}

	r.svc.Tick(ctx, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	r.svc.Wait()
	if r.ex.count() != 3 { // */5 matches 00:00 too
		t.Fatalf("at midnight want 2 more runs (*/5 and @daily), got %d total", r.ex.count())
	}
}

func TestTickReadsScheduleInConfiguredLocation(t *testing.T) {
	jakarta := time.FixedZone("WIB", 7*3600)
	r := newRig(t, func(o *Options) { o.Location = jakarta })
	spec := argvSpec("local")
	spec.Schedule = "0 17 * * *" // 17:00 WIB is 10:00 UTC
	r.create(t, spec)

	r.svc.Tick(context.Background(), time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	r.svc.Wait()
	if r.ex.count() != 1 {
		t.Fatalf("runs = %d, want 1 at 17:00 in the configured zone", r.ex.count())
	}
}

func TestRunLoopOnFakeClockDoesNotReplayMissedRuns(t *testing.T) {
	r := newRig(t)
	j := r.create(t, argvSpec("each-minute"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.svc.Run(ctx) }()

	waitFor(t, "loop to sleep", func() bool { return r.clk.schedulerWaits() >= 1 })
	if r.ex.count() != 0 {
		t.Fatal("job ran before its minute started")
	}

	// Start is 10:00:30; the next minute boundary is 10:01:00.
	r.clk.Advance(30 * time.Second)
	waitFor(t, "second sleep", func() bool { return r.clk.schedulerWaits() >= 2 })
	r.svc.Wait()
	if got := r.ex.count(); got != 1 {
		t.Fatalf("after 10:01:00 runs = %d, want 1", got)
	}

	// The panel is "down" for three hours (the clock jumps, the loop is stuck):
	// exactly one run happens when it wakes, not one per missed minute.
	r.clk.Advance(3 * time.Hour)
	waitFor(t, "third sleep", func() bool { return r.clk.schedulerWaits() >= 3 })
	r.svc.Wait()
	if got := r.ex.count(); got != 2 {
		t.Fatalf("after a 3h gap runs = %d, want 2 (missed minutes are not replayed)", got)
	}

	r.clk.Advance(time.Minute)
	waitFor(t, "fourth sleep", func() bool { return r.clk.schedulerWaits() >= 4 })
	r.svc.Wait()
	if got := r.ex.count(); got != 3 {
		t.Fatalf("after the next minute runs = %d, want 3", got)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	runs := r.runs(t, j.ID)
	if len(runs) != 3 || countStatus(runs, StatusSucceeded) != 3 {
		t.Fatalf("runs = %+v", runs)
	}
	for _, run := range runs {
		if run.Source != SourceSchedule {
			t.Errorf("run %d source = %s", run.ID, run.Source)
		}
	}
}

func TestRunLoopDoesNotFireTheMinuteItStartedIn(t *testing.T) {
	r := newRig(t) // starts at 10:00:30
	r.create(t, argvSpec("each-minute"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.svc.Run(ctx) }()
	waitFor(t, "loop to sleep", func() bool { return r.clk.schedulerWaits() >= 1 })
	r.svc.Tick(ctx, testStart) // the loop's own minute: already considered
	r.svc.Wait()
	if r.ex.count() != 0 {
		t.Fatalf("the start minute fired: %d runs", r.ex.count())
	}
	cancel()
	<-done
}

func TestRunMarksLeftoverRunningRunsInterrupted(t *testing.T) {
	r := newRig(t)
	j := r.create(t, argvSpec("crashy"))
	if _, err := r.db.DB().Exec(
		`INSERT INTO cron_runs (job_id, source, started_at, status) VALUES (?, 'schedule', ?, 'running')`,
		j.ID, "2026-11-17T09:00:00Z"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.svc.Run(ctx) }()
	waitFor(t, "loop to sleep", func() bool { return r.clk.schedulerWaits() >= 1 })
	cancel()
	<-done

	runs := r.runs(t, j.ID)
	if len(runs) != 1 || runs[0].Status != StatusInterrupted || runs[0].FinishedAt == nil {
		t.Fatalf("runs = %+v, want one interrupted run", runs)
	}
}

// blockingExec makes each call wait for a release, so a test controls how long runs last.
type blockingExec struct {
	*fakeExec
	started chan struct{}
	release chan struct{}
	active  atomic.Int32
	maxSeen atomic.Int32
}

func newBlocking() *blockingExec {
	b := &blockingExec{fakeExec: &fakeExec{}, started: make(chan struct{}, 16), release: make(chan struct{}, 16)}
	b.handler = func(ctx context.Context, c Command) (int, error) {
		n := b.active.Add(1)
		for {
			m := b.maxSeen.Load()
			if n <= m || b.maxSeen.CompareAndSwap(m, n) {
				break
			}
		}
		defer b.active.Add(-1)
		b.started <- struct{}{}
		select {
		case <-b.release:
			return 0, nil
		case <-ctx.Done():
			return -1, nil
		}
	}
	return b
}

func (b *blockingExec) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a run to start")
	}
}

func TestOverlapSkip(t *testing.T) {
	b := newBlocking()
	r := newRig(t, func(o *Options) { o.Executor = b })
	spec := argvSpec("skipper")
	spec.Overlap = OverlapSkip
	j := r.create(t, spec)
	ctx := context.Background()

	first, err := r.svc.RunNow(ctx, j.ID)
	if err != nil || first.Outcome != OutcomeStarted {
		t.Fatalf("first RunNow = %+v, %v", first, err)
	}
	b.waitStarted(t)

	second, err := r.svc.RunNow(ctx, j.ID)
	if err != nil || second.Outcome != OutcomeSkipped || second.Run.Status != StatusSkipped {
		t.Fatalf("second RunNow = %+v, %v, want skipped", second, err)
	}
	if !strings.Contains(second.Run.Error, "still in progress") {
		t.Fatalf("skip reason = %q", second.Run.Error)
	}
	// A scheduled firing while running is skipped too.
	r.svc.Tick(ctx, time.Date(2026, 11, 17, 10, 1, 0, 0, time.UTC))

	b.release <- struct{}{}
	r.svc.Wait()

	if b.count() != 1 {
		t.Fatalf("executor calls = %d, want 1", b.count())
	}
	runs := r.runs(t, j.ID)
	if countStatus(runs, StatusSucceeded) != 1 || countStatus(runs, StatusSkipped) != 2 {
		t.Fatalf("runs = %+v, want 1 succeeded and 2 skipped", runs)
	}

	// With the first run finished a new firing runs again.
	if res, _ := r.svc.RunNow(ctx, j.ID); res.Outcome != OutcomeStarted {
		t.Fatalf("RunNow after completion = %+v", res)
	}
	b.waitStarted(t)
	b.release <- struct{}{}
	r.svc.Wait()
}

func TestOverlapQueueHoldsExactlyOne(t *testing.T) {
	b := newBlocking()
	r := newRig(t, func(o *Options) { o.Executor = b })
	spec := argvSpec("queuer")
	spec.Overlap = OverlapQueue
	j := r.create(t, spec)
	ctx := context.Background()

	if res, _ := r.svc.RunNow(ctx, j.ID); res.Outcome != OutcomeStarted {
		t.Fatalf("first = %+v", res)
	}
	b.waitStarted(t)

	second, _ := r.svc.RunNow(ctx, j.ID)
	if second.Outcome != OutcomeQueued || second.Run.ID != 0 {
		t.Fatalf("second = %+v, want queued with no run yet", second)
	}
	third, _ := r.svc.RunNow(ctx, j.ID)
	if third.Outcome != OutcomeSkipped || !strings.Contains(third.Run.Error, "queued") {
		t.Fatalf("third = %+v, want skipped because one is already queued", third)
	}
	if b.count() != 1 {
		t.Fatalf("executor calls while queued = %d, want 1", b.count())
	}

	// Finishing the first run starts the queued one by itself.
	b.release <- struct{}{}
	b.waitStarted(t)
	if b.count() != 2 {
		t.Fatalf("queued run did not start: %d calls", b.count())
	}
	b.release <- struct{}{}
	r.svc.Wait()

	runs := r.runs(t, j.ID)
	if countStatus(runs, StatusSucceeded) != 2 || countStatus(runs, StatusSkipped) != 1 || len(runs) != 3 {
		t.Fatalf("runs = %+v", runs)
	}
	if b.maxSeen.Load() != 1 {
		t.Fatalf("runs overlapped under the queue policy (max %d concurrent)", b.maxSeen.Load())
	}

	// The queue is empty again: the next firing while idle simply starts.
	if res, _ := r.svc.RunNow(ctx, j.ID); res.Outcome != OutcomeStarted {
		t.Fatalf("after drain = %+v", res)
	}
	b.waitStarted(t)
	b.release <- struct{}{}
	r.svc.Wait()
}

func TestOverlapAllowRunsConcurrently(t *testing.T) {
	b := newBlocking()
	r := newRig(t, func(o *Options) { o.Executor = b })
	spec := argvSpec("parallel")
	spec.Overlap = OverlapAllow
	j := r.create(t, spec)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		res, err := r.svc.RunNow(ctx, j.ID)
		if err != nil || res.Outcome != OutcomeStarted {
			t.Fatalf("RunNow %d = %+v, %v", i, res, err)
		}
		b.waitStarted(t)
	}
	if b.maxSeen.Load() != 2 {
		t.Fatalf("max concurrent = %d, want 2", b.maxSeen.Load())
	}
	b.release <- struct{}{}
	b.release <- struct{}{}
	r.svc.Wait()
	if runs := r.runs(t, j.ID); countStatus(runs, StatusSucceeded) != 2 || len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestOverlapIsPerJob(t *testing.T) {
	b := newBlocking()
	r := newRig(t, func(o *Options) { o.Executor = b })
	a := r.create(t, argvSpec("job-a"))
	c := r.create(t, argvSpec("job-b"))
	ctx := context.Background()

	_, _ = r.svc.RunNow(ctx, a.ID)
	b.waitStarted(t)
	res, _ := r.svc.RunNow(ctx, c.ID)
	if res.Outcome != OutcomeStarted {
		t.Fatalf("other job = %+v, want started", res)
	}
	b.waitStarted(t)
	b.release <- struct{}{}
	b.release <- struct{}{}
	r.svc.Wait()
}

func TestTimeoutKillsTheRun(t *testing.T) {
	r := newRig(t)
	killed := make(chan struct{})
	r.ex.handler = func(ctx context.Context, c Command) (int, error) {
		_, _ = c.Output.Write([]byte("partial\n"))
		<-ctx.Done()
		close(killed)
		return -1, nil
	}
	spec := argvSpec("slow")
	spec.TimeoutSeconds = 90
	j := r.create(t, spec)

	res, _ := r.svc.RunNow(context.Background(), j.ID)
	waitFor(t, "timeout to be armed", func() bool { return r.clk.timeoutWaits() >= 1 })

	r.clk.Advance(89 * time.Second)
	select {
	case <-killed:
		t.Fatal("killed before the timeout")
	case <-time.After(20 * time.Millisecond):
	}
	r.clk.Advance(time.Second)
	r.svc.Wait()
	<-killed

	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.Status != StatusTimeout || !strings.Contains(run.Error, "90s") || run.OutputTail != "partial\n" {
		t.Fatalf("run = %+v", run)
	}
}

func TestRunStatusFromExit(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		err    error
		status Status
		errHas string
	}{
		{"success", 0, nil, StatusSucceeded, ""},
		{"nonzero", 3, nil, StatusFailed, "exit status 3"},
		{"start failure", -1, errors.New("fork/exec: permission denied"), StatusFailed, "permission denied"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.ex.handler = func(context.Context, Command) (int, error) { return c.code, c.err }
			j := r.create(t, argvSpec("status"))
			res, _ := r.svc.RunNow(context.Background(), j.ID)
			r.svc.Wait()
			run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
			if run.Status != c.status || run.ExitCode != c.code || !strings.Contains(run.Error, c.errHas) || run.FinishedAt == nil {
				t.Fatalf("run = %+v", run)
			}
		})
	}
}

func TestUnknownUserAtRunTimeFailsTheRun(t *testing.T) {
	r := newRig(t)
	j := r.create(t, argvSpec("orphan"))
	r.svc.lookup = func(string) (Account, error) { return Account{}, errors.New("no such user") }
	res, _ := r.svc.RunNow(context.Background(), j.ID)
	r.svc.Wait()
	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "no such user") || r.ex.count() != 0 {
		t.Fatalf("run = %+v, calls = %d", run, r.ex.count())
	}
}

func TestRunNowWorksOnDisabledJobAndIsManual(t *testing.T) {
	r := newRig(t)
	j := r.create(t, argvSpec("manual"))
	_, _ = r.svc.SetEnabled(context.Background(), j.ID, false)
	res, err := r.svc.RunNow(context.Background(), j.ID)
	if err != nil || res.Outcome != OutcomeStarted || res.Run.Source != SourceManual {
		t.Fatalf("RunNow = %+v, %v", res, err)
	}
	r.svc.Wait()
	if _, err := r.svc.RunNow(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RunNow missing = %v", err)
	}
}

func TestOutputIsCapturedToLogAndRunRow(t *testing.T) {
	r := newRig(t)
	r.ex.handler = func(_ context.Context, c Command) (int, error) {
		_, _ = c.Output.Write([]byte("line one\n"))
		_, _ = c.Output.Write([]byte("line two\n"))
		return 0, nil
	}
	j := r.create(t, argvSpec("talker"))
	res, _ := r.svc.RunNow(context.Background(), j.ID)
	r.svc.Wait()

	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	wantPath := filepath.Join(r.logs, "talker", "1.log")
	if run.LogPath != wantPath {
		t.Fatalf("LogPath = %q, want %q", run.LogPath, wantPath)
	}
	data, err := os.ReadFile(run.LogPath)
	if err != nil || string(data) != "line one\nline two\n" {
		t.Fatalf("log = %q, %v", data, err)
	}
	if run.OutputTail != "line one\nline two\n" || run.Truncated {
		t.Fatalf("run = %+v", run)
	}
}

func TestOutputTailAndLogAreBounded(t *testing.T) {
	r := newRig(t, func(o *Options) { o.OutputTailBytes = 10; o.MaxLogBytes = 25 })
	r.ex.handler = func(_ context.Context, c Command) (int, error) {
		for i := 0; i < 10; i++ {
			_, _ = c.Output.Write([]byte("0123456789"))
		}
		return 0, nil
	}
	j := r.create(t, argvSpec("loud"))
	res, _ := r.svc.RunNow(context.Background(), j.ID)
	r.svc.Wait()

	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.OutputTail != "0123456789" || !run.Truncated {
		t.Fatalf("tail = %q truncated=%v", run.OutputTail, run.Truncated)
	}
	data, _ := os.ReadFile(run.LogPath)
	if !strings.HasPrefix(string(data), strings.Repeat("0123456789", 2)+"01234") || !strings.Contains(string(data), "truncated") {
		t.Fatalf("log = %q", data)
	}
	if len(data) > 25+80 {
		t.Fatalf("log is %d bytes, cap not applied", len(data))
	}
}

func TestRetentionKeepsNewestRunsAndDeletesTheirLogs(t *testing.T) {
	r := newRig(t)
	r.ex.handler = func(_ context.Context, c Command) (int, error) {
		_, _ = c.Output.Write([]byte("x"))
		return 0, nil
	}
	spec := argvSpec("keeper")
	spec.Retention = 2
	j := r.create(t, spec)
	var paths []string
	for i := 0; i < 4; i++ {
		res, err := r.svc.RunNow(context.Background(), j.ID)
		if err != nil {
			t.Fatal(err)
		}
		r.svc.Wait()
		run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
		paths = append(paths, run.LogPath)
	}

	runs := r.runs(t, j.ID)
	if len(runs) != 2 || runs[0].ID != 4 || runs[1].ID != 3 {
		t.Fatalf("kept runs = %+v, want ids 4 and 3", runs)
	}
	for i, p := range paths {
		_, err := os.Stat(p)
		if i < 2 && !os.IsNotExist(err) {
			t.Errorf("log %s of a pruned run still exists (%v)", p, err)
		}
		if i >= 2 && err != nil {
			t.Errorf("log %s of a kept run is gone: %v", p, err)
		}
	}
}

func TestRetentionAlsoTrimsSkippedRuns(t *testing.T) {
	b := newBlocking()
	r := newRig(t, func(o *Options) { o.Executor = b })
	spec := argvSpec("trim")
	spec.Retention = 3
	j := r.create(t, spec)
	ctx := context.Background()

	_, _ = r.svc.RunNow(ctx, j.ID)
	b.waitStarted(t)
	for i := 0; i < 6; i++ {
		_, _ = r.svc.RunNow(ctx, j.ID)
	}
	if got := len(r.runs(t, j.ID)); got != 4 { // 3 skipped kept + the running one
		t.Fatalf("rows while running = %d, want 4", got)
	}
	b.release <- struct{}{}
	r.svc.Wait()
	if got := len(r.runs(t, j.ID)); got != 3 {
		t.Fatalf("rows after finish = %d, want 3", got)
	}
}

func TestCloseCancelsRunningJobs(t *testing.T) {
	b := newBlocking()
	r := newRig(t, func(o *Options) { o.Executor = b })
	j := r.create(t, argvSpec("long"))
	res, _ := r.svc.RunNow(context.Background(), j.ID)
	b.waitStarted(t)

	r.svc.Close()
	run, _ := r.svc.GetRun(context.Background(), res.Run.ID)
	if run.Status != StatusCanceled {
		t.Fatalf("run = %+v, want canceled", run)
	}
}

func TestDeleteCancelsRunningJob(t *testing.T) {
	b := newBlocking()
	r := newRig(t, func(o *Options) { o.Executor = b })
	j := r.create(t, argvSpec("doomed"))
	_, _ = r.svc.RunNow(context.Background(), j.ID)
	b.waitStarted(t)

	if err := r.svc.Delete(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	r.svc.Wait() // returns only because Delete canceled the run
	if b.active.Load() != 0 {
		t.Fatal("run still active after delete")
	}
}
