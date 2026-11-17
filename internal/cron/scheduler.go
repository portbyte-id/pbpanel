package cron

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Outcome is what a firing did.
type Outcome string

const (
	OutcomeStarted Outcome = "started"
	OutcomeQueued  Outcome = "queued"
	OutcomeSkipped Outcome = "skipped"
)

// RunNowResult reports what RunNow did. Run is zero for OutcomeQueued, because a
// queued firing has no run until the running one ends.
type RunNowResult struct {
	Outcome Outcome
	Run     Run
}

// Run drives the schedule until ctx ends, then cancels the runs in flight and
// waits for them. It first marks runs left "running" by a previous process as
// "interrupted". Firings missed while the panel was down are not replayed: each
// wake-up considers only the minute it woke in, so a long gap (downtime, a
// suspended host, a stalled loop) skips the minutes in between.
func (s *Service) Run(ctx context.Context) error {
	if err := s.markInterrupted(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastMinute = s.clock.Now().Truncate(time.Minute)
	s.mu.Unlock()

	defer func() {
		s.cancel()
		s.wg.Wait()
	}()
	for {
		now := s.clock.Now()
		wait := now.Truncate(time.Minute).Add(time.Minute).Sub(now)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.clock.After(wait):
		}
		s.Tick(ctx, s.clock.Now())
	}
}

// Tick starts every enabled job whose schedule matches the minute containing now.
// It fires each minute at most once and never for a minute before the last one it
// handled. Run calls it; tests call it directly.
func (s *Service) Tick(ctx context.Context, now time.Time) {
	minute := now.Truncate(time.Minute)
	s.mu.Lock()
	if !minute.After(s.lastMinute) {
		s.mu.Unlock()
		return
	}
	s.lastMinute = minute
	s.mu.Unlock()

	jobs, err := s.listJobs(ctx, selectJob+` WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return
	}
	local := minute.In(s.loc)
	for _, j := range jobs {
		sched, err := ParseSchedule(j.Schedule)
		if err != nil || !sched.Matches(local) {
			continue
		}
		_, _ = s.dispatch(ctx, j, SourceSchedule)
	}
}

// RunNow starts a job immediately, subject to its overlap policy, whether or not
// it is enabled.
func (s *Service) RunNow(ctx context.Context, id int64) (RunNowResult, error) {
	job, err := s.Get(ctx, id)
	if err != nil {
		return RunNowResult{}, err
	}
	return s.dispatch(ctx, job, SourceManual)
}

func (s *Service) markInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cron_runs SET status = 'interrupted', finished_at = ?, error = 'the panel stopped while this run was in progress' WHERE status = 'running'`,
		s.stamp())
	return err
}

func (s *Service) state(id int64) *jobState {
	st := s.states[id]
	if st == nil {
		st = &jobState{cancels: make(map[int64]context.CancelFunc)}
		s.states[id] = st
	}
	return st
}

// dispatch applies the overlap policy to one firing.
func (s *Service) dispatch(ctx context.Context, job Job, src Source) (RunNowResult, error) {
	s.mu.Lock()
	st := s.state(job.ID)
	if st.running > 0 && job.Overlap != OverlapAllow {
		if job.Overlap == OverlapQueue && !st.queued {
			st.queued = true
			s.mu.Unlock()
			return RunNowResult{Outcome: OutcomeQueued}, nil
		}
		s.mu.Unlock()
		reason := "the previous run is still in progress"
		if job.Overlap == OverlapQueue {
			reason = "a run is already queued behind the one in progress"
		}
		run, err := s.recordSkipped(ctx, job, src, reason)
		return RunNowResult{Outcome: OutcomeSkipped, Run: run}, err
	}
	st.running++
	s.wg.Add(1)
	s.mu.Unlock()

	run, err := s.start(ctx, job, src)
	if err != nil {
		if s.release(job.ID, 0) {
			s.runQueued(context.WithoutCancel(ctx), job.ID)
		}
		s.wg.Done()
		return RunNowResult{}, err
	}
	return RunNowResult{Outcome: OutcomeStarted, Run: run}, nil
}

// start inserts the run row and launches the goroutine. The caller has already
// counted the run in running and in the wait group.
func (s *Service) start(ctx context.Context, job Job, src Source) (Run, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO cron_runs (job_id, source, started_at, status) VALUES (?, ?, ?, 'running')`,
		job.ID, src, s.stamp())
	if err != nil {
		return Run{}, err
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return Run{}, err
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return Run{}, err
	}

	rctx, cancel := context.WithCancel(s.base)
	s.mu.Lock()
	s.state(job.ID).cancels[runID] = cancel
	s.mu.Unlock()
	go s.execute(rctx, cancel, job, runID)
	return run, nil
}

func (s *Service) recordSkipped(ctx context.Context, job Job, src Source, reason string) (Run, error) {
	now := s.stamp()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO cron_runs (job_id, source, started_at, finished_at, status, error) VALUES (?, ?, ?, ?, 'skipped', ?)`,
		job.ID, src, now, now, reason)
	if err != nil {
		return Run{}, err
	}
	id, _ := res.LastInsertId()
	s.prune(context.WithoutCancel(ctx), job)
	return s.GetRun(ctx, id)
}

// release drops one running count and the cancel handle, and returns whether a
// queued firing is now due.
func (s *Service) release(jobID, runID int64) (queuedDue bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[jobID]
	if st == nil {
		return false
	}
	st.running--
	delete(st.cancels, runID)
	if st.running == 0 && st.queued {
		st.queued = false
		if s.base.Err() == nil {
			st.running++
			s.wg.Add(1)
			return true
		}
	}
	if st.running == 0 && !st.queued {
		delete(s.states, jobID)
	}
	return false
}

type finish struct {
	status    Status
	exitCode  int
	tail      string
	truncated bool
	logPath   string
	errText   string
}

func (s *Service) execute(ctx context.Context, cancel context.CancelFunc, job Job, runID int64) {
	defer s.wg.Done()
	defer cancel()

	f := s.runJob(ctx, job, runID)
	// The context may be canceled by shutdown, but the outcome must still be saved.
	dctx := context.WithoutCancel(ctx)
	_, _ = s.db.ExecContext(dctx,
		`UPDATE cron_runs SET finished_at=?, exit_code=?, status=?, log_path=?, output_tail=?, truncated=?, error=? WHERE id=?`,
		s.stamp(), f.exitCode, f.status, f.logPath, f.tail, boolInt(f.truncated), f.errText, runID)
	s.prune(dctx, job)

	if s.release(job.ID, runID) {
		s.runQueued(dctx, job.ID)
	}
}

// runQueued starts the firing that was held behind a run. The caller has counted
// it in running and in the wait group.
func (s *Service) runQueued(ctx context.Context, jobID int64) {
	job, err := s.Get(ctx, jobID)
	if err == nil {
		_, err = s.start(ctx, job, SourceSchedule)
	}
	if err != nil {
		s.release(jobID, 0)
		s.wg.Done()
	}
}

func (s *Service) runJob(ctx context.Context, job Job, runID int64) finish {
	f := finish{status: StatusFailed, exitCode: -1}
	fail := func(format string, a ...any) finish {
		f.errText = fmt.Sprintf(format, a...)
		return f
	}

	acct, err := s.lookup(job.User)
	if err != nil {
		return fail("user %q: %v", job.User, err)
	}
	cmd, err := s.command(job, acct)
	if err != nil {
		return fail("%v", err)
	}

	tail := &tailBuffer{max: s.tailBytes}
	var out io.Writer = tail
	var logFile *os.File
	var logNote string
	if s.logRoot != "" {
		dir := filepath.Join(s.logRoot, job.Name)
		path := filepath.Join(dir, strconv.FormatInt(runID, 10)+".log")
		if err := os.MkdirAll(dir, 0o750); err == nil {
			logFile, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
			if err == nil {
				f.logPath = path
			} else {
				logNote = fmt.Sprintf("log file: %v", err)
			}
		} else {
			logNote = fmt.Sprintf("log directory: %v", err)
		}
	}
	var capped *cappedWriter
	if logFile != nil {
		capped = &cappedWriter{w: logFile, remaining: s.maxLog}
		out = io.MultiWriter(capped, tail)
		defer func() { _ = logFile.Close() }()
	}
	cmd.Output = out

	// The timeout runs on the injected clock so tests can trigger it without waiting.
	tctx, tcancel := context.WithCancel(ctx)
	defer tcancel()
	var timedOut atomic.Bool
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-s.clock.After(time.Duration(job.TimeoutSeconds) * time.Second):
			timedOut.Store(true)
			tcancel()
		case <-done:
		}
	}()

	code, err := s.exec.Run(tctx, cmd)

	f.exitCode = code
	switch {
	case timedOut.Load():
		f.status = StatusTimeout
		f.errText = fmt.Sprintf("killed after the %ds timeout", job.TimeoutSeconds)
	case ctx.Err() != nil:
		f.status = StatusCanceled
		f.errText = "canceled"
	case err != nil:
		f.status = StatusFailed
		f.errText = err.Error()
	case code == 0:
		f.status = StatusSucceeded
	default:
		f.status = StatusFailed
		f.errText = fmt.Sprintf("exit status %d", code)
	}
	if logNote != "" {
		f.errText = strings.TrimSpace(f.errText + "; " + logNote)
	}
	f.tail = tail.String()
	f.truncated = tail.truncated || (capped != nil && capped.truncated)
	return f
}

// command resolves a job into the process to start.
func (s *Service) command(job Job, acct Account) (Command, error) {
	c := Command{User: acct, Dir: job.Workdir, Env: environ(acct)}
	if c.Dir == "" {
		c.Dir = acct.Home
	}
	if c.Dir == "" {
		c.Dir = "/"
	}
	switch job.Kind {
	case KindArgv:
		c.Path, c.Args = job.Argv[0], job.Argv
	case KindScript:
		c.Path, c.Args = job.ScriptPath, []string{job.ScriptPath}
	case KindShell:
		if !s.allowShell() {
			return c, ErrShellNotAllowed
		}
		p, err := s.writeScript(job, acct)
		if err != nil {
			return c, err
		}
		c.Path, c.Args = "/bin/sh", []string{"/bin/sh", p}
	default:
		return c, fmt.Errorf("cron: job %q has kind %q", job.Name, job.Kind)
	}
	return c, nil
}

func environ(a Account) []string {
	home := a.Home
	if home == "" {
		home = "/"
	}
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + home,
		"USER=" + a.Name,
		"LOGNAME=" + a.Name,
	}
}

func (s *Service) scriptPath(job Job) string {
	return filepath.Join(s.scriptDir, job.Name+".sh")
}

// writeScript renders a shell job's body to its script file. The file is mode 0600
// and, when the panel is root and the job runs as someone else, handed to that
// account, so the script is readable by the job's user and nobody else.
func (s *Service) writeScript(job Job, acct Account) (string, error) {
	if s.scriptDir == "" {
		return "", errShellScriptDirNA
	}
	if err := os.MkdirAll(s.scriptDir, 0o755); err != nil {
		return "", fmt.Errorf("cron: script directory: %w", err)
	}
	final := s.scriptPath(job)
	tmp, err := os.CreateTemp(s.scriptDir, "."+job.Name+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("cron: write script: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	body := job.ScriptBody
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("cron: write script: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("cron: write script: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return "", fmt.Errorf("cron: write script: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(tmp.Name(), int(acct.UID), int(acct.GID)); err != nil {
			return "", fmt.Errorf("cron: write script: %w", err)
		}
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", fmt.Errorf("cron: write script: %w", err)
	}
	return final, nil
}

// prune deletes the runs beyond the job's retention, oldest first, with their
// log files. Runs still in progress are neither counted nor deleted.
func (s *Service) prune(ctx context.Context, job Job) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, log_path FROM cron_runs WHERE job_id = ? AND status != 'running' ORDER BY id DESC LIMIT -1 OFFSET ?`,
		job.ID, job.Retention)
	if err != nil {
		return
	}
	type old struct {
		id  int64
		log string
	}
	var olds []old
	for rows.Next() {
		var o old
		if rows.Scan(&o.id, &o.log) == nil {
			olds = append(olds, o)
		}
	}
	_ = rows.Close()
	for _, o := range olds {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM cron_runs WHERE id = ?`, o.id); err != nil {
			continue
		}
		if o.log != "" {
			_ = os.Remove(o.log)
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.truncated = true
		if len(t.buf) > 2*t.max {
			t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
		}
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	b := t.buf
	if len(b) > t.max {
		b = b[len(b)-t.max:]
	}
	// Cutting at a byte boundary can split a multi-byte rune; drop the fragment.
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return strings.ToValidUTF8(string(b), "�")
}

// cappedWriter forwards up to remaining bytes and silently discards the rest, so
// a chatty job fills neither the disk nor a pipe it is blocked writing to.
type cappedWriter struct {
	w         io.Writer
	remaining int64
	truncated bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	n := int64(len(p))
	if n > c.remaining {
		n = c.remaining
		c.truncated = true
	}
	c.remaining -= n
	if _, err := c.w.Write(p[:n]); err != nil {
		return 0, err
	}
	if c.truncated {
		_, _ = io.WriteString(c.w, "\n[output truncated: log size limit reached]\n")
	}
	return len(p), nil
}
