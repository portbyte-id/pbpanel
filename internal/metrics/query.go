package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/portbyte-id/server-panel/internal/hist"
)

// MaxPoints bounds one series of a response.
const MaxPoints = 1500

// ErrBadRange is returned for a range that is empty, inverted or absurdly wide.
var ErrBadRange = errors.New("metrics: bad range")

// Range is a half-open interval of unix seconds.
type Range struct{ From, To int64 }

// Preset ranges the UI offers.
var presetSeconds = map[string]int64{
	"1h": 3600, "6h": 6 * 3600, "24h": 24 * 3600, "7d": 7 * 86400, "30d": 30 * 86400, "90d": 90 * 86400,
}

// PresetRange resolves one of 1h, 6h, 24h, 7d, 30d, 90d to the interval ending
// at now. ok is false for any other name.
func PresetRange(name string, now int64) (Range, bool) {
	s, ok := presetSeconds[name]
	if !ok {
		return Range{}, false
	}
	return Range{From: now - s, To: now}, true
}

type tier struct {
	table string
	step  int64
}

var (
	hostTiers = []tier{{"host_10s", 10}, {"host_1m", 60}, {"host_5m", 300}}
	diskTiers = []tier{{"disk_10s", 10}, {"disk_1m", 60}, {"disk_5m", 300}}
	svcTiers  = []tier{{"svc_30s", 30}, {"svc_1m", 60}, {"svc_5m", 300}}
	siteTiers = []tier{{"site_1m", 60}, {"site_15m", 900}}
)

// plan chooses the finest tier whose retention reaches back to the start of the
// range, and a step that keeps the series under MaxPoints. tiers[i] is kept for
// retentions[i].
func plan(r Range, now int64, tiers []tier, retentions []int64) (tier, int64, error) {
	if r.To <= r.From || r.From < 0 {
		return tier{}, 0, ErrBadRange
	}
	if r.To-r.From > 400*86400 {
		return tier{}, 0, fmt.Errorf("%w: wider than 400 days", ErrBadRange)
	}
	age := now - r.From
	pick := tiers[len(tiers)-1]
	for i, t := range tiers {
		if age <= retentions[i] {
			pick = t
			break
		}
	}
	step := (r.To - r.From + MaxPoints - 1) / MaxPoints
	step = (step + pick.step - 1) / pick.step * pick.step
	step = max(step, pick.step)
	return pick, step, nil
}

func (s *Service) retentions(three bool) []int64 {
	ret := s.rec.retention()
	if three {
		return []int64{int64(ret.Raw.Seconds()), int64(ret.Minute.Seconds()), int64(ret.Coarse.Seconds())}
	}
	return []int64{int64(ret.Minute.Seconds()), int64(ret.Coarse.Seconds())}
}

// grid is the time axis of a response: n buckets of step seconds from start.
type grid struct {
	start, step int64
	n           int
}

func newGrid(r Range, step int64) (grid, error) {
	start := r.From / step * step
	n := int((r.To - start + step - 1) / step)
	if n < 1 || n > 2*MaxPoints {
		return grid{}, ErrBadRange
	}
	return grid{start: start, step: step, n: n}, nil
}

func (g grid) times() []int64 {
	t := make([]int64, g.n)
	for i := range t {
		t[i] = g.start + int64(i)*g.step
	}
	return t
}

func (g grid) col() []*float64 { return make([]*float64, g.n) }

func (g grid) at(bucket int64) (int, bool) {
	if bucket < 0 || bucket >= int64(g.n) {
		return 0, false
	}
	return int(bucket), true
}

func set(col []*float64, i int, v sql.NullFloat64) {
	if v.Valid {
		x := v.Float64
		col[i] = &x
	}
}

// HostSeries is the host history, one value per bucket, null where nothing was
// recorded.
type HostSeries struct {
	Step     int64      `json:"step"`
	T        []int64    `json:"t"`
	CPU      []*float64 `json:"cpu"`
	CPUMax   []*float64 `json:"cpu_max"`
	Load1    []*float64 `json:"load1"`
	Load1Max []*float64 `json:"load1_max"`
	Load5    []*float64 `json:"load5"`
	Load15   []*float64 `json:"load15"`
	MemUsed  []*float64 `json:"mem_used"`
	MemAvail []*float64 `json:"mem_avail"`
	SwapUsed []*float64 `json:"swap_used"`
	NetRx    []*float64 `json:"net_rx"`
	NetTx    []*float64 `json:"net_tx"`
}

// Host reads the host history for r.
func (s *Service) Host(ctx context.Context, r Range) (HostSeries, error) {
	t, step, err := plan(r, s.now().Unix(), hostTiers, s.retentions(true))
	if err != nil {
		return HostSeries{}, err
	}
	g, err := newGrid(r, step)
	if err != nil {
		return HostSeries{}, err
	}
	out := HostSeries{Step: step, T: g.times(), CPU: g.col(), CPUMax: g.col(), Load1: g.col(), Load1Max: g.col(),
		Load5: g.col(), Load15: g.col(), MemUsed: g.col(), MemAvail: g.col(), SwapUsed: g.col(), NetRx: g.col(), NetTx: g.col()}
	rows, err := s.db.SQL().QueryContext(ctx, fmt.Sprintf(`SELECT (ts - ?) / ?, avg(cpu), max(cpu_max), avg(load1), max(load1_max),
		avg(load5), avg(load15), avg(mem_used), avg(mem_avail), avg(swap_used), avg(net_rx), avg(net_tx)
		FROM %s WHERE ts >= ? AND ts < ? GROUP BY 1`, t.table), g.start, step, g.start, r.To)
	if err != nil {
		return HostSeries{}, fmt.Errorf("metrics: read host history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var b int64
		var v [11]sql.NullFloat64
		if err := rows.Scan(&b, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8], &v[9], &v[10]); err != nil {
			return HostSeries{}, fmt.Errorf("metrics: read host history: %w", err)
		}
		i, ok := g.at(b)
		if !ok {
			continue
		}
		for k, col := range [][]*float64{out.CPU, out.CPUMax, out.Load1, out.Load1Max, out.Load5, out.Load15,
			out.MemUsed, out.MemAvail, out.SwapUsed, out.NetRx, out.NetTx} {
			set(col, i, v[k])
		}
	}
	return out, rows.Err()
}

// MountSeries is one filesystem.
type MountSeries struct {
	Mount      string     `json:"mount"`
	UsedPct    []*float64 `json:"used_pct"`
	UsedPctMax []*float64 `json:"used_pct_max"`
	Used       []*float64 `json:"used"`
	RdBps      []*float64 `json:"rd_bps"`
	WrBps      []*float64 `json:"wr_bps"`
}

// DiskSeries is the disk history.
type DiskSeries struct {
	Step   int64         `json:"step"`
	T      []int64       `json:"t"`
	Mounts []MountSeries `json:"mounts"`
}

// Disks reads usage and IO for every mount recorded in r.
func (s *Service) Disks(ctx context.Context, r Range) (DiskSeries, error) {
	t, step, err := plan(r, s.now().Unix(), diskTiers, s.retentions(true))
	if err != nil {
		return DiskSeries{}, err
	}
	g, err := newGrid(r, step)
	if err != nil {
		return DiskSeries{}, err
	}
	names, err := s.db.KeyNames(ctx, KindMount)
	if err != nil {
		return DiskSeries{}, err
	}
	by := map[int64]*MountSeries{}
	rows, err := s.db.SQL().QueryContext(ctx, fmt.Sprintf(`SELECT (ts - ?) / ?, key, avg(used_pct), max(used_pct_max), avg(used), avg(rd_bps), avg(wr_bps)
		FROM %s WHERE ts >= ? AND ts < ? GROUP BY 1, 2`, t.table), g.start, step, g.start, r.To)
	if err != nil {
		return DiskSeries{}, fmt.Errorf("metrics: read disk history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var b, key int64
		var v [5]sql.NullFloat64
		if err := rows.Scan(&b, &key, &v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
			return DiskSeries{}, fmt.Errorf("metrics: read disk history: %w", err)
		}
		ms := by[key]
		if ms == nil {
			ms = &MountSeries{Mount: names[key], UsedPct: g.col(), UsedPctMax: g.col(), Used: g.col(), RdBps: g.col(), WrBps: g.col()}
			by[key] = ms
		}
		if i, ok := g.at(b); ok {
			for k, col := range [][]*float64{ms.UsedPct, ms.UsedPctMax, ms.Used, ms.RdBps, ms.WrBps} {
				set(col, i, v[k])
			}
		}
	}
	if err := rows.Err(); err != nil {
		return DiskSeries{}, err
	}
	out := DiskSeries{Step: step, T: g.times(), Mounts: []MountSeries{}}
	for _, ms := range by {
		out.Mounts = append(out.Mounts, *ms)
	}
	sort.Slice(out.Mounts, func(i, j int) bool { return out.Mounts[i].Mount < out.Mounts[j].Mount })
	return out, nil
}

// ServiceSeries is one service.
type ServiceSeries struct {
	Name   string     `json:"name"`
	State  []*float64 `json:"state"`
	UpFrac []*float64 `json:"up_frac"`
	CPU    []*float64 `json:"cpu"`
	CPUMax []*float64 `json:"cpu_max"`
	RSS    []*float64 `json:"rss"`
	RSSMax []*float64 `json:"rss_max"`
}

// ServicesSeries is the service history.
type ServicesSeries struct {
	Step     int64           `json:"step"`
	T        []int64         `json:"t"`
	Services []ServiceSeries `json:"services"`
}

// Services reads state and usage for every service in r, or only name when it is
// not empty.
func (s *Service) Services(ctx context.Context, r Range, name string) (ServicesSeries, error) {
	t, step, err := plan(r, s.now().Unix(), svcTiers, s.retentions(true))
	if err != nil {
		return ServicesSeries{}, err
	}
	g, err := newGrid(r, step)
	if err != nil {
		return ServicesSeries{}, err
	}
	names, err := s.db.KeyNames(ctx, KindService)
	if err != nil {
		return ServicesSeries{}, err
	}
	q := fmt.Sprintf(`SELECT (ts - ?) / ?, key, max(state), avg(up_frac), avg(cpu), max(cpu_max), avg(rss), max(rss_max)
		FROM %s WHERE ts >= ? AND ts < ?`, t.table)
	args := []any{g.start, step, g.start, r.To}
	if name != "" {
		var want int64 = -1
		for id, n := range names {
			if n == name {
				want = id
			}
		}
		q += ` AND key = ?`
		args = append(args, want)
	}
	rows, err := s.db.SQL().QueryContext(ctx, q+` GROUP BY 1, 2`, args...)
	if err != nil {
		return ServicesSeries{}, fmt.Errorf("metrics: read service history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	by := map[int64]*ServiceSeries{}
	for rows.Next() {
		var b, key int64
		var v [6]sql.NullFloat64
		if err := rows.Scan(&b, &key, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5]); err != nil {
			return ServicesSeries{}, fmt.Errorf("metrics: read service history: %w", err)
		}
		ss := by[key]
		if ss == nil {
			ss = &ServiceSeries{Name: names[key], State: g.col(), UpFrac: g.col(), CPU: g.col(), CPUMax: g.col(), RSS: g.col(), RSSMax: g.col()}
			by[key] = ss
		}
		if i, ok := g.at(b); ok {
			for k, col := range [][]*float64{ss.State, ss.UpFrac, ss.CPU, ss.CPUMax, ss.RSS, ss.RSSMax} {
				set(col, i, v[k])
			}
		}
	}
	if err := rows.Err(); err != nil {
		return ServicesSeries{}, err
	}
	out := ServicesSeries{Step: step, T: g.times(), Services: []ServiceSeries{}}
	for _, ss := range by {
		out.Services = append(out.Services, *ss)
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	return out, nil
}

// SiteSeries is one site's traffic. Percentiles are interpolated inside a fixed
// histogram bin, so they are estimates good to about 20%.
type SiteSeries struct {
	Step int64      `json:"step"`
	T    []int64    `json:"t"`
	Req  []*float64 `json:"req"`
	S2   []*float64 `json:"s2"`
	S3   []*float64 `json:"s3"`
	S4   []*float64 `json:"s4"`
	S5   []*float64 `json:"s5"`
	// Bytes is the response bytes sent in the bucket.
	Bytes []*float64 `json:"bytes"`
	// RTP50 and the rest are seconds. The upstream ones are null for sites whose
	// log lines carry no upstream time (an older log format, or only static files).
	RTP50  []*float64 `json:"rt_p50"`
	RTP95  []*float64 `json:"rt_p95"`
	URTP50 []*float64 `json:"urt_p50"`
	URTP95 []*float64 `json:"urt_p95"`
}

// ErrUnknownSite is returned for a domain with no history key and that the site
// list does not know either.
var ErrUnknownSite = errors.New("metrics: unknown site")

// Site reads one site's traffic for r.
func (s *Service) Site(ctx context.Context, domain string, r Range) (SiteSeries, error) {
	key, known, err := s.siteKey(ctx, domain)
	if err != nil {
		return SiteSeries{}, err
	}
	t, step, err := plan(r, s.now().Unix(), siteTiers, s.retentions(false))
	if err != nil {
		return SiteSeries{}, err
	}
	g, err := newGrid(r, step)
	if err != nil {
		return SiteSeries{}, err
	}
	out := SiteSeries{Step: step, T: g.times(), Req: g.col(), S2: g.col(), S3: g.col(), S4: g.col(), S5: g.col(), Bytes: g.col(),
		RTP50: g.col(), RTP95: g.col(), URTP50: g.col(), URTP95: g.col()}
	if !known {
		return out, nil // a site that exists but has recorded nothing yet
	}
	type acc struct {
		req, s2, s3, s4, s5, bytes int64
		rt, urt                    hist.H
	}
	accs := map[int64]*acc{}
	rows, err := s.db.SQL().QueryContext(ctx, fmt.Sprintf(`SELECT ts, req, s2, s3, s4, s5, bytes, rt_hist, urt_hist
		FROM %s WHERE key = ? AND ts >= ? AND ts < ?`, t.table), key, g.start, r.To)
	if err != nil {
		return SiteSeries{}, fmt.Errorf("metrics: read site history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var ts, req, s2, s3, s4, s5, bytes int64
		var rt, urt []byte
		if err := rows.Scan(&ts, &req, &s2, &s3, &s4, &s5, &bytes, &rt, &urt); err != nil {
			return SiteSeries{}, fmt.Errorf("metrics: read site history: %w", err)
		}
		b := (ts - g.start) / step
		a := accs[b]
		if a == nil {
			a = &acc{}
			accs[b] = a
		}
		a.req += req
		a.s2 += s2
		a.s3 += s3
		a.s4 += s4
		a.s5 += s5
		a.bytes += bytes
		if h, err := hist.Decode(rt); err == nil {
			a.rt.Merge(h)
		}
		if h, err := hist.Decode(urt); err == nil {
			a.urt.Merge(h)
		}
	}
	if err := rows.Err(); err != nil {
		return SiteSeries{}, err
	}
	up, err := s.hostPresence(ctx, g, r.To, step)
	if err != nil {
		return SiteSeries{}, err
	}
	f := func(n int64) *float64 { x := float64(n); return &x }
	for i := range g.n {
		a := accs[int64(i)]
		switch {
		case a != nil:
			out.Req[i], out.S2[i], out.S3[i], out.S4[i], out.S5[i], out.Bytes[i] = f(a.req), f(a.s2), f(a.s3), f(a.s4), f(a.s5), f(a.bytes)
			if v, ok := a.rt.Quantile(0.5); ok {
				out.RTP50[i] = &v
			}
			if v, ok := a.rt.Quantile(0.95); ok {
				out.RTP95[i] = &v
			}
			if v, ok := a.urt.Quantile(0.5); ok {
				out.URTP50[i] = &v
			}
			if v, ok := a.urt.Quantile(0.95); ok {
				out.URTP95[i] = &v
			}
		case up[i]:
			// The daemon was recording and the site saw no traffic: that is zero
			// requests, not missing data.
			out.Req[i], out.S2[i], out.S3[i], out.S4[i], out.S5[i], out.Bytes[i] = f(0), f(0), f(0), f(0), f(0), f(0)
		}
	}
	return out, nil
}

// hostPresence marks the buckets in which the host history has a row, i.e. the
// daemon was recording.
func (s *Service) hostPresence(ctx context.Context, g grid, to, step int64) ([]bool, error) {
	table := "host_1m"
	if age := s.now().Unix() - g.start; age > int64(s.rec.retention().Minute.Seconds()) {
		table = "host_5m"
	}
	rows, err := s.db.SQL().QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT (ts - ?) / ? FROM %s WHERE ts >= ? AND ts < ?`, table), g.start, step, g.start, to)
	if err != nil {
		return nil, fmt.Errorf("metrics: read host presence: %w", err)
	}
	defer func() { _ = rows.Close() }()
	up := make([]bool, g.n)
	for rows.Next() {
		var b int64
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if i, ok := g.at(b); ok {
			up[i] = true
		}
	}
	return up, rows.Err()
}

func (s *Service) siteKey(ctx context.Context, domain string) (key int64, hasHistory bool, err error) {
	names, err := s.db.KeyNames(ctx, KindSite)
	if err != nil {
		return 0, false, err
	}
	for id, n := range names {
		if n == domain {
			return id, true, nil
		}
	}
	if s.sites != nil {
		list, lerr := s.sites(ctx)
		if lerr == nil {
			for _, d := range list {
				if d == domain {
					return 0, false, nil
				}
			}
		}
	}
	return 0, false, ErrUnknownSite
}
