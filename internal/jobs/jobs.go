// Package jobs runs background jobs, persists their output and streams it over SSE.
//
// A job is a Go function, never a command line: the daemon does not build shell
// strings (development plan §1.4), so a runner receives a context and an output
// writer and does its own work through typed clients.
//
// Output is appended line by line to `job_output`, capped per job. When the cap
// is reached the oldest lines are deleted and the job records that truncation
// happened, so a client that reconnects knows its replay is incomplete rather
// than believing the start of the log is the start of the run.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// DefaultMaxLines is how many output lines one job keeps. It is a constructor
// option today and becomes a settings key when the registry (step 0.4) lands.
const DefaultMaxLines = 2000

const (
	// exitCanceled is the shell convention for a job stopped by the operator.
	exitCanceled = 130
	exitFailed   = 1

	// subBuffer is how far a live subscriber may lag before it is dropped. A
	// dropped subscriber reconnects with Last-Event-ID and replays from the
	// database, so a slow browser costs itself a re-fetch, never the job.
	subBuffer = 256

	// timeLayout is fixed width so string comparison and parse round-trip agree;
	// time.RFC3339Nano trims trailing zeros and would sort inconsistently.
	timeLayout = "2006-01-02T15:04:05.000Z"
)

// State is where a job is in its life cycle.
type State string

// Job states. Queued, running and terminal: only the first two are non-terminal.
const (
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
	StateCanceled    State = "canceled"
	StateInterrupted State = "interrupted"
)

// Terminal reports that the job will not produce more output.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCanceled, StateInterrupted:
		return true
	}
	return false
}

var (
	// ErrNotFound is returned for a job id the database does not know.
	ErrNotFound = errors.New("jobs: job not found")
	// ErrNotRunning is returned by Cancel for a job that is not live in this
	// process: it finished, or the daemon restarted and it is now interrupted.
	ErrNotRunning = errors.New("jobs: job is not running")
	// ErrFinished is returned when output is written to a job that already ended.
	ErrFinished = errors.New("jobs: job has finished")
	// errCanceled is the cancellation cause Cancel installs, which is how the
	// runner tells an operator cancel from a daemon shutdown.
	errCanceled = errors.New("jobs: canceled by operator")
)

// Job is one background job as stored in the `jobs` table.
type Job struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	State      State      `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   int        `json:"exit_code"`
	Err        string     `json:"error"`
	// LastSeq is the highest sequence number ever issued. It keeps counting
	// after old lines are dropped, so it stays comparable with Last-Event-ID.
	LastSeq int64 `json:"last_seq"`
	// Dropped is how many lines the cap has removed.
	Dropped int64 `json:"dropped"`
	// Truncated reports that Dropped > 0: replay starts mid-stream.
	Truncated bool `json:"truncated"`
}

// Line is one output line with the sequence number a client resumes from.
type Line struct {
	Seq  int64  `json:"seq"`
	Text string `json:"line"`
}

// Func is a job body. It runs on its own goroutine; ctx is canceled by Cancel or
// by the parent context the manager was started with. A nil return means the job
// succeeded.
type Func func(ctx context.Context, out *Writer) error

// Option configures a Manager.
type Option func(*Manager)

// WithMaxLines sets how many output lines one job keeps.
func WithMaxLines(n int) Option {
	return func(m *Manager) { m.maxLines = n }
}

// Manager owns the live jobs of one daemon.
type Manager struct {
	store    *store.Store
	maxLines int
	now      func() time.Time

	mu   sync.Mutex
	runs map[string]*run
}

// run is the in-memory half of a job: its cancel function, the sequence
// counter, and the live subscribers.
type run struct {
	id     string
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu       sync.Mutex
	nextSeq  int64
	finished bool
	subs     map[chan Line]struct{}

	// final and finishErr are written before done is closed, so a reader that
	// waits on done sees them without taking the mutex.
	final     Job
	finishErr error
}

// New opens the manager and marks every job left non-terminal by a previous
// process as interrupted. That is the daemon-start rule: a job whose goroutine
// died with the process can never produce output again, and pretending it is
// still running would leave the UI streaming a stream that will never move.
func New(st *store.Store, opts ...Option) (*Manager, error) {
	if st == nil {
		return nil, errors.New("jobs: store is nil")
	}
	m := &Manager{
		store:    st,
		maxLines: DefaultMaxLines,
		now:      time.Now,
		runs:     make(map[string]*run),
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.maxLines < 1 {
		return nil, fmt.Errorf("jobs: max lines must be at least 1, got %d", m.maxLines)
	}
	if err := m.recoverInterrupted(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

// MaxLines reports the output cap in effect.
func (m *Manager) MaxLines() int { return m.maxLines }

func (m *Manager) recoverInterrupted(ctx context.Context) error {
	// `queued` is included because a job is only ever queued for the instant
	// between its insert and its goroutine starting, so a queued row at boot
	// belongs to a process that died before it ran.
	const q = `UPDATE jobs
	           SET state = ?, finished_at = ?, exit_code = ?, error = ?
	           WHERE state IN (?, ?)`
	now := m.now().UTC().Format(timeLayout)
	_, err := m.store.DB().ExecContext(ctx, q,
		string(StateInterrupted), now, exitFailed, "daemon restarted before the job finished",
		string(StateQueued), string(StateRunning))
	if err != nil {
		return fmt.Errorf("jobs: mark interrupted: %w", err)
	}
	return nil
}

// Start records a job and runs fn on its own goroutine. The returned Job is
// already running; fn may finish before Start returns.
func (m *Manager) Start(ctx context.Context, kind string, fn Func) (Job, error) {
	if strings.TrimSpace(kind) == "" {
		return Job{}, errors.New("jobs: kind is required")
	}
	if fn == nil {
		return Job{}, errors.New("jobs: runner is nil")
	}

	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	created := m.now().UTC()

	const insert = `INSERT INTO jobs (id, kind, state, created_at) VALUES (?, ?, ?, ?)`
	if _, err := m.store.DB().ExecContext(ctx, insert, id, kind, string(StateQueued), created.Format(timeLayout)); err != nil {
		return Job{}, fmt.Errorf("jobs: create %s: %w", kind, err)
	}

	jobCtx, cancel := context.WithCancelCause(ctx)
	r := &run{
		id:      id,
		ctx:     jobCtx,
		cancel:  cancel,
		done:    make(chan struct{}),
		nextSeq: 1,
		subs:    make(map[chan Line]struct{}),
	}
	m.mu.Lock()
	m.runs[id] = r
	m.mu.Unlock()

	// queued -> running happens here rather than in the goroutine so a caller
	// that reads the job straight after Start sees a running job, not a race.
	started := m.now().UTC()
	const markRunning = `UPDATE jobs SET state = ?, started_at = ? WHERE id = ?`
	if _, err := m.store.DB().ExecContext(ctx, markRunning, string(StateRunning), started.Format(timeLayout), id); err != nil {
		cancel(errCanceled)
		m.mu.Lock()
		delete(m.runs, id)
		m.mu.Unlock()
		return Job{}, fmt.Errorf("jobs: start %s: %w", kind, err)
	}

	go func() { _ = m.run(r, fn) }()

	return Job{
		ID:        id,
		Kind:      kind,
		State:     StateRunning,
		CreatedAt: created,
		StartedAt: &started,
	}, nil
}

// run is the job goroutine. Its error is the final database write: it has no
// caller to return to, so it is kept on the run and surfaced by Wait.
func (m *Manager) run(r *run, fn Func) error {
	defer close(r.done)

	runErr := fn(r.ctx, &Writer{m: m, r: r})

	state, code, msg := StateSucceeded, 0, ""
	switch cause := context.Cause(r.ctx); {
	case errors.Is(cause, errCanceled):
		state, code, msg = StateCanceled, exitCanceled, "canceled"
	case cause != nil:
		state, code, msg = StateInterrupted, exitFailed, "daemon shut down before the job finished"
	case runErr != nil:
		state, code, msg = StateFailed, exitFailed, runErr.Error()
	}

	r.mu.Lock()
	r.finished = true
	r.mu.Unlock()

	r.final, r.finishErr = m.finish(r.id, state, code, msg)

	r.mu.Lock()
	for ch := range r.subs {
		close(ch)
	}
	r.subs = nil
	r.mu.Unlock()

	m.mu.Lock()
	delete(m.runs, r.id)
	m.mu.Unlock()

	return r.finishErr
}

// finish writes the terminal row. The job context may already be canceled, so
// the write gets its own deadline; five seconds is the same order as the
// database busy timeout.
func (m *Manager) finish(id string, state State, code int, msg string) (Job, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 5*time.Second)
	defer cancel()

	now := m.now().UTC()
	const q = `UPDATE jobs SET state = ?, finished_at = ?, exit_code = ?, error = ? WHERE id = ?`
	if _, err := m.store.DB().ExecContext(ctx, q, string(state), now.Format(timeLayout), code, msg, id); err != nil {
		return Job{}, fmt.Errorf("jobs: finish %s: %w", id, err)
	}
	return m.Get(ctx, id)
}

// Cancel stops a running job by canceling its context. The runner decides how
// quickly it notices; the job is recorded `canceled` once it returns.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return ErrNotRunning
	}
	r.cancel(errCanceled)
	return nil
}

// Wait blocks until the job reaches a terminal state and returns the stored row.
// For a job this process never ran (or already forgot) it returns the row as it
// stands, which is terminal by construction.
func (m *Manager) Wait(ctx context.Context, id string) (Job, error) {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		return m.Get(ctx, id)
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		return Job{}, ctx.Err()
	}
	if r.finishErr != nil {
		return Job{}, r.finishErr
	}
	return r.final, nil
}

// Get reads one job.
func (m *Manager) Get(ctx context.Context, id string) (Job, error) {
	const q = `SELECT id, kind, state, created_at, started_at, finished_at,
	                  exit_code, error, last_seq, dropped, truncated
	           FROM jobs WHERE id = ?`
	j, err := scanJob(m.store.DB().QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("jobs: read %s: %w", id, err)
	}
	return j, nil
}

// ListOptions narrows List.
type ListOptions struct {
	// Active keeps only queued and running jobs.
	Active bool
	// Limit caps the result. Zero means DefaultListLimit; larger values are
	// clamped to MaxListLimit.
	Limit int
}

const (
	// DefaultListLimit is how many jobs List returns when the caller names no limit.
	DefaultListLimit = 100
	// MaxListLimit is the most jobs one List call returns.
	MaxListLimit = 500
)

// List returns jobs newest first. With Active set, only queued and running jobs
// are returned, so a long job is never pushed out of the page by newer short ones.
func (m *Manager) List(ctx context.Context, opts ListOptions) ([]Job, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	q := `SELECT id, kind, state, created_at, started_at, finished_at,
	             exit_code, error, last_seq, dropped, truncated
	      FROM jobs`
	args := make([]any, 0, 3)
	if opts.Active {
		q += ` WHERE state IN (?, ?)`
		args = append(args, string(StateQueued), string(StateRunning))
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := m.store.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Job, 0)
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobs: list: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	return out, nil
}

// rowScanner is the part of *sql.Row and *sql.Rows that scanJob reads.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob reads one row in the column order Get and List select.
func scanJob(s rowScanner) (Job, error) {
	var (
		j                 Job
		created           string
		started, finished sql.NullString
		truncated         int
	)
	if err := s.Scan(
		&j.ID, &j.Kind, &j.State, &created, &started, &finished,
		&j.ExitCode, &j.Err, &j.LastSeq, &j.Dropped, &truncated,
	); err != nil {
		return Job{}, err
	}

	var err error
	if j.CreatedAt, err = parseTime(created); err != nil {
		return Job{}, err
	}
	if j.StartedAt, err = parseNullTime(started); err != nil {
		return Job{}, err
	}
	if j.FinishedAt, err = parseNullTime(finished); err != nil {
		return Job{}, err
	}
	j.Truncated = truncated == 1
	return j, nil
}

// Replay returns the stored lines after seq, in order. This is the catch-up half
// of the SSE contract: a client that sends Last-Event-ID: N gets N+1 onwards.
func (m *Manager) Replay(ctx context.Context, id string, after int64) ([]Line, error) {
	const q = `SELECT seq, line FROM job_output WHERE job_id = ? AND seq > ? ORDER BY seq`
	rows, err := m.store.DB().QueryContext(ctx, q, id, after)
	if err != nil {
		return nil, fmt.Errorf("jobs: replay %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Line
	for rows.Next() {
		var ln Line
		if err := rows.Scan(&ln.Seq, &ln.Text); err != nil {
			return nil, fmt.Errorf("jobs: replay %s: %w", id, err)
		}
		out = append(out, ln)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: replay %s: %w", id, err)
	}
	return out, nil
}

// Subscribe returns a channel of live lines and the function that ends the
// subscription. The channel is closed when the job finishes, or immediately for
// a job that is not live in this process — the caller then relies on Replay.
func (m *Manager) Subscribe(id string) (<-chan Line, func(), error) {
	ch := make(chan Line, subBuffer)

	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		close(ch)
		return ch, func() {}, nil
	}

	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		close(ch)
		return ch, func() {}, nil
	}
	r.subs[ch] = struct{}{}
	r.mu.Unlock()

	unsubscribe := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, live := r.subs[ch]; live {
			delete(r.subs, ch)
			close(ch)
		}
	}
	return ch, unsubscribe, nil
}

// Writer appends output to one job. It is safe for concurrent use.
type Writer struct {
	m *Manager
	r *run
}

// Line appends one line of output. A string containing newlines is split, so
// the stored unit is always a line.
func (w *Writer) Line(s string) error {
	for _, part := range splitLines(s) {
		if err := w.append(part); err != nil {
			return err
		}
	}
	return nil
}

// Printf formats and appends one line.
func (w *Writer) Printf(format string, args ...any) error {
	return w.Line(fmt.Sprintf(format, args...))
}

func (w *Writer) append(text string) error {
	r := w.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return ErrFinished
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}

	seq := r.nextSeq
	cutoff := seq - int64(w.m.maxLines)

	err := w.m.store.WithTx(r.ctx, func(tx *sql.Tx) error {
		const insert = `INSERT INTO job_output (job_id, seq, line) VALUES (?, ?, ?)`
		if _, err := tx.Exec(insert, r.id, seq, text); err != nil {
			return err
		}
		// Oldest-first: the cap keeps the newest maxLines lines, which is what a
		// reader of a long build log actually needs.
		if cutoff > 0 {
			res, err := tx.Exec(`DELETE FROM job_output WHERE job_id = ? AND seq <= ?`, r.id, cutoff)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				const mark = `UPDATE jobs SET dropped = dropped + ?, truncated = 1 WHERE id = ?`
				if _, err := tx.Exec(mark, n, r.id); err != nil {
					return err
				}
			}
		}
		const bump = `UPDATE jobs SET last_seq = ? WHERE id = ?`
		_, err := tx.Exec(bump, seq, r.id)
		return err
	})
	if err != nil {
		return fmt.Errorf("jobs: append line: %w", err)
	}

	r.nextSeq = seq + 1
	line := Line{Seq: seq, Text: text}
	for ch := range r.subs {
		select {
		case ch <- line:
		default:
			// A subscriber that cannot keep up is dropped and reconnects with
			// Last-Event-ID; blocking here would stall the job for a slow tab.
			close(ch)
			delete(r.subs, ch)
		}
	}
	return nil
}

func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad timestamp %q: %w", s, err)
	}
	return t, nil
}

func parseNullTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("jobs: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
