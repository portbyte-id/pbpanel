package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMirrorPath is where the production panel appends its audit trail.
const DefaultMirrorPath = "/www/panel/logs/audit.log"

// Result is the outcome an entry records.
type Result string

const (
	Success Result = "success"
	Failure Result = "failure"
)

// ErrInvalidEntry is returned for an entry the writer refuses to store.
var ErrInvalidEntry = errors.New("audit: invalid entry")

// Entry is one audited action. Detail is any value that marshals to JSON: a map,
// a struct, or nil for "nothing to add".
type Entry struct {
	Actor  string
	Action string
	Target string
	Result Result
	Detail any
}

// Writer stores entries in the audit_log table and mirrors them to a JSON-lines
// file. The database is the source of truth; the mirror is best-effort, because a
// full or read-only log filesystem must never fail the operation being audited.
type Writer struct {
	db         *sql.DB
	mirrorPath string
	logger     *slog.Logger

	mu           sync.Mutex
	mirrorBroken bool
}

// Option configures a Writer.
type Option func(*Writer)

// WithMirrorPath replaces the production log path. Tests point this at a temp dir.
func WithMirrorPath(path string) Option {
	return func(w *Writer) { w.mirrorPath = path }
}

// WithLogger sets where a mirror failure is reported.
func WithLogger(logger *slog.Logger) Option {
	return func(w *Writer) { w.logger = logger }
}

// New returns a Writer over db.
func New(db *sql.DB, opts ...Option) *Writer {
	w := &Writer{
		db:         db,
		mirrorPath: DefaultMirrorPath,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Write records one entry. The row is written first: if the database rejects the
// entry the caller gets an error and no mirror line is emitted, so the file can
// never claim something the database does not have. A mirror failure is logged and
// does not affect the return value.
func (w *Writer) Write(ctx context.Context, e Entry) error {
	if e.Action == "" {
		return fmt.Errorf("%w: action is empty", ErrInvalidEntry)
	}
	if e.Result != Success && e.Result != Failure {
		return fmt.Errorf("%w: result %q is neither %q nor %q", ErrInvalidEntry, e.Result, Success, Failure)
	}

	at := time.Now().UTC().Format(time.RFC3339Nano)

	detail, err := redactedDetail(e.Detail)
	if err != nil {
		return err
	}

	const insert = `INSERT INTO audit_log (at, actor, action, target, result, detail) VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := w.db.ExecContext(ctx, insert, at, e.Actor, e.Action, e.Target, string(e.Result), string(detail)); err != nil {
		return fmt.Errorf("audit: insert entry: %w", err)
	}

	w.mirror(mirrorLine{
		At:     at,
		Actor:  e.Actor,
		Action: e.Action,
		Target: e.Target,
		Result: e.Result,
		Detail: json.RawMessage(detail),
	})
	return nil
}

// mirrorLine is the JSON object written per line to the audit log file.
type mirrorLine struct {
	At     string          `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target string          `json:"target"`
	Result Result          `json:"result"`
	Detail json.RawMessage `json:"detail"`
}

func (w *Writer) mirror(line mirrorLine) {
	if err := w.appendMirror(line); err != nil {
		w.reportMirrorError(err)
		return
	}

	w.mu.Lock()
	w.mirrorBroken = false
	w.mu.Unlock()
}

func (w *Writer) appendMirror(line mirrorLine) error {
	// json.Marshal cannot emit a newline inside the object, so one line is one
	// entry and a reader can split on '\n' without parsing first.
	buf, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("audit: marshal mirror line: %w", err)
	}
	buf = append(buf, '\n')

	dir := filepath.Dir(w.mirrorPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("audit: create %s: %w", dir, err)
	}

	// Opened per write rather than held open: an external logrotate can move the
	// file under the panel without the writer appending to a deleted inode. The
	// lock keeps two goroutines from interleaving halves of a line.
	w.mu.Lock()
	defer w.mu.Unlock()

	f, err := os.OpenFile(w.mirrorPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", w.mirrorPath, err)
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return fmt.Errorf("audit: write %s: %w", w.mirrorPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("audit: close %s: %w", w.mirrorPath, err)
	}
	return nil
}

// reportMirrorError logs the first failure of a streak and nothing after it, so a
// permanently unwritable path costs one line rather than one per audited request.
// The next successful append starts a new streak.
func (w *Writer) reportMirrorError(err error) {
	w.mu.Lock()
	alreadyReported := w.mirrorBroken
	w.mirrorBroken = true
	w.mu.Unlock()

	if alreadyReported {
		return
	}
	w.logger.Error("audit: file mirror failed, entries are still in the database", "error", err, "path", w.mirrorPath)
}
