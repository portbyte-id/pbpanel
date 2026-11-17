package cron

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Kind is how a job's command is described.
type Kind string

const (
	// KindArgv runs Argv directly: Argv[0] is an absolute path, no shell involved.
	KindArgv Kind = "argv"
	// KindScript runs an existing executable file, ScriptPath, with no arguments.
	KindScript Kind = "script"
	// KindShell writes ScriptBody to a file under the panel data directory and
	// runs `/bin/sh <file>`. Shell jobs are refused unless the caller allows them.
	KindShell Kind = "shell"
)

// Overlap says what happens when a job fires while its previous run is still going.
type Overlap string

const (
	// OverlapSkip drops the new firing and records a "skipped" run.
	OverlapSkip Overlap = "skip"
	// OverlapQueue holds one pending firing, which starts when the running one
	// ends. A firing that arrives while one is already pending is skipped.
	OverlapQueue Overlap = "queue"
	// OverlapAllow starts the new run alongside the running one.
	OverlapAllow Overlap = "allow"
)

// Status is how a run ended, or "running" while it has not.
type Status string

const (
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusTimeout     Status = "timeout"
	StatusCanceled    Status = "canceled"
	StatusInterrupted Status = "interrupted"
	StatusSkipped     Status = "skipped"
)

// Source says what started a run.
type Source string

const (
	SourceSchedule Source = "schedule"
	SourceManual   Source = "manual"
)

// Limits on job definitions.
const (
	MinTimeout     = 1 * time.Second
	MaxTimeout     = 24 * time.Hour
	DefaultTimeout = time.Hour

	DefaultRetention = 50
	MaxRetention     = 1000

	MaxNameLen    = 64
	MaxArgvItems  = 256
	MaxArgLen     = 4096
	MaxScriptBody = 64 << 10
)

// Errors the API layer maps to statuses.
var (
	ErrNotFound         = errors.New("cron: job not found")
	ErrDuplicate        = errors.New("cron: a job with that name already exists")
	ErrInvalid          = errors.New("cron: invalid job")
	ErrShellNotAllowed  = errors.New("cron: shell jobs are not enabled")
	ErrRunNotFound      = errors.New("cron: run not found")
	errShellScriptDirNA = errors.New("cron: no script directory is configured for shell jobs")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Job is a stored job definition.
type Job struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Schedule   string   `json:"schedule"`
	Kind       Kind     `json:"kind"`
	User       string   `json:"user"`
	Argv       []string `json:"argv,omitempty"`
	ScriptPath string   `json:"script_path,omitempty"`
	ScriptBody string   `json:"script_body,omitempty"`
	Workdir    string   `json:"workdir,omitempty"`
	// TimeoutSeconds is the wall-clock limit of one run.
	TimeoutSeconds int       `json:"timeout_seconds"`
	Overlap        Overlap   `json:"overlap"`
	Retention      int       `json:"retention"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Next returns the job's next firing after t.
func (j Job) Next(after time.Time) (time.Time, bool) {
	s, err := ParseSchedule(j.Schedule)
	if err != nil {
		return time.Time{}, false
	}
	return s.Next(after)
}

// JobSpec is what a caller supplies to create or update a job. On update the
// name cannot change (log directories and script files are keyed by it); it may
// be left empty or repeated.
type JobSpec struct {
	Name           string
	Schedule       string
	Kind           Kind
	User           string
	Argv           []string
	ScriptPath     string
	ScriptBody     string
	Workdir        string
	TimeoutSeconds int     // 0 means DefaultTimeout
	Overlap        Overlap // empty means OverlapSkip
	Retention      int     // 0 means the service default
	Enabled        *bool   // nil: enabled on create, unchanged on update
}

// Run is one execution of a job, or a firing that was skipped.
type Run struct {
	ID         int64      `json:"id"`
	JobID      int64      `json:"job_id"`
	Source     Source     `json:"source"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   int        `json:"exit_code"`
	Status     Status     `json:"status"`
	LogPath    string     `json:"log_path,omitempty"`
	// OutputTail is the last bytes of the combined output; the full output is in
	// LogPath, up to the log size cap.
	OutputTail string `json:"output_tail"`
	Truncated  bool   `json:"truncated"`
	Error      string `json:"error,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validName(name string) error {
	if !nameRe.MatchString(name) {
		return invalid("name %q must be 1-%d characters of a-z, 0-9, '.', '_' or '-', starting with a letter or digit", name, MaxNameLen)
	}
	if strings.Contains(name, "..") {
		return invalid("name %q must not contain '..'", name)
	}
	return nil
}

func checkPath(what, p string) error {
	if !path.IsAbs(p) {
		return invalid("%s %q must be an absolute path", what, p)
	}
	if strings.ContainsRune(p, 0) {
		return invalid("%s contains a NUL byte", what)
	}
	if path.Clean(p) != p {
		return invalid("%s %q must be clean (no '..', '.', '//' or trailing '/')", what, p)
	}
	return nil
}

// normalize validates a spec and fills its defaults. It does not look the user up.
func normalize(spec JobSpec, defaultRetention int) (JobSpec, error) {
	if err := validName(spec.Name); err != nil {
		return spec, err
	}
	if _, err := ParseSchedule(spec.Schedule); err != nil {
		return spec, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	spec.Schedule = strings.TrimSpace(spec.Schedule)
	if spec.User == "" {
		return spec, invalid("user is required")
	}

	switch spec.Kind {
	case KindArgv:
		if len(spec.Argv) == 0 {
			return spec, invalid("argv is required for an argv job")
		}
		if len(spec.Argv) > MaxArgvItems {
			return spec, invalid("argv has %d items, at most %d", len(spec.Argv), MaxArgvItems)
		}
		if err := checkPath("argv[0]", spec.Argv[0]); err != nil {
			return spec, err
		}
		for i, a := range spec.Argv {
			if len(a) > MaxArgLen || strings.ContainsRune(a, 0) {
				return spec, invalid("argv[%d] is too long or contains a NUL byte", i)
			}
		}
		if spec.ScriptPath != "" || spec.ScriptBody != "" {
			return spec, invalid("an argv job takes neither script_path nor script_body")
		}
	case KindScript:
		if err := checkPath("script_path", spec.ScriptPath); err != nil {
			return spec, err
		}
		if len(spec.Argv) != 0 || spec.ScriptBody != "" {
			return spec, invalid("a script job takes neither argv nor script_body")
		}
	case KindShell:
		if strings.TrimSpace(spec.ScriptBody) == "" {
			return spec, invalid("script_body is required for a shell job")
		}
		if len(spec.ScriptBody) > MaxScriptBody {
			return spec, invalid("script_body is %d bytes, at most %d", len(spec.ScriptBody), MaxScriptBody)
		}
		if strings.ContainsRune(spec.ScriptBody, 0) {
			return spec, invalid("script_body contains a NUL byte")
		}
		if len(spec.Argv) != 0 || spec.ScriptPath != "" {
			return spec, invalid("a shell job takes neither argv nor script_path")
		}
	default:
		return spec, invalid("kind %q must be argv, script or shell", spec.Kind)
	}

	if spec.Workdir != "" {
		if err := checkPath("workdir", spec.Workdir); err != nil {
			return spec, err
		}
	}

	if spec.TimeoutSeconds == 0 {
		spec.TimeoutSeconds = int(DefaultTimeout / time.Second)
	}
	if d := time.Duration(spec.TimeoutSeconds) * time.Second; d < MinTimeout || d > MaxTimeout {
		return spec, invalid("timeout_seconds %d must be between %d and %d", spec.TimeoutSeconds, int(MinTimeout/time.Second), int(MaxTimeout/time.Second))
	}

	switch spec.Overlap {
	case "":
		spec.Overlap = OverlapSkip
	case OverlapSkip, OverlapQueue, OverlapAllow:
	default:
		return spec, invalid("overlap %q must be skip, queue or allow", spec.Overlap)
	}

	if spec.Retention == 0 {
		spec.Retention = defaultRetention
	}
	if spec.Retention < 1 || spec.Retention > MaxRetention {
		return spec, invalid("retention %d must be between 1 and %d", spec.Retention, MaxRetention)
	}
	return spec, nil
}
