package cron

import (
	"context"
	"io"
	"time"
)

// Account is the identity a job runs as.
type Account struct {
	Name   string
	UID    uint32
	GID    uint32
	Groups []uint32
	Home   string
}

// Command is a fully resolved process to start. There is no shell string: Args is
// the argv, Args[0] included, and Path is the file to execute.
type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
	User Account
	// Output receives stdout and stderr interleaved.
	Output io.Writer
}

// Executor starts a Command as its User and waits for it. The returned error is
// non-nil only when the process could not be started or waited on; a process that
// exits non-zero returns its code and a nil error. When ctx ends the executor must
// kill the process (and its children) and return.
type Executor interface {
	Run(ctx context.Context, c Command) (exitCode int, err error)
}

// Clock is the scheduler's source of time.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RealClock is the wall clock.
var RealClock Clock = realClock{}
