package cron

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// Options configures a Service.
type Options struct {
	Store *store.Store
	// OnRunFinished is called after a run's outcome is saved. It runs on the run's goroutine.
	OnRunFinished func(job Job, status Status, errText string)
	// Clock drives scheduling, run timestamps and timeouts; nil means RealClock.
	Clock Clock
	// Executor starts the processes; nil means OSExecutor.
	Executor Executor
	// LookupUser resolves the account a job runs as; nil means LookupOSUser.
	LookupUser func(name string) (Account, error)
	// AllowShell reports whether shell jobs are permitted. It is read on every
	// create, update and run, so a setting change takes effect without a restart.
	// Nil refuses shell jobs.
	AllowShell func() bool
	// LogRoot is the directory run logs go under, as <LogRoot>/<job>/<run>.log
	// (the panel uses /www/panel/logs/cron). Empty disables log files; the output
	// tail is still recorded in the run row.
	LogRoot string
	// ScriptDir is where a shell job's script file is written, as <ScriptDir>/<job>.sh.
	// Empty makes shell jobs fail.
	ScriptDir string
	// DefaultRetention is the number of runs kept for a job that does not say;
	// 0 means DefaultRetention.
	DefaultRetention int
	// Location is the time zone the schedules are read in; nil means time.Local.
	Location *time.Location
	// OutputTailBytes is how much output the run row keeps; 0 means 8 KiB.
	OutputTailBytes int
	// MaxLogBytes caps one run's log file; 0 means 10 MiB.
	MaxLogBytes int64
	// Builtins are the functions built-in jobs run, by name. A built-in job whose
	// name is not here fails each run with that reason.
	Builtins map[string]BuiltinFunc
}

// Service owns the cron jobs, their schedule and their run history.
type Service struct {
	db            *sql.DB
	onRunFinished func(Job, Status, string)
	clock         Clock
	exec          Executor
	lookup        func(string) (Account, error)
	allowShell    func() bool
	logRoot       string
	scriptDir     string
	defRetain     int
	loc           *time.Location
	tailBytes     int
	maxLog        int64
	builtins      map[string]BuiltinFunc

	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	states     map[int64]*jobState
	lastMinute time.Time
}

type jobState struct {
	running int
	queued  bool
	cancels map[int64]context.CancelFunc
}

// New returns a Service.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, errors.New("cron: Store is required")
	}
	s := &Service{
		db: o.Store.DB(), clock: o.Clock, exec: o.Executor, lookup: o.LookupUser, onRunFinished: o.OnRunFinished,
		allowShell: o.AllowShell, logRoot: o.LogRoot, scriptDir: o.ScriptDir,
		defRetain: o.DefaultRetention, loc: o.Location, tailBytes: o.OutputTailBytes,
		maxLog: o.MaxLogBytes, states: make(map[int64]*jobState),
		builtins: make(map[string]BuiltinFunc, len(o.Builtins)),
	}
	for name, fn := range o.Builtins {
		s.builtins[name] = fn
	}
	if s.clock == nil {
		s.clock = RealClock
	}
	if s.exec == nil {
		s.exec = OSExecutor{}
	}
	if s.lookup == nil {
		s.lookup = LookupOSUser
	}
	if s.allowShell == nil {
		s.allowShell = func() bool { return false }
	}
	if s.defRetain == 0 {
		s.defRetain = DefaultRetention
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.tailBytes == 0 {
		s.tailBytes = 8 << 10
	}
	if s.maxLog == 0 {
		s.maxLog = 10 << 20
	}
	s.base, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

// Close cancels every running job and waits for them to finish.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// Wait blocks until no run is in flight. Tests use it to see a run's final state.
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) stamp() string { return s.clock.Now().UTC().Format(time.RFC3339Nano) }

const selectJob = `SELECT id, name, schedule, kind, run_user, argv, script_path, script_body, workdir,
	timeout_seconds, overlap, retention, enabled, created_at, updated_at, builtin FROM cron_jobs`

type scanner interface{ Scan(dest ...any) error }

func scanJob(r scanner) (Job, error) {
	var (
		j                Job
		argv             string
		enabled          int
		created, updated string
	)
	if err := r.Scan(&j.ID, &j.Name, &j.Schedule, &j.Kind, &j.User, &argv, &j.ScriptPath, &j.ScriptBody,
		&j.Workdir, &j.TimeoutSeconds, &j.Overlap, &j.Retention, &enabled, &created, &updated, &j.Builtin); err != nil {
		return Job{}, err
	}
	if err := json.Unmarshal([]byte(argv), &j.Argv); err != nil {
		return Job{}, fmt.Errorf("cron: job %q has a corrupt argv: %w", j.Name, err)
	}
	if len(j.Argv) == 0 {
		j.Argv = nil
	}
	j.Enabled = enabled != 0
	var err error
	if j.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Job{}, fmt.Errorf("cron: job %q created_at: %w", j.Name, err)
	}
	if j.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return Job{}, fmt.Errorf("cron: job %q updated_at: %w", j.Name, err)
	}
	return j, nil
}

// prepare validates a spec and everything about it that needs the host: the shell
// allowlist and the account.
func (s *Service) prepare(spec JobSpec) (JobSpec, error) {
	spec, err := normalize(spec, s.defRetain)
	if err != nil {
		return spec, err
	}
	if spec.Kind == KindShell && !s.allowShell() {
		return spec, ErrShellNotAllowed
	}
	if _, err := s.lookup(spec.User); err != nil {
		return spec, invalid("user %q does not exist", spec.User)
	}
	return spec, nil
}

// Create validates and stores a new job.
func (s *Service) Create(ctx context.Context, spec JobSpec) (Job, error) {
	spec, err := s.prepare(spec)
	if err != nil {
		return Job{}, err
	}
	enabled := spec.Enabled == nil || *spec.Enabled
	argv, _ := json.Marshal(append([]string{}, spec.Argv...))
	now := s.stamp()

	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM cron_jobs WHERE name = ?`, spec.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrDuplicate
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO cron_jobs
			(name, schedule, kind, run_user, argv, script_path, script_body, workdir, timeout_seconds, overlap, retention, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			spec.Name, spec.Schedule, spec.Kind, spec.User, string(argv), spec.ScriptPath, spec.ScriptBody, spec.Workdir,
			spec.TimeoutSeconds, spec.Overlap, spec.Retention, boolInt(enabled), now, now)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
}

// Update replaces a job's definition. The name cannot change.
func (s *Service) Update(ctx context.Context, id int64, spec JobSpec) (Job, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if spec.Name != "" && spec.Name != cur.Name {
		return Job{}, invalid("name cannot be changed (%q to %q)", cur.Name, spec.Name)
	}
	spec.Name = cur.Name
	if cur.Kind == KindBuiltin {
		return s.updateBuiltin(ctx, cur, spec)
	}
	spec, err = s.prepare(spec)
	if err != nil {
		return Job{}, err
	}
	enabled := cur.Enabled
	if spec.Enabled != nil {
		enabled = *spec.Enabled
	}
	argv, _ := json.Marshal(append([]string{}, spec.Argv...))
	_, err = s.db.ExecContext(ctx, `UPDATE cron_jobs SET schedule=?, kind=?, run_user=?, argv=?, script_path=?, script_body=?,
		workdir=?, timeout_seconds=?, overlap=?, retention=?, enabled=?, updated_at=? WHERE id=?`,
		spec.Schedule, spec.Kind, spec.User, string(argv), spec.ScriptPath, spec.ScriptBody, spec.Workdir,
		spec.TimeoutSeconds, spec.Overlap, spec.Retention, boolInt(enabled), s.stamp(), id)
	if err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
}

// SetEnabled turns scheduling of a job on or off. A disabled job can still be run
// by hand. It does not check the host again, so a job whose account has since
// gone can be disabled.
func (s *Service) SetEnabled(ctx context.Context, id int64, enabled bool) (Job, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE cron_jobs SET enabled=?, updated_at=? WHERE id=?`, boolInt(enabled), s.stamp(), id)
	if err != nil {
		return Job{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Job{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes a job, its run history, its log directory and its script file,
// and cancels any run in flight.
func (s *Service) Delete(ctx context.Context, id int64) error {
	job, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if job.Kind == KindBuiltin {
		return fmt.Errorf("%w: %s is part of the panel and cannot be deleted; turn it off instead", ErrBuiltin, job.Name)
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM cron_runs WHERE job_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id = ?`, id)
		return err
	}); err != nil {
		return err
	}

	s.mu.Lock()
	if st := s.states[id]; st != nil {
		st.queued = false
		for _, c := range st.cancels {
			c()
		}
	}
	s.mu.Unlock()

	if s.logRoot != "" {
		_ = os.RemoveAll(filepath.Join(s.logRoot, job.Name))
	}
	if s.scriptDir != "" {
		_ = os.Remove(s.scriptPath(job))
	}
	return nil
}

// Get returns one job.
func (s *Service) Get(ctx context.Context, id int64) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, selectJob+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// List returns every job ordered by name.
func (s *Service) List(ctx context.Context) ([]Job, error) {
	return s.listJobs(ctx, selectJob+` ORDER BY name`)
}

func (s *Service) listJobs(ctx context.Context, query string) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

const selectRun = `SELECT id, job_id, source, started_at, finished_at, exit_code, status, log_path, output_tail, truncated, error FROM cron_runs`

func scanRun(r scanner) (Run, error) {
	var (
		run       Run
		started   string
		finished  sql.NullString
		truncated int
	)
	if err := r.Scan(&run.ID, &run.JobID, &run.Source, &started, &finished, &run.ExitCode, &run.Status,
		&run.LogPath, &run.OutputTail, &truncated, &run.Error); err != nil {
		return Run{}, err
	}
	var err error
	if run.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
		return Run{}, fmt.Errorf("cron: run %d started_at: %w", run.ID, err)
	}
	if finished.Valid {
		t, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return Run{}, fmt.Errorf("cron: run %d finished_at: %w", run.ID, err)
		}
		run.FinishedAt = &t
	}
	run.Truncated = truncated != 0
	return run, nil
}

// Runs returns a job's runs, newest first. limit 0 means 50.
func (s *Service) Runs(ctx context.Context, jobID int64, limit int) ([]Run, error) {
	if _, err := s.Get(ctx, jobID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > MaxRetention {
		limit = MaxRetention
	}
	rows, err := s.db.QueryContext(ctx, selectRun+` WHERE job_id = ? ORDER BY id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRun returns one run.
func (s *Service) GetRun(ctx context.Context, runID int64) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, selectRun+` WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	return r, err
}

func (s *Service) withTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
