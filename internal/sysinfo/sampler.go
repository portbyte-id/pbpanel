package sysinfo

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultInterval is the sampling period when Options.Interval is zero.
	DefaultInterval = 5 * time.Second
	// DefaultCapacity is the ring size when Options.Capacity is zero: one hour at
	// the default interval.
	DefaultCapacity = 720

	// subscriberBuffer is how many snapshots a slow subscriber may fall behind by
	// before the oldest is dropped.
	subscriberBuffer = 4
)

// DiskStat is one mounted filesystem.
type DiskStat struct {
	Mount      string `json:"mount"`
	Device     string `json:"device"`
	FSType     string `json:"fstype"`
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	// UsedPercent is used / (used + available), the way df computes Use%.
	UsedPercent float64 `json:"used_percent"`
}

// NetRate is one interface's throughput since the previous sample.
type NetRate struct {
	Iface         string  `json:"iface"`
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
	RxBytes       uint64  `json:"rx_bytes"`
	TxBytes       uint64  `json:"tx_bytes"`
}

// Snapshot is the host at one instant. A Snapshot is never modified after the
// Sampler publishes it, so it can be shared between subscribers and the ring.
type Snapshot struct {
	Time time.Time `json:"time"`
	// Rates is false on the first sample after start: CPU percentages and network
	// rates are differences between two readings and are zero until there are two.
	Rates      bool      `json:"rates"`
	CPUPercent float64   `json:"cpu_percent"`
	CPUCores   []float64 `json:"cpu_cores"`
	Load       Load      `json:"load"`
	Memory     Memory    `json:"memory"`
	// UptimeSeconds is the host's time since boot.
	UptimeSeconds float64    `json:"uptime_seconds"`
	Disks         []DiskStat `json:"disks"`
	Net           []NetRate  `json:"net"`
}

// Options configure a Sampler. Every field has a usable zero value except Source.
type Options struct {
	Source Source
	// Interval is the time between samples. Zero means DefaultInterval.
	Interval time.Duration
	// Capacity is how many snapshots the ring keeps. Zero means DefaultCapacity.
	Capacity int
	// Now is the clock stamped on snapshots and used for rate arithmetic. Nil means
	// time.Now.
	Now func() time.Time
	// Tick returns the channel Run waits on and a stop function. Nil means a
	// time.Ticker. Tests inject a channel they fill by hand.
	Tick func(d time.Duration) (<-chan time.Time, func())
	// OnError is told about a sample that failed inside Run. Nil drops it.
	OnError func(error)
}

type reading struct {
	at    time.Time
	cpu   CPUTimes
	cores []CPUTimes
	net   map[string]NetCounters
}

// Sampler takes a Snapshot of the host on an interval and keeps the last few in a
// fixed-size ring. There is no time-series database: history is whatever the ring
// holds, and it is gone at restart.
type Sampler struct {
	src      Source
	interval time.Duration
	now      func() time.Time
	tick     func(time.Duration) (<-chan time.Time, func())
	onError  func(error)

	// sampleMu serialises Sample so two callers cannot interleave their deltas.
	sampleMu sync.Mutex
	prev     *reading

	mu   sync.Mutex // guards the ring and subs
	ring []Snapshot
	head int // index of the oldest element once the ring is full
	subs map[chan Snapshot]struct{}
}

// New builds a Sampler. It does not start sampling; call Run, or Sample by hand.
func New(o Options) (*Sampler, error) {
	if o.Source == nil {
		return nil, fmt.Errorf("sysinfo: Options.Source is required")
	}
	if o.Interval < 0 || o.Capacity < 0 {
		return nil, fmt.Errorf("sysinfo: Interval and Capacity must not be negative")
	}
	s := &Sampler{
		src: o.Source, interval: o.Interval, now: o.Now, tick: o.Tick, onError: o.OnError,
		subs: map[chan Snapshot]struct{}{},
	}
	if s.interval == 0 {
		s.interval = DefaultInterval
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.tick == nil {
		s.tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	capacity := o.Capacity
	if capacity == 0 {
		capacity = DefaultCapacity
	}
	s.ring = make([]Snapshot, 0, capacity)
	return s, nil
}

// Interval is the sampling period.
func (s *Sampler) Interval() time.Duration { return s.interval }

// Run samples once at once, then on every tick, until ctx is done. A failed
// sample is reported through Options.OnError and does not stop the loop.
func (s *Sampler) Run(ctx context.Context) {
	ticks, stop := s.tick(s.interval)
	defer stop()
	for {
		if _, err := s.Sample(ctx); err != nil && s.onError != nil {
			s.onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// Sample reads the host once, stores the result in the ring and hands it to the
// subscribers. A failed read stores and publishes nothing.
func (s *Sampler) Sample(ctx context.Context) (Snapshot, error) {
	s.sampleMu.Lock()
	defer s.sampleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	snap, cur, err := s.read()
	if err != nil {
		return Snapshot{}, err
	}
	if p := s.prev; p != nil {
		snap.Rates = true
		snap.CPUPercent = CPUPercent(p.cpu, cur.cpu)
		for i, c := range cur.cores {
			if i < len(p.cores) {
				snap.CPUCores[i] = CPUPercent(p.cores[i], c)
			}
		}
		dt := cur.at.Sub(p.at).Seconds()
		for i := range snap.Net {
			n := &snap.Net[i]
			old, ok := p.net[n.Iface]
			if !ok || dt <= 0 {
				continue
			}
			n.RxBytesPerSec = rate(old.RxBytes, n.RxBytes, dt)
			n.TxBytesPerSec = rate(old.TxBytes, n.TxBytes, dt)
		}
	}
	s.prev = &cur
	s.publish(snap)
	return snap, nil
}

// rate is bytes per second between two counter readings. A counter that went
// backwards (interface reset, 32-bit wrap) gives 0 rather than a huge number.
func rate(prev, cur uint64, seconds float64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur-prev) / seconds
}

func (s *Sampler) read() (Snapshot, reading, error) {
	proc := func(name string) ([]byte, error) {
		b, err := s.src.ReadProc(name)
		if err != nil {
			return nil, fmt.Errorf("sysinfo: read /proc/%s: %w", name, err)
		}
		return b, nil
	}
	now := s.now()
	snap := Snapshot{Time: now}
	cur := reading{at: now}

	b, err := proc("stat")
	if err != nil {
		return snap, cur, err
	}
	var cpu CPUTimes
	var cores []CPUTimes
	if cpu, cores, err = ParseCPUStat(b); err != nil {
		return snap, cur, err
	}
	cur.cpu, cur.cores = cpu, cores
	snap.CPUCores = make([]float64, len(cores))

	if b, err = proc("loadavg"); err != nil {
		return snap, cur, err
	}
	if snap.Load, err = ParseLoadavg(b); err != nil {
		return snap, cur, err
	}

	if b, err = proc("meminfo"); err != nil {
		return snap, cur, err
	}
	if snap.Memory, err = ParseMeminfo(b); err != nil {
		return snap, cur, err
	}

	if b, err = proc("uptime"); err != nil {
		return snap, cur, err
	}
	up, err := ParseUptime(b)
	if err != nil {
		return snap, cur, err
	}
	snap.UptimeSeconds = up.Seconds()

	if b, err = proc("net/dev"); err != nil {
		return snap, cur, err
	}
	nets, err := ParseNetDev(b)
	if err != nil {
		return snap, cur, err
	}
	cur.net = make(map[string]NetCounters, len(nets))
	snap.Net = []NetRate{}
	for _, n := range nets {
		if n.Iface == "lo" {
			continue
		}
		cur.net[n.Iface] = n
		snap.Net = append(snap.Net, NetRate{Iface: n.Iface, RxBytes: n.RxBytes, TxBytes: n.TxBytes})
	}

	if b, err = proc("mounts"); err != nil {
		return snap, cur, err
	}
	mounts, err := ParseMounts(b)
	if err != nil {
		return snap, cur, err
	}
	snap.Disks = []DiskStat{}
	for _, m := range DiskMounts(mounts) {
		u, err := s.src.Disk(m.Point)
		if err != nil || u.TotalBytes == 0 {
			continue // a mount that cannot be read is left out, not a failed sample
		}
		d := DiskStat{
			Mount: m.Point, Device: m.Device, FSType: m.FSType,
			TotalBytes: u.TotalBytes, UsedBytes: u.UsedBytes, FreeBytes: u.FreeBytes,
		}
		if denom := u.UsedBytes + u.FreeBytes; denom > 0 {
			d.UsedPercent = clampPercent(float64(u.UsedBytes) / float64(denom) * 100)
		}
		snap.Disks = append(snap.Disks, d)
	}
	return snap, cur, nil
}

// publish appends to the ring and fans out to subscribers without ever blocking:
// a subscriber whose buffer is full loses its oldest snapshot, so it sees the
// newest ones and a stalled SSE client cannot hold up the sampler.
func (s *Sampler) publish(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ring) < cap(s.ring) {
		s.ring = append(s.ring, snap)
	} else {
		s.ring[s.head] = snap
		s.head = (s.head + 1) % len(s.ring)
	}
	for ch := range s.subs {
		select {
		case ch <- snap:
			continue
		default:
		}
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- snap:
		default:
		}
	}
}

// History returns the snapshots in the ring, oldest first. The slice is a copy.
func (s *Sampler) History() []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Snapshot, 0, len(s.ring))
	out = append(out, s.ring[s.head:]...)
	out = append(out, s.ring[:s.head]...)
	return out
}

// Latest returns the newest snapshot, and false before the first sample.
func (s *Sampler) Latest() (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ring) == 0 {
		return Snapshot{}, false
	}
	return s.ring[(s.head+len(s.ring)-1)%len(s.ring)], true
}

// Subscribe returns a channel that receives every snapshot taken from now on, and
// a cancel function that must be called when the subscriber is done (an SSE
// handler defers it). The channel is closed by cancel. A subscriber that reads too
// slowly misses the oldest snapshots instead of blocking the sampler.
func (s *Sampler) Subscribe() (<-chan Snapshot, func()) {
	ch := make(chan Snapshot, subscriberBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, ch)
			s.mu.Unlock()
			close(ch)
		})
	}
}
