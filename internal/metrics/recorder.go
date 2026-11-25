package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/sysinfo"
)

const (
	// HostStep is the raw host and disk tier: the mean of two 5 s samples.
	HostStep = 10
	// ServiceStep is the raw service tier.
	ServiceStep = 30

	flushEvery = 60 * time.Second
	sweepEvery = time.Hour
	// rollupGrace keeps the newest minute out of a rollup until late samples
	// (a service poll that straddled the boundary) have had time to land.
	rollupGrace = 15 * time.Second
	// errorLogEvery rate-limits repeated flush failures (disk full).
	errorLogEvery = 10 * time.Minute
)

// Service states as stored. Larger is worse, so a rollup keeps the maximum.
const (
	StateRunning  = 1
	StateStarting = 2
	StateStopping = 3
	StateStopped  = 4
	StateFailed   = 5
	StateMissing  = 6
	StateUnknown  = 7
)

// StateCode converts a sysinfo service state name to its stored integer.
func StateCode(state string) int {
	switch state {
	case "running":
		return StateRunning
	case "starting":
		return StateStarting
	case "stopping":
		return StateStopping
	case "stopped":
		return StateStopped
	case "failed":
		return StateFailed
	case "missing":
		return StateMissing
	}
	return StateUnknown
}

// StateName is the reverse of StateCode.
func StateName(code int) string {
	for _, n := range []string{"running", "starting", "stopping", "stopped", "failed", "missing"} {
		if StateCode(n) == code {
			return n
		}
	}
	return "unknown"
}

// ServiceSample is one service at one instant.
type ServiceSample struct {
	Name  string
	Kind  string
	State int
	// CPU is percent of one core; RSS is bytes. They are meaningful only when the
	// matching OK flag is set (a stopped unit, or a host without cgroup v2, has none).
	CPU   float64
	CPUOK bool
	RSS   int64
	RSSOK bool
}

// SnapshotSource is the sampler as the recorder sees it.
type SnapshotSource interface {
	Subscribe() (<-chan sysinfo.Snapshot, func())
}

// ServiceSource reports the usage of every service on demand.
type ServiceSource interface {
	Usage(ctx context.Context) []ServiceSample
}

// Retention is how long each tier is kept.
type Retention struct {
	Raw, Minute, Coarse time.Duration
}

// DefaultRetention is 24 h of raw samples, 7 d of minutes, 90 d of coarse rows.
func DefaultRetention() Retention {
	return Retention{Raw: 24 * time.Hour, Minute: 7 * 24 * time.Hour, Coarse: 90 * 24 * time.Hour}
}

// Options configure a Recorder. DB is required.
type Options struct {
	DB        *DB
	Snapshots SnapshotSource // nil: no host or disk history
	Services  ServiceSource  // nil: no service history
	Sites     SiteSource     // nil: no site history
	Retention Retention      // zero value means DefaultRetention
	Disabled  bool           // start with persistence off
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Tick returns the channel Run flushes on, and its stop function. Nil means a
	// 60 s ticker. Tests drive Flush by hand instead.
	Tick func(d time.Duration) (<-chan time.Time, func())
	// OnError hears about a failed flush or sweep, rate-limited. Nil drops it.
	OnError func(error)
}

type hostAgg struct {
	n                                        int
	cpu, cpuMax, load1, load1Max, load5, l15 float64
	memUsed, memAvail, swap, rx, tx          float64
	memAvailMin                              uint64
}

func (a *hostAgg) add(s sysinfo.Snapshot) {
	a.n++
	a.cpu += s.CPUPercent
	a.cpuMax = max(a.cpuMax, s.CPUPercent)
	a.load1 += s.Load.One
	a.load1Max = max(a.load1Max, s.Load.One)
	a.load5 += s.Load.Five
	a.l15 += s.Load.Fifteen
	a.memUsed += float64(s.Memory.UsedBytes)
	a.memAvail += float64(s.Memory.AvailableBytes)
	if a.n == 1 || s.Memory.AvailableBytes < a.memAvailMin {
		a.memAvailMin = s.Memory.AvailableBytes
	}
	a.swap += float64(s.Memory.SwapUsedBytes)
	for _, n := range s.Net {
		a.rx += n.RxBytesPerSec
		a.tx += n.TxBytesPerSec
	}
}

type diskAgg struct {
	n          int
	pct, pctMx float64
	used       float64
	ioN        int
	rd, wr     float64
}

func (a *diskAgg) add(d sysinfo.DiskStat) {
	a.n++
	a.pct += d.UsedPercent
	a.pctMx = max(a.pctMx, d.UsedPercent)
	a.used += float64(d.UsedBytes)
	if d.IO {
		a.ioN++
		a.rd += d.ReadBytesPerSec
		a.wr += d.WriteBytesPerSec
	}
}

type svcRow struct {
	ts int64
	ServiceSample
}

// Status is what the status route reports about the recorder.
type Status struct {
	Enabled   bool      `json:"enabled"`
	LastFlush time.Time `json:"last_flush"`
	LastError string    `json:"last_error"`
	// LastErrorAt is when LastError happened; zero when there has been none.
	LastErrorAt time.Time `json:"last_error_at"`
	// Dropped counts rows discarded because a flush failed.
	Dropped int `json:"dropped"`
}

// Recorder turns the live sampler, the service poll and the site minutes into
// rows of metrics.db: one transaction a minute, with the rollups in the same
// transaction.
type Recorder struct {
	o   Options
	now func() time.Time

	mu       sync.Mutex
	enabled  bool
	sitesOff bool
	ret      Retention
	host     map[int64]*hostAgg
	disks    map[int64]map[string]*diskAgg
	svcs     []svcRow
	latest   *sysinfo.Snapshot
	status   Status
	lastSwp  time.Time
	lastErrL time.Time
}

// NewRecorder builds a Recorder. It does nothing until Run, or Observe and Flush
// by hand.
func NewRecorder(o Options) (*Recorder, error) {
	if o.DB == nil {
		return nil, fmt.Errorf("metrics: Options.DB is required")
	}
	r := &Recorder{
		o: o, now: o.Now, enabled: !o.Disabled, ret: o.Retention,
		host: map[int64]*hostAgg{}, disks: map[int64]map[string]*diskAgg{},
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.ret == (Retention{}) {
		r.ret = DefaultRetention()
	}
	return r, nil
}

// SetEnabled turns persistence on or off live. Turning it off discards what is
// pending and keeps the database.
func (r *Recorder) SetEnabled(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = on
	if !on {
		r.host = map[int64]*hostAgg{}
		r.disks = map[int64]map[string]*diskAgg{}
		r.svcs = nil
	}
}

// SetRetention changes the tiers' lifetimes; the next sweep applies it.
func (r *Recorder) SetRetention(ret Retention) {
	r.mu.Lock()
	r.ret = ret
	r.mu.Unlock()
}

// Status reports the recorder's health.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	s.Enabled = r.enabled
	return s
}

// Latest is the newest host snapshot seen, kept even when a flush failed so a
// full disk can still be alerted on from memory.
func (r *Recorder) Latest() (sysinfo.Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latest == nil {
		return sysinfo.Snapshot{}, false
	}
	return *r.latest, true
}

// Observe folds one sampler snapshot into its 10 s bucket. The first snapshot
// after a restart has no rates and is skipped.
func (r *Recorder) Observe(s sysinfo.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := s
	r.latest = &snap
	if !r.enabled || !s.Rates {
		return
	}
	b := s.Time.Unix() / HostStep * HostStep
	a := r.host[b]
	if a == nil {
		a = &hostAgg{}
		r.host[b] = a
	}
	a.add(s)
	dm := r.disks[b]
	if dm == nil {
		dm = map[string]*diskAgg{}
		r.disks[b] = dm
	}
	for _, d := range s.Disks {
		da := dm[d.Mount]
		if da == nil {
			da = &diskAgg{}
			dm[d.Mount] = da
		}
		da.add(d)
	}
}

// ObserveServices records one poll of the services at time at.
func (r *Recorder) ObserveServices(at time.Time, samples []ServiceSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled {
		return
	}
	ts := at.Unix() / ServiceStep * ServiceStep
	for _, s := range samples {
		r.svcs = append(r.svcs, svcRow{ts: ts, ServiceSample: s})
	}
}

type hostRow struct {
	ts int64
	a  *hostAgg
}

type diskRow struct {
	ts    int64
	mount string
	a     *diskAgg
}

// Flush writes everything that has closed since the last call, then rolls up
// and, once an hour, sweeps. A failure drops the batch, remembers the error and
// returns it; the recorder keeps running.
func (r *Recorder) Flush(ctx context.Context) error {
	now := r.now()
	r.mu.Lock()
	if !r.enabled {
		r.mu.Unlock()
		return nil
	}
	cut := now.Unix() / HostStep * HostStep // buckets starting before this one are closed
	var hosts []hostRow
	var disks []diskRow
	for b, a := range r.host {
		if b+HostStep <= now.Unix() {
			hosts = append(hosts, hostRow{b, a})
			delete(r.host, b)
		}
	}
	for b, m := range r.disks {
		if b >= cut {
			continue
		}
		for mount, a := range m {
			disks = append(disks, diskRow{b, mount, a})
		}
		delete(r.disks, b)
	}
	svcs := r.svcs
	r.svcs = nil
	ret := r.ret
	sweepDue := r.lastSwp.IsZero() || now.Sub(r.lastSwp) >= sweepEvery
	r.mu.Unlock()

	err := r.write(ctx, now, hosts, disks, svcs)
	if err == nil && sweepDue {
		err = r.Sweep(ctx, now, ret)
		if err == nil {
			r.mu.Lock()
			r.lastSwp = now
			r.mu.Unlock()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.status.Dropped += len(hosts) + len(disks) + len(svcs)
		r.status.LastError = err.Error()
		r.status.LastErrorAt = now
		if r.o.OnError != nil && (r.lastErrL.IsZero() || now.Sub(r.lastErrL) >= errorLogEvery) {
			r.lastErrL = now
			r.o.OnError(err)
		}
		return err
	}
	r.status.LastFlush = now
	return nil
}

func (r *Recorder) write(ctx context.Context, now time.Time, hosts []hostRow, disks []diskRow, svcs []svcRow) error {
	mountKeys := map[string]int64{}
	for _, d := range disks {
		if _, ok := mountKeys[d.mount]; ok {
			continue
		}
		id, err := r.o.DB.KeyID(ctx, KindMount, d.mount)
		if err != nil {
			return err
		}
		mountKeys[d.mount] = id
	}
	svcKeys := map[string]int64{}
	for _, s := range svcs {
		if _, ok := svcKeys[s.Name]; ok {
			continue
		}
		id, err := r.o.DB.KeyID(ctx, KindService, s.Name)
		if err != nil {
			return err
		}
		svcKeys[s.Name] = id
	}
	sites, err := r.prepareSites(ctx, now)
	if err != nil {
		return err
	}

	return r.o.DB.Tx(ctx, func(tx *sql.Tx) error {
		// INSERT OR IGNORE: a clock that stepped backwards must drop the
		// overlapping rows, not fail the whole batch.
		for _, h := range hosts {
			n := float64(h.a.n)
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO host_10s
				(ts, cpu, cpu_max, load1, load1_max, load5, load15, mem_used, mem_avail, mem_avail_min, swap_used, net_rx, net_tx)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				h.ts, h.a.cpu/n, h.a.cpuMax, h.a.load1/n, h.a.load1Max, h.a.load5/n, h.a.l15/n,
				int64(h.a.memUsed/n), int64(h.a.memAvail/n), int64(h.a.memAvailMin), int64(h.a.swap/n), h.a.rx/n, h.a.tx/n); err != nil {
				return fmt.Errorf("metrics: insert host row: %w", err)
			}
		}
		for _, d := range disks {
			n := float64(d.a.n)
			var rd, wr any
			if d.a.ioN > 0 {
				rd, wr = d.a.rd/float64(d.a.ioN), d.a.wr/float64(d.a.ioN)
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO disk_10s
				(ts, key, used_pct, used_pct_max, used, rd_bps, wr_bps) VALUES (?,?,?,?,?,?,?)`,
				d.ts, mountKeys[d.mount], d.a.pct/n, d.a.pctMx, int64(d.a.used/n), rd, wr); err != nil {
				return fmt.Errorf("metrics: insert disk row: %w", err)
			}
		}
		for _, s := range svcs {
			var cpu, rss any
			if s.CPUOK {
				cpu = s.CPU
			}
			if s.RSSOK {
				rss = s.RSS
			}
			up := 0.0
			if s.State == StateRunning {
				up = 1
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO svc_30s
				(ts, key, state, up_frac, cpu, cpu_max, rss, rss_max) VALUES (?,?,?,?,?,?,?,?)`,
				s.ts, svcKeys[s.Name], s.State, up, cpu, cpu, rss, rss); err != nil {
				return fmt.Errorf("metrics: insert service row: %w", err)
			}
		}
		if err := writeSites(ctx, tx, sites); err != nil {
			return err
		}
		return rollupAll(ctx, tx, now.Add(-rollupGrace).Unix())
	})
}

// Run drives the recorder until ctx is done: it consumes sampler snapshots,
// polls the services every 30 s and flushes every minute. A final flush runs on
// the way out so a clean shutdown loses nothing.
func (r *Recorder) Run(ctx context.Context) {
	var snaps <-chan sysinfo.Snapshot
	if r.o.Snapshots != nil {
		ch, cancel := r.o.Snapshots.Subscribe()
		defer cancel()
		snaps = ch
	}
	tick := r.o.Tick
	if tick == nil {
		tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	flushC, stopFlush := tick(flushEvery)
	defer stopFlush()
	var svcC <-chan time.Time
	if r.o.Services != nil {
		c, stop := tick(ServiceStep * time.Second)
		defer stop()
		svcC = c
		r.pollServices(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = r.Flush(final)
			cancel()
			return
		case s, ok := <-snaps:
			if !ok {
				snaps = nil
				continue
			}
			r.Observe(s)
		case <-svcC:
			r.pollServices(ctx)
		case <-flushC:
			_ = r.Flush(ctx)
		}
	}
}

func (r *Recorder) pollServices(ctx context.Context) {
	r.ObserveServices(r.now(), r.o.Services.Usage(ctx))
}
