package cron

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// fakeClock is a clock the test moves by hand. After channels fire from Advance.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
	afters  []time.Duration
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.afters = append(c.afters, d)
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, &waiter{at: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves time forward and fires every waiter that is now due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

// afterCalls counts After calls whose duration satisfies pred.
func (c *fakeClock) afterCalls(pred func(time.Duration) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, d := range c.afters {
		if pred(d) {
			n++
		}
	}
	return n
}

// schedulerWaits counts how many times the scheduler loop has gone to sleep: its
// waits are at most a minute, while job timeouts are longer.
func (c *fakeClock) schedulerWaits() int {
	return c.afterCalls(func(d time.Duration) bool { return d <= time.Minute })
}

// timeoutWaits counts armed job timeouts.
func (c *fakeClock) timeoutWaits() int {
	return c.afterCalls(func(d time.Duration) bool { return d > time.Minute })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeExec records commands and answers them with handler.
type fakeExec struct {
	mu      sync.Mutex
	calls   []Command
	handler func(ctx context.Context, c Command) (int, error)
}

func (f *fakeExec) Run(ctx context.Context, c Command) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	h := f.handler
	f.mu.Unlock()
	if h == nil {
		return 0, nil
	}
	return h(ctx, c)
}

func (f *fakeExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeExec) call(i int) Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

type rig struct {
	svc        *Service
	clk        *fakeClock
	ex         *fakeExec
	db         *store.Store
	logs       string
	scripts    string
	allowShell *atomic.Bool
}

var testStart = time.Date(2026, 11, 17, 10, 0, 30, 0, time.UTC)

var testUsers = map[string]Account{
	"www":  {Name: "www", UID: 1001, GID: 1001, Groups: []uint32{1001, 33}, Home: "/home/www"},
	"root": {Name: "root", UID: 0, GID: 0, Home: "/root"},
}

func newRig(t *testing.T, mutate ...func(*Options)) *rig {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	r := &rig{
		clk: newFakeClock(testStart), ex: &fakeExec{}, db: db,
		logs: filepath.Join(t.TempDir(), "logs"), scripts: filepath.Join(t.TempDir(), "scripts"),
		allowShell: new(atomic.Bool),
	}
	opts := Options{
		Store: db, Clock: r.clk, Executor: r.ex,
		LookupUser: func(name string) (Account, error) {
			if a, ok := testUsers[name]; ok {
				return a, nil
			}
			return Account{}, errors.New("unknown user")
		},
		AllowShell: r.allowShell.Load,
		LogRoot:    r.logs, ScriptDir: r.scripts,
		Location: time.UTC,
	}
	for _, m := range mutate {
		m(&opts)
	}
	r.svc, err = New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(r.svc.Close)
	return r
}

func argvSpec(name string) JobSpec {
	return JobSpec{
		Name: name, Schedule: "* * * * *", Kind: KindArgv, User: "www",
		Argv: []string{"/bin/echo", "hi"},
	}
}

func (r *rig) create(t *testing.T, spec JobSpec) Job {
	t.Helper()
	j, err := r.svc.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create(%s): %v", spec.Name, err)
	}
	return j
}

func (r *rig) runs(t *testing.T, jobID int64) []Run {
	t.Helper()
	runs, err := r.svc.Runs(context.Background(), jobID, 0)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	return runs
}

func countStatus(runs []Run, st Status) int {
	n := 0
	for _, r := range runs {
		if r.Status == st {
			n++
		}
	}
	return n
}
