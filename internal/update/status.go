// Package update checks GitHub for a newer panel release and runs the upgrade
// as a detached unit.
//
// The upgrade cannot run inside the panel: it restarts the panel's own service
// as part of the swap, which would kill the process doing the work. So the
// panel starts `pbp upgrade` as a transient systemd unit, and the upgrade
// reports where it has got to in a status file. The panel reads that file to
// show progress, and it still can after the restart.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Phases an upgrade passes through, in order. The last four are terminal.
const (
	PhaseQueued      = "queued"
	PhaseChecking    = "checking"
	PhaseDownloading = "downloading"
	PhaseInstalling  = "installing"
	PhaseRestarting  = "restarting"
	PhaseHealth      = "health"
	PhaseDone        = "done"
	PhaseUpToDate    = "up_to_date"
	PhaseRolledBack  = "rolled_back"
	PhaseFailed      = "failed"

	// PhaseInterrupted is computed by the reader, never written: the upgrade
	// stopped reporting while it was still in progress, for example because the
	// host rebooted during it.
	PhaseInterrupted = "interrupted"
	// PhaseIdle is computed too: there is no status file, so nothing has run.
	PhaseIdle = "idle"
)

// maxLog bounds the output lines a status file keeps. The UI shows a tail.
const maxLog = 200

// progressInterval throttles progress writes: a download reports every chunk,
// and rewriting the file for each one would be most of the upgrade's I/O.
const progressInterval = 250 * time.Millisecond

// Status is one upgrade as the status file records it and the API returns it.
type Status struct {
	ID        string    `json:"id"`
	Tag       string    `json:"tag"`
	Phase     string    `json:"phase"`
	Message   string    `json:"message"`
	Done      int64     `json:"done"`
	Total     int64     `json:"total"`
	Error     string    `json:"error,omitempty"`
	Log       []string  `json:"log"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Terminal reports that the upgrade has finished, one way or another.
func (s Status) Terminal() bool {
	switch s.Phase {
	case PhaseDone, PhaseUpToDate, PhaseRolledBack, PhaseFailed, PhaseInterrupted, PhaseIdle:
		return true
	}
	return false
}

// Active reports that an upgrade is running and must not be started again.
func (s Status) Active() bool { return !s.Terminal() }

// Idle is the status when no upgrade has ever run here.
func Idle() Status { return Status{Phase: PhaseIdle, Log: []string{}} }

// StatusPath is the status file under the panel's own tree. The daemon hands it
// to the upgrade it starts, so both sides read and write the same file.
func StatusPath(panelDir string) string {
	return filepath.Join(panelDir, "update", "status.json")
}

// ReadStatus loads the status file. A missing file is the idle state, not an
// error: a host that has never upgraded has no file.
func ReadStatus(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Idle(), nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("read upgrade status: %w", err)
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("decode upgrade status: %w", err)
	}
	if s.Log == nil {
		s.Log = []string{}
	}
	return s, nil
}

// writeStatus replaces the file atomically, so a reader never sees half of it.
func writeStatus(path string, s Status) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Reporter writes an upgrade's progress to its status file. It runs inside the
// detached `pbp upgrade` process, which does one thing at a time, so it is not
// safe for concurrent use.
type Reporter struct {
	path      string
	now       func() time.Time
	st        Status
	lastWrite time.Time
}

// OpenReporter continues the status file at path: the job the panel queued
// keeps its id and start time. A missing file starts a fresh record.
func OpenReporter(path string) (*Reporter, error) {
	st, err := ReadStatus(path)
	if err != nil {
		return nil, err
	}
	if st.Phase == PhaseIdle {
		st = Status{StartedAt: time.Now().UTC(), Log: []string{}}
	}
	return &Reporter{path: path, now: func() time.Time { return time.Now().UTC() }, st: st}, nil
}

// Phase moves the upgrade into phase. The file is always written, because a
// phase change is what the UI is waiting for.
func (r *Reporter) Phase(phase, message string) error {
	r.st.Phase = phase
	r.st.Message = message
	r.st.Done, r.st.Total = 0, 0
	r.touch()
	return r.flush()
}

// Progress records how far a transfer has got. Writes are throttled, but the
// last report of a transfer is always written.
func (r *Reporter) Progress(done, total int64) {
	r.st.Done, r.st.Total = done, total
	if done < total && r.now().Sub(r.lastWrite) < progressInterval {
		return
	}
	r.touch()
	_ = r.flush()
}

// Log appends one line of output, keeping the last maxLog lines.
func (r *Reporter) Log(line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	r.st.Log = append(r.st.Log, line)
	if over := len(r.st.Log) - maxLog; over > 0 {
		r.st.Log = r.st.Log[over:]
	}
	r.touch()
	_ = r.flush()
}

// Finish records how the upgrade ended. A nil error completes it unless a
// phase already ended it (up_to_date, rolled_back). A non-nil error marks it
// failed unless a rollback already explained it.
func (r *Reporter) Finish(err error) error {
	switch {
	case err == nil:
		if !r.st.Terminal() {
			r.st.Phase = PhaseDone
		}
	case r.st.Phase == PhaseRolledBack:
		r.st.Error = err.Error()
	default:
		r.st.Phase = PhaseFailed
		r.st.Error = err.Error()
		r.st.Message = err.Error()
	}
	r.st.Done, r.st.Total = 0, 0
	r.touch()
	return r.flush()
}

// Status returns a copy of the record as it stands.
func (r *Reporter) Status() Status { return r.st }

// Writer returns an io.Writer that records each line written to it through Log.
func (r *Reporter) Writer() io.Writer { return lineWriter{r} }

func (r *Reporter) touch() {
	r.st.UpdatedAt = r.now()
	r.lastWrite = r.st.UpdatedAt
}

func (r *Reporter) flush() error { return writeStatus(r.path, r.st) }

type lineWriter struct{ r *Reporter }

func (w lineWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		w.r.Log(line)
	}
	return len(p), nil
}
