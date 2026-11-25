package metrics

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestService(t *testing.T, o Options) (*Service, *Recorder, *testClock) {
	t.Helper()
	r, clk, d := newTestRecorder(t, o)
	s, err := NewService(ServiceOptions{DB: d, Recorder: r, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return s, r, clk
}

func TestEveryPresetStaysUnderTheCap(t *testing.T) {
	now := base.Unix()
	ret := []int64{86400, 7 * 86400, 90 * 86400}
	for name := range presetSeconds {
		r, ok := PresetRange(name, now)
		if !ok {
			t.Fatalf("preset %s unknown", name)
		}
		tr, step, err := plan(r, now, hostTiers, ret)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		g, err := newGrid(r, step)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if g.n > MaxPoints+1 {
			t.Errorf("%s: %d points on tier %s step %d, want at most %d", name, g.n, tr.table, step, MaxPoints)
		}
		if step%tr.step != 0 {
			t.Errorf("%s: step %d is not a multiple of the tier step %d", name, step, tr.step)
		}
	}
}

func TestPlanPicksTheFinestTierThatReaches(t *testing.T) {
	now := base.Unix()
	ret := []int64{86400, 7 * 86400, 90 * 86400}
	cases := []struct {
		name  string
		table string
	}{{"1h", "host_10s"}, {"24h", "host_10s"}, {"7d", "host_1m"}, {"30d", "host_5m"}, {"90d", "host_5m"}}
	for _, c := range cases {
		r, _ := PresetRange(c.name, now)
		tr, _, _ := plan(r, now, hostTiers, ret)
		if tr.table != c.table {
			t.Errorf("%s uses %s, want %s", c.name, tr.table, c.table)
		}
	}
	// A shortened raw retention pushes a 6 h range to the minute tier.
	r, _ := PresetRange("6h", now)
	if tr, _, _ := plan(r, now, hostTiers, []int64{3600, 7 * 86400, 90 * 86400}); tr.table != "host_1m" {
		t.Errorf("6h with 1h raw retention uses %s", tr.table)
	}
}

func TestPlanRejectsBadRanges(t *testing.T) {
	now := base.Unix()
	for _, r := range []Range{{10, 10}, {20, 10}, {-5, 10}, {0, 500 * 86400}} {
		if _, _, err := plan(r, now, hostTiers, []int64{1, 1, 1}); !errors.Is(err, ErrBadRange) {
			t.Errorf("plan(%v) = %v, want ErrBadRange", r, err)
		}
	}
	if _, ok := PresetRange("2h", now); ok {
		t.Error("an unknown preset resolved")
	}
}

func TestHostQueryReturnsGapsAsNull(t *testing.T) {
	s, r, clk := newTestService(t, Options{})
	runMinutes(t, r, clk, 6, map[int]bool{2: true}, func(int, int) float64 { return 20 })
	got, err := s.Host(context.Background(), Range{From: base.Unix(), To: base.Add(6 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != 10 || len(got.T) != 36 || len(got.CPU) != 36 {
		t.Fatalf("step %d, %d points", got.Step, len(got.T))
	}
	for i, v := range got.CPU {
		minute := i / 6
		switch {
		case minute == 2 && v != nil:
			t.Fatalf("bucket %d inside the gap has a value %v", i, *v)
		case minute != 2 && (v == nil || *v != 20):
			t.Fatalf("bucket %d = %v, want 20", i, v)
		}
	}
	if got.MemUsed[0] == nil || *got.MemUsed[0] != 1000 || *got.NetRx[0] != 10 {
		t.Errorf("mem/net = %v %v", got.MemUsed[0], got.NetRx[0])
	}
}

func TestHostQueryAggregatesToTheStep(t *testing.T) {
	s, r, clk := newTestService(t, Options{})
	runMinutes(t, r, clk, 6, nil, func(m, _ int) float64 { return float64(10 * (m + 1)) })
	// A 24 h range at the 10 s tier is far past 1 500 points, so it is grouped to
	// 60 s buckets: each is the mean of six 10 s rows.
	got, err := s.Host(context.Background(), Range{From: base.Unix() - 3600, To: base.Add(6 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Step < 10 || len(got.T) > MaxPoints+1 {
		t.Fatalf("step %d with %d points", got.Step, len(got.T))
	}
	var sawFirst, sawLast bool
	for i, v := range got.CPU {
		if v == nil {
			continue
		}
		if got.T[i] == base.Unix() && got.Step <= 10 {
			sawFirst = *v == 10
		}
		if got.T[i] == base.Add(5*time.Minute).Unix() && got.Step <= 60 {
			sawLast = *v == 60
		}
	}
	if got.Step <= 10 && !sawFirst {
		t.Error("first bucket is not minute 0's value")
	}
	if got.Step <= 60 && !sawLast {
		t.Error("last bucket is not minute 5's value")
	}
}

func TestDisksAndServicesQueries(t *testing.T) {
	s, r, clk := newTestService(t, Options{})
	ctx := context.Background()
	for m := range 3 {
		for i := range 12 {
			r.Observe(snapAt(base.Add(time.Duration(m)*time.Minute+time.Duration(i)*5*time.Second), 10))
		}
		state := StateRunning
		if m == 1 {
			state = StateFailed
		}
		r.ObserveServices(base.Add(time.Duration(m)*time.Minute), []ServiceSample{
			{Name: "nginx", Kind: "systemd", State: state, CPU: 4, CPUOK: true, RSS: 2048, RSSOK: true},
			{Name: "docker", Kind: "systemd", State: StateRunning},
		})
		clk.t = base.Add(time.Duration(m)*time.Minute + 80*time.Second)
		if err := r.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rng := Range{From: base.Unix(), To: base.Add(3 * time.Minute).Unix()}

	d, err := s.Disks(ctx, rng)
	if err != nil || len(d.Mounts) != 1 || d.Mounts[0].Mount != "/" {
		t.Fatalf("disks = %+v, %v", d, err)
	}
	if v := d.Mounts[0].UsedPct[0]; v == nil || *v != 40 {
		t.Errorf("used_pct[0] = %v", v)
	}
	if v := d.Mounts[0].WrBps[0]; v == nil || *v != 200 {
		t.Errorf("wr_bps[0] = %v", v)
	}

	sv, err := s.Services(ctx, rng, "")
	if err != nil || len(sv.Services) != 2 || sv.Services[0].Name != "docker" {
		t.Fatalf("services = %+v, %v", sv, err)
	}
	only, err := s.Services(ctx, rng, "nginx")
	if err != nil || len(only.Services) != 1 || only.Services[0].Name != "nginx" {
		t.Fatalf("filtered = %+v, %v", only, err)
	}
	if none, _ := s.Services(ctx, rng, "nope"); len(none.Services) != 0 {
		t.Errorf("an unknown service returned rows: %+v", none.Services)
	}
	nginx := only.Services[0]
	// The 30 s tier: the failed sample sits at base+60, bucket 2 of six.
	if only.Step != 30 || nginx.State[2] == nil || *nginx.State[2] != StateFailed || nginx.State[0] == nil || *nginx.State[0] != StateRunning {
		t.Errorf("step %d, states %v %v", only.Step, nginx.State[0], nginx.State[2])
	}
	if nginx.CPU[0] == nil || *nginx.CPU[0] != 4 || nginx.RSS[0] == nil || *nginx.RSS[0] != 2048 {
		t.Errorf("nginx cpu/rss = %v %v", nginx.CPU[0], nginx.RSS[0])
	}
}

func TestSiteQueryFillsZerosOnlyWhileTheDaemonWasUp(t *testing.T) {
	src := &fakeSites{minutes: []SiteMinute{
		siteMin("example.com", base, 10),
		siteMin("example.com", base.Add(2*time.Minute), 30),
	}}
	s, r, clk := newTestService(t, Options{Sites: src})
	runMinutes(t, r, clk, 5, map[int]bool{4: true}, func(int, int) float64 { return 5 })
	got, err := s.Site(context.Background(), "example.com", Range{From: base.Unix(), To: base.Add(5 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != 60 {
		t.Fatalf("step = %d, want the 1 m site tier", got.Step)
	}
	want := []float64{10, 0, 30, 0}
	for i, w := range want {
		if got.Req[i] == nil || *got.Req[i] != w {
			t.Errorf("req[%d] = %v, want %v", i, got.Req[i], w)
		}
	}
	if got.Req[4] != nil {
		t.Errorf("req[4] = %v, want null: the daemon recorded nothing that minute", *got.Req[4])
	}
	if got.RTP50[0] == nil || *got.RTP50[0] <= 0.02 || *got.RTP50[0] > 0.05 {
		t.Errorf("rt_p50[0] = %v, want inside the 20-50 ms bin", got.RTP50[0])
	}
	if got.URTP95[0] != nil {
		t.Errorf("upstream p95 = %v, want null: these lines carry no upstream time", *got.URTP95[0])
	}
	if got.S5[0] == nil || *got.S5[0] != 1 || *got.Bytes[0] != 1000 {
		t.Errorf("s5/bytes = %v %v", got.S5[0], got.Bytes[0])
	}
}

func TestSiteQueryUnknownAndKnownEmpty(t *testing.T) {
	r, clk, d := newTestRecorder(t, Options{})
	known := func(context.Context) ([]string, error) { return []string{"quiet.example"}, nil }
	s, err := NewService(ServiceOptions{DB: d, Recorder: r, Sites: known, Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	rng := Range{From: base.Unix(), To: base.Add(time.Hour).Unix()}
	if _, err := s.Site(context.Background(), "nope.example", rng); !errors.Is(err, ErrUnknownSite) {
		t.Errorf("unknown site error = %v", err)
	}
	got, err := s.Site(context.Background(), "quiet.example", rng)
	if err != nil || len(got.T) == 0 || got.Req[0] != nil {
		t.Errorf("a managed site with no history: %+v, %v", got.Req, err)
	}
}

func TestStatusAndClear(t *testing.T) {
	s, r, clk := newTestService(t, Options{})
	ctx := context.Background()
	runMinutes(t, r, clk, 3, nil, func(int, int) float64 { return 7 })
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.FileBytes == 0 || st.LastFlush == 0 || len(st.Tiers) != len(dataTables) {
		t.Errorf("status = %+v", st)
	}
	var host10 TierStatus
	for _, tr := range st.Tiers {
		if tr.Table == "host_10s" {
			host10 = tr
		}
	}
	if host10.Rows != 18 || host10.Oldest != base.Unix() {
		t.Errorf("host_10s = %+v, want 18 rows from %d", host10, base.Unix())
	}

	if err := s.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Status(ctx)
	for _, tr := range after.Tiers {
		if tr.Rows != 0 {
			t.Errorf("%s has %d rows after Clear", tr.Table, tr.Rows)
		}
	}
	// The recorder carries on into an emptied database, with fresh series keys.
	runMinutes(t, r, clk, 1, nil, func(int, int) float64 { return 7 })
	if n := count(t, r.o.DB, "disk_10s"); n == 0 {
		t.Error("recording did not resume after Clear")
	}
}
