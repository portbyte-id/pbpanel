package metrics

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/hist"
	"github.com/portbyte-id/server-panel/internal/sysinfo"
)

// base is a multiple of 900, so every tier boundary falls on a round offset.
var base = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

func newTestRecorder(t *testing.T, o Options) (*Recorder, *testClock, *DB) {
	t.Helper()
	d := openTestDB(t)
	clk := &testClock{t: base}
	o.DB, o.Now = d, clk.now
	r, err := NewRecorder(o)
	if err != nil {
		t.Fatal(err)
	}
	return r, clk, d
}

func snapAt(at time.Time, cpu float64) sysinfo.Snapshot {
	return sysinfo.Snapshot{
		Time: at, Rates: true, CPUPercent: cpu,
		Load:   sysinfo.Load{One: 1, Five: 2, Fifteen: 3},
		Memory: sysinfo.Memory{UsedBytes: 1000, AvailableBytes: 3000, SwapUsedBytes: 50},
		Disks:  []sysinfo.DiskStat{{Mount: "/", UsedPercent: 40, UsedBytes: 4000, IO: true, ReadBytesPerSec: 100, WriteBytesPerSec: 200}},
		Net:    []sysinfo.NetRate{{Iface: "eth0", RxBytesPerSec: 10, TxBytesPerSec: 20}},
	}
}

func count(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	if err := d.SQL().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// runMinutes feeds 5 s snapshots for each listed minute (skipping the others)
// and flushes 20 s after every minute ends. cpu(m) is that minute's CPU.
func runMinutes(t *testing.T, r *Recorder, clk *testClock, minutes int, skip map[int]bool, cpu func(m, i int) float64) {
	t.Helper()
	for m := range minutes {
		if !skip[m] {
			for i := range 12 {
				at := base.Add(time.Duration(m)*time.Minute + time.Duration(i)*5*time.Second)
				r.Observe(snapAt(at, cpu(m, i)))
			}
		}
		clk.t = base.Add(time.Duration(m)*time.Minute + 80*time.Second)
		if err := r.Flush(context.Background()); err != nil {
			t.Fatalf("flush after minute %d: %v", m, err)
		}
	}
}

func TestSixMinutesFillEveryTier(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{})
	runMinutes(t, r, clk, 6, nil, func(m, i int) float64 {
		if m == 1 && i == 3 {
			return 90 // one spike, to prove the max survives the rollup
		}
		return 10 + float64(m)
	})

	if got := count(t, d, "host_10s"); got != 36 {
		t.Errorf("host_10s rows = %d, want 36", got)
	}
	if got := count(t, d, "host_1m"); got != 6 {
		t.Errorf("host_1m rows = %d, want 6", got)
	}
	if got := count(t, d, "host_5m"); got != 1 {
		t.Errorf("host_5m rows = %d, want 1", got)
	}
	if got := count(t, d, "disk_10s"); got != 36 {
		t.Errorf("disk_10s rows = %d, want 36", got)
	}

	var cpu, cpuMax float64
	var memUsed, availMin int64
	if err := d.SQL().QueryRow(`SELECT cpu, cpu_max, mem_used, mem_avail_min FROM host_1m WHERE ts = ?`, base.Unix()).
		Scan(&cpu, &cpuMax, &memUsed, &availMin); err != nil {
		t.Fatal(err)
	}
	if cpu != 10 || cpuMax != 10 || memUsed != 1000 || availMin != 3000 {
		t.Errorf("minute 0 = cpu %v max %v mem %d availMin %d", cpu, cpuMax, memUsed, availMin)
	}
	// Minute 1: six 10 s rows; the spike sat in the first bucket's two samples.
	if err := d.SQL().QueryRow(`SELECT cpu_max FROM host_1m WHERE ts = ?`, base.Add(time.Minute).Unix()).Scan(&cpuMax); err != nil {
		t.Fatal(err)
	}
	if cpuMax != 90 {
		t.Errorf("minute 1 cpu_max = %v, want the 90 spike", cpuMax)
	}
	var avg5 float64
	if err := d.SQL().QueryRow(`SELECT cpu FROM host_5m WHERE ts = ?`, base.Unix()).Scan(&avg5); err != nil {
		t.Fatal(err)
	}
	if avg5 < 11 || avg5 > 20 {
		t.Errorf("5 m cpu = %v, want a plausible mean of the five minutes", avg5)
	}

	var pct, rd float64
	key, _ := d.KeyID(context.Background(), KindMount, "/")
	if err := d.SQL().QueryRow(`SELECT used_pct, rd_bps FROM disk_1m WHERE ts = ? AND key = ?`, base.Unix(), key).Scan(&pct, &rd); err != nil {
		t.Fatal(err)
	}
	if pct != 40 || rd != 100 {
		t.Errorf("disk 1m = %v%% rd %v", pct, rd)
	}
}

func TestMissedMinuteLeavesAGapNotAFabricatedRow(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{})
	runMinutes(t, r, clk, 6, map[int]bool{2: true}, func(int, int) float64 { return 20 })
	var n int
	if err := d.SQL().QueryRow(`SELECT count(*) FROM host_1m WHERE ts = ?`, base.Add(2*time.Minute).Unix()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a row exists for the skipped minute (%d, %v)", n, err)
	}
	if got := count(t, d, "host_1m"); got != 5 {
		t.Errorf("host_1m rows = %d, want 5", got)
	}
}

func TestFirstSnapshotWithoutRatesIsSkipped(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{})
	s := snapAt(base, 99)
	s.Rates = false
	r.Observe(s)
	clk.t = base.Add(2 * time.Minute)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, "host_10s"); got != 0 {
		t.Errorf("a rate-less snapshot was recorded (%d rows)", got)
	}
}

func TestDisabledRecordsNothing(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{Disabled: true})
	runMinutes(t, r, clk, 3, nil, func(int, int) float64 { return 5 })
	if got := count(t, d, "host_10s"); got != 0 {
		t.Errorf("disabled recorder wrote %d rows", got)
	}
	r.SetEnabled(true)
	runMinutes(t, r, clk, 1, nil, func(int, int) float64 { return 5 })
	if got := count(t, d, "host_10s"); got != 6 {
		t.Errorf("after enabling: %d rows, want 6", got)
	}
}

func TestServiceRowsAndRollup(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{})
	for m := range 3 {
		for half := range 2 {
			at := base.Add(time.Duration(m)*time.Minute + time.Duration(half)*30*time.Second)
			state := StateRunning
			if m == 1 && half == 1 {
				state = StateFailed
			}
			r.ObserveServices(at, []ServiceSample{
				{Name: "nginx", Kind: "systemd", State: state, CPU: 5, CPUOK: true, RSS: 1 << 20, RSSOK: true},
				{Name: "gone", Kind: "systemd", State: StateStopped}, // no usage figures
			})
		}
		clk.t = base.Add(time.Duration(m)*time.Minute + 80*time.Second)
		if err := r.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(t, d, "svc_30s"); got != 12 {
		t.Errorf("svc_30s rows = %d, want 12", got)
	}
	key, _ := d.KeyID(context.Background(), KindService, "nginx")
	var state int
	var up float64
	var cpu sql.NullFloat64
	if err := d.SQL().QueryRow(`SELECT state, up_frac, cpu FROM svc_1m WHERE ts = ? AND key = ?`, base.Add(time.Minute).Unix(), key).
		Scan(&state, &up, &cpu); err != nil {
		t.Fatal(err)
	}
	if state != StateFailed || up != 0.5 || !cpu.Valid || cpu.Float64 != 5 {
		t.Errorf("nginx minute 1 = state %d up %v cpu %v", state, up, cpu)
	}
	gone, _ := d.KeyID(context.Background(), KindService, "gone")
	if err := d.SQL().QueryRow(`SELECT cpu FROM svc_1m WHERE ts = ? AND key = ?`, base.Unix(), gone).Scan(&cpu); err != nil || cpu.Valid {
		t.Errorf("stopped service has a cpu figure: %v %v", cpu, err)
	}
}

func TestSweepDeletesInChunks(t *testing.T) {
	r, _, d := newTestRecorder(t, Options{})
	ctx := context.Background()
	old := base.Add(-48 * time.Hour).Unix()
	if err := d.Tx(ctx, func(tx *sql.Tx) error {
		for i := range 12000 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO host_10s (ts, cpu) VALUES (?, 1)`, old+int64(i)); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO host_10s (ts, cpu) VALUES (?, 2)`, base.Unix())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Sweep(ctx, base, Retention{Raw: 24 * time.Hour, Minute: time.Hour, Coarse: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, "host_10s"); got != 1 {
		t.Errorf("rows after sweep = %d, want the one recent row", got)
	}
}

type fakeSites struct{ minutes []SiteMinute }

func (f *fakeSites) DrainMinutes(closedBefore time.Time) []SiteMinute {
	var out []SiteMinute
	for _, m := range f.minutes {
		if m.Minute+60 <= closedBefore.Unix() {
			out = append(out, m)
		}
	}
	return out
}

func siteMin(domain string, minute time.Time, req int64) SiteMinute {
	m := SiteMinute{Domain: domain, Minute: minute.Unix(), Req: req, S2: req - 1, S5: 1, Bytes: req * 100}
	for range req {
		m.RT.Add(0.05)
	}
	return m
}

func TestSiteMinutesAreNotDoubleCountedAcrossARestart(t *testing.T) {
	src := &fakeSites{}
	for m := range 20 {
		src.minutes = append(src.minutes, siteMin("example.com", base.Add(time.Duration(m)*time.Minute), 10))
	}
	r, clk, d := newTestRecorder(t, Options{Sites: src})
	clk.t = base.Add(21 * time.Minute)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	sum := func(table string) int {
		var n sql.NullInt64
		if err := d.SQL().QueryRow(`SELECT sum(req) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return int(n.Int64)
	}
	if got := sum("site_1m"); got != 200 {
		t.Fatalf("site_1m requests = %d, want 200", got)
	}
	if got := sum("site_15m"); got != 150 {
		t.Errorf("site_15m requests = %d, want the first full quarter hour (150)", got)
	}

	// Restart: a new recorder on the same file, and the collector's backfill
	// replays every minute again. Nothing may be counted twice.
	r2, err := NewRecorder(Options{DB: d, Now: clk.now, Sites: src})
	if err != nil {
		t.Fatal(err)
	}
	clk.t = base.Add(22 * time.Minute)
	if err := r2.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sum("site_1m"); got != 200 {
		t.Errorf("after replay site_1m requests = %d, want 200", got)
	}

	var rt []byte
	key, _ := d.KeyID(context.Background(), KindSite, "example.com")
	if err := d.SQL().QueryRow(`SELECT rt_hist FROM site_15m WHERE key = ? ORDER BY ts LIMIT 1`, key).Scan(&rt); err != nil {
		t.Fatal(err)
	}
	h, err := hist.Decode(rt)
	if err != nil || h.Total() != 150 {
		t.Errorf("merged histogram total = %d (%v), want 150", h.Total(), err)
	}
	if p, _ := h.Quantile(0.5); math.Abs(p-0.035) > 0.02 {
		t.Errorf("p50 = %v, want near the 20-50 ms bin", p)
	}
}

func TestSitesCanBeSwitchedOff(t *testing.T) {
	src := &fakeSites{minutes: []SiteMinute{siteMin("example.com", base, 5)}}
	r, clk, d := newTestRecorder(t, Options{Sites: src})
	r.SetSitesEnabled(false)
	clk.t = base.Add(5 * time.Minute)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, "site_1m"); got != 0 {
		t.Errorf("site rows written while off: %d", got)
	}
}

func TestFailedFlushDropsTheBatchAndRecordsTheError(t *testing.T) {
	var heard int
	r, clk, d := newTestRecorder(t, Options{OnError: func(error) { heard++ }})
	for i := range 6 {
		r.Observe(snapAt(base.Add(time.Duration(i)*5*time.Second), 10))
	}
	if _, err := d.SQL().Exec(`DROP TABLE host_10s`); err != nil {
		t.Fatal(err)
	}
	clk.t = base.Add(2 * time.Minute)
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("flush into a missing table succeeded")
	}
	st := r.Status()
	if st.LastError == "" || st.Dropped == 0 || heard != 1 {
		t.Errorf("status = %+v, heard %d", st, heard)
	}
	if _, ok := r.Latest(); !ok {
		t.Error("the in-memory snapshot is gone after a failed flush")
	}
	// A second failure inside the rate-limit window is not reported again.
	clk.t = base.Add(3 * time.Minute)
	r.Observe(snapAt(clk.t.Add(-time.Minute), 10))
	_ = r.Flush(context.Background())
	if heard != 1 {
		t.Errorf("error reported %d times inside the rate-limit window", heard)
	}
}
