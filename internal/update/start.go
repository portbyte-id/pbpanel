package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"
)

// ErrRunning means an upgrade is still in progress; a second one would race it
// for the binary and the service.
var ErrRunning = errors.New("an upgrade is already running")

// ErrInvalidTag refuses a tag that is not a plain release name. The tag is
// passed as an argument, never through a shell, but it is also written into
// the status file and a unit description, so it is kept to the characters a
// release tag uses.
var ErrInvalidTag = errors.New("the release tag is not valid")

// staleAfter is how long an in-progress upgrade may go without writing before
// the reader calls it interrupted. The longest silent stretch is the health
// window, which is 30 s, so five minutes is far past any healthy upgrade.
const staleAfter = 5 * time.Minute

var tagPattern = regexp.MustCompile(`^(latest|v?[0-9A-Za-z][0-9A-Za-z._-]{0,62})$`)

// runner is the one command the starter needs. osdep.Runner satisfies it.
type runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Starter queues an upgrade as a transient systemd unit and reads its status.
type Starter struct {
	// BinaryPath is the installed panel binary, the one the unit runs.
	BinaryPath string
	// StatusFile is where the detached upgrade writes its progress.
	StatusFile string
	// Runner runs systemd-run.
	Runner runner
	// Env supplies the variables the detached upgrade inherits: PB_REPO and the
	// GitHub token. Nil means os.LookupEnv.
	Env func(key string) (string, bool)
	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	// mu makes the running check and the queueing one step, so two requests
	// that arrive together cannot both start an upgrade.
	mu sync.Mutex
}

// Status returns the current upgrade. An upgrade that stopped writing while it
// was still in progress is reported as interrupted, not as running forever.
func (s *Starter) Status() (Status, error) {
	st, err := ReadStatus(s.StatusFile)
	if err != nil {
		return Status{}, err
	}
	if st.Active() && s.now().Sub(st.UpdatedAt) > staleAfter {
		st.Phase = PhaseInterrupted
		st.Message = "the upgrade stopped reporting; check the host journal for pbp-upgrade-" + st.ID
	}
	return st, nil
}

// Start queues `pbp upgrade` for tag as its own unit and returns the queued
// status. The unit outlives the panel's restart, which is the point.
func (s *Starter) Start(ctx context.Context, tag string) (Status, error) {
	if !tagPattern.MatchString(tag) {
		return Status{}, ErrInvalidTag
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.Status()
	if err != nil {
		return Status{}, err
	}
	if cur.Active() {
		return Status{}, ErrRunning
	}

	id, err := newID()
	if err != nil {
		return Status{}, err
	}
	path := s.StatusFile
	queued := Status{ID: id, Tag: tag, Phase: PhaseQueued, Message: "waiting for the upgrade unit to start",
		StartedAt: s.now(), UpdatedAt: s.now(), Log: []string{}}
	if err := writeStatus(path, queued); err != nil {
		return Status{}, fmt.Errorf("write upgrade status: %w", err)
	}

	bin := s.BinaryPath
	unit := "pbp-upgrade-" + id
	args := []string{
		"--unit=" + unit,
		"--collect",
		"--description=Server Panel upgrade to " + tag,
	}
	for _, key := range []string{"PB_REPO", "PB_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if v, ok := s.lookupEnv(key); ok && v != "" {
			args = append(args, "--setenv="+key+"="+v)
		}
	}
	args = append(args, "--", bin, "upgrade", "--status-file", path, "--version", tag)

	if err := s.Runner.Run(ctx, "systemd-run", args...); err != nil {
		failed := queued
		failed.Phase = PhaseFailed
		failed.Error = "systemd-run could not start the upgrade: " + err.Error()
		failed.Message = failed.Error
		failed.UpdatedAt = s.now()
		_ = writeStatus(path, failed)
		return failed, fmt.Errorf("start upgrade unit: %w", err)
	}
	return queued, nil
}

func (s *Starter) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Starter) lookupEnv(key string) (string, bool) {
	if s.Env != nil {
		return s.Env(key)
	}
	return os.LookupEnv(key)
}

func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("upgrade id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
