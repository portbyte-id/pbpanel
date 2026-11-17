package sysinfo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource serves canned /proc text and records nothing else. Tests replace
// entries to move the "host" forward between samples.
type fakeSource struct {
	mu    sync.Mutex
	proc  map[string]string
	disks map[string]DiskUsage
}

func newFakeSource(t *testing.T) *fakeSource {
	t.Helper()
	f := &fakeSource{proc: map[string]string{}, disks: map[string]DiskUsage{
		"/":              {TotalBytes: 100 << 30, UsedBytes: 25 << 30, FreeBytes: 75 << 30},
		"/mnt/data disk": {TotalBytes: 0}, // reports nothing: left out
	}}
	for name, file := range map[string]string{
		"stat": "stat", "loadavg": "loadavg", "meminfo": "meminfo", "uptime": "uptime",
		"net/dev": "net_dev", "mounts": "mounts",
	} {
		f.proc[name] = string(fixture(t, file))
	}
	return f
}

func (f *fakeSource) set(name, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proc[name] = text
}

func (f *fakeSource) ReadProc(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.proc[name]
	if !ok {
		return nil, fmt.Errorf("no such file %s", name)
	}
	return []byte(s), nil
}

func (f *fakeSource) Disk(m string) (DiskUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.disks[m]; ok {
		return u, nil
	}
	return DiskUsage{}, errors.New("statfs failed")
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newSampler(t *testing.T, src Source, capacity int) (*Sampler, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	s, err := New(Options{Source: src, Capacity: capacity, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func TestSampleFirstThenRates(t *testing.T) {
	src := newFakeSource(t)
	s, clk := newSampler(t, src, 10)
	ctx := context.Background()

	first, err := s.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Rates || first.CPUPercent != 0 {
		t.Fatalf("first sample claims rates: %+v", first)
	}
	if first.Load.One != 0.52 || first.Memory.UsedBytes != 4000000*1024 || first.UptimeSeconds != 350735.47 {
		t.Fatalf("static fields: %+v", first)
	}
	if len(first.Net) != 2 || first.Net[0].Iface != "eth0" || first.Net[1].Iface != "docker0" {
		t.Fatalf("lo should be left out: %+v", first.Net)
	}
	// "/" reads fine; "/mnt/data disk" reports zero bytes and "/boot/efi" fails statfs,
	// so both are left out instead of failing the sample.
	if len(first.Disks) != 1 || first.Disks[0].Mount != "/" || !near(first.Disks[0].UsedPercent, 25) {
		t.Fatalf("disks: %+v", first.Disks)
	}

	// 2s later: CPU gained 1000 ticks of which 400 idle; eth0 moved 2000/1000 bytes.
	clk.advance(2 * time.Second)
	src.set("stat", "cpu  5305 150 3120 80400 200 0 45 10 0 0\ncpu0 2700 70 1500 40200 100 0 40 5 0 0\ncpu1 2605 80 1620 40200 100 0 5 5 0 0\n")
	src.set("net/dev", "h\nh\n  eth0: 1002000 900 2 0 0 0 0 0 501000 700 1 0 0 0 0 0\ndocker0: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n")
	second, err := s.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Rates || !near(second.CPUPercent, 60) {
		t.Fatalf("cpu = %v rates = %v", second.CPUPercent, second.Rates)
	}
	if len(second.CPUCores) != 2 || !near(second.CPUCores[0], 60) {
		t.Fatalf("cores = %v", second.CPUCores)
	}
	eth := second.Net[0]
	if !near(eth.RxBytesPerSec, 1000) || !near(eth.TxBytesPerSec, 500) {
		t.Fatalf("eth0 rates = %+v", eth)
	}
	// docker0's counter went backwards: 0, not a wrapped huge number.
	if d := second.Net[1]; d.RxBytesPerSec != 0 || d.TxBytesPerSec != 0 {
		t.Fatalf("docker0 rates = %+v", d)
	}
}

func TestSampleFailureStoresNothing(t *testing.T) {
	src := newFakeSource(t)
	s, _ := newSampler(t, src, 3)
	src.set("meminfo", "garbage")
	if _, err := s.Sample(context.Background()); err == nil || !strings.Contains(err.Error(), "MemTotal") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := s.Latest(); ok || len(s.History()) != 0 {
		t.Fatal("failed sample was stored")
	}

	delete(src.proc, "uptime")
	src.set("meminfo", string(fixture(t, "meminfo")))
	if _, err := s.Sample(context.Background()); err == nil || !strings.Contains(err.Error(), "/proc/uptime") {
		t.Fatalf("missing file err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Sample(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: %v", err)
	}
}

func TestRingKeepsNewestInOrder(t *testing.T) {
	src := newFakeSource(t)
	s, clk := newSampler(t, src, 3)
	if _, ok := s.Latest(); ok {
		t.Fatal("Latest before any sample")
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Sample(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.advance(time.Second)
		if h := s.History(); len(h) > 3 {
			t.Fatalf("ring grew to %d", len(h))
		}
	}
	h := s.History()
	if len(h) != 3 {
		t.Fatalf("history = %d", len(h))
	}
	for i := 1; i < len(h); i++ {
		if !h[i].Time.After(h[i-1].Time) {
			t.Fatalf("not oldest first: %v", h)
		}
	}
	last, _ := s.Latest()
	if !last.Time.Equal(h[2].Time) || !h[0].Time.Equal(clk.t.Add(-3*time.Second)) {
		t.Fatalf("latest %v, oldest %v, now %v", last.Time, h[0].Time, clk.t)
	}
	// History is a copy.
	h[0] = Snapshot{}
	if s.History()[0].Time.IsZero() {
		t.Fatal("History aliases the ring")
	}
}

func TestSubscribeDeliversAndNeverBlocks(t *testing.T) {
	src := newFakeSource(t)
	s, _ := newSampler(t, src, 10)
	ctx := context.Background()

	fast, cancelFast := s.Subscribe()
	slow, cancelSlow := s.Subscribe()
	defer cancelSlow()

	if _, err := s.Sample(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fast:
	default:
		t.Fatal("subscriber missed a snapshot")
	}

	// A subscriber that never reads must not hold up sampling, and must end up
	// with the newest snapshots.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			if _, err := s.Sample(ctx); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled subscriber blocked the sampler")
	}
	if got := len(slow); got != subscriberBuffer {
		t.Fatalf("slow buffer holds %d, want %d", got, subscriberBuffer)
	}
	var lastSeen Snapshot
	for len(slow) > 0 {
		lastSeen = <-slow
	}
	latest, _ := s.Latest()
	if !lastSeen.Time.Equal(latest.Time) {
		t.Fatal("slow subscriber lost the newest snapshot")
	}

	cancelFast()
	cancelFast() // idempotent
	if _, ok := <-fast; ok {
		for range fast { // drain what was buffered
		}
	}
	if _, err := s.Sample(ctx); err != nil { // publishing after cancel must not panic
		t.Fatal(err)
	}
}

func TestRunSamplesOnTicks(t *testing.T) {
	src := newFakeSource(t)
	ticks := make(chan time.Time)
	var stopped bool
	var gotInterval time.Duration
	errCh := make(chan error, 4)
	s, err := New(Options{
		Source: src, Interval: 7 * time.Second, Capacity: 10,
		Tick: func(d time.Duration) (<-chan time.Time, func()) {
			gotInterval = d
			return ticks, func() { stopped = true }
		},
		OnError: func(err error) { errCh <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, cancel := s.Subscribe()
	defer cancel()

	ctx, stop := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { s.Run(ctx); close(finished) }()

	wait := func(label string) {
		t.Helper()
		select {
		case <-sub:
		case <-time.After(5 * time.Second):
			t.Fatalf("no snapshot: %s", label)
		}
	}
	wait("initial")
	ticks <- time.Time{}
	wait("tick 1")
	src.set("loadavg", "bad") // a failing sample is reported, the loop goes on
	ticks <- time.Time{}
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("failed sample not reported")
	}
	src.set("loadavg", string(fixture(t, "loadavg")))
	ticks <- time.Time{}
	wait("tick 3")
	stop()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored cancellation")
	}
	if !stopped || gotInterval != 7*time.Second {
		t.Fatalf("stopped=%v interval=%v", stopped, gotInterval)
	}
	if len(errCh) != 0 {
		t.Fatalf("unexpected extra errors: %d", len(errCh))
	}
	if got := len(s.History()); got != 3 {
		t.Fatalf("history = %d", got)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("nil Source accepted")
	}
	if _, err := New(Options{Source: newFakeSource(t), Interval: -1}); err == nil {
		t.Fatal("negative interval accepted")
	}
	s, err := New(Options{Source: newFakeSource(t)})
	if err != nil || s.Interval() != DefaultInterval || cap(s.ring) != DefaultCapacity {
		t.Fatalf("defaults: %v %v", s, err)
	}
}
