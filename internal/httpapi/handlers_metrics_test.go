package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/metrics"
)

type fakeMetrics struct {
	enabled bool
	err     error

	lastRange  metrics.Range
	lastDomain string
	lastName   string
	cleared    bool
}

func (f *fakeMetrics) Enabled() bool { return f.enabled }

func (f *fakeMetrics) Host(_ context.Context, r metrics.Range) (metrics.HostSeries, error) {
	f.lastRange = r
	v := 12.5
	return metrics.HostSeries{Step: 10, T: []int64{r.From}, CPU: []*float64{&v}}, f.err
}

func (f *fakeMetrics) Disks(_ context.Context, r metrics.Range) (metrics.DiskSeries, error) {
	f.lastRange = r
	return metrics.DiskSeries{Step: 10, T: []int64{r.From}, Mounts: []metrics.MountSeries{}}, f.err
}

func (f *fakeMetrics) Services(_ context.Context, r metrics.Range, name string) (metrics.ServicesSeries, error) {
	f.lastRange, f.lastName = r, name
	return metrics.ServicesSeries{Step: 30, T: []int64{r.From}, Services: []metrics.ServiceSeries{}}, f.err
}

func (f *fakeMetrics) Site(_ context.Context, domain string, r metrics.Range) (metrics.SiteSeries, error) {
	f.lastRange, f.lastDomain = r, domain
	return metrics.SiteSeries{Step: 60, T: []int64{r.From}}, f.err
}

func (f *fakeMetrics) Status(context.Context) (metrics.StatusReport, error) {
	return metrics.StatusReport{Enabled: f.enabled, Tiers: []metrics.TierStatus{}}, f.err
}

func (f *fakeMetrics) Clear(context.Context) error {
	f.cleared = true
	return f.err
}

var metricsPaths = []struct{ method, path string }{
	{http.MethodGet, "/api/metrics/host"},
	{http.MethodGet, "/api/metrics/disks"},
	{http.MethodGet, "/api/metrics/services"},
	{http.MethodGet, "/api/metrics/sites/example.com"},
	{http.MethodGet, "/api/metrics/status"},
	{http.MethodPost, "/api/metrics/clear"},
}

func TestMetricsRoutesRequireSession(t *testing.T) {
	h := newHarness(t)
	for _, c := range metricsPaths {
		if rec := h.do(c.method, c.path, "", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

func TestMetricsRoutesAnswer503WithoutService(t *testing.T) {
	h := newHarness(t)
	s := h.login()
	for _, c := range metricsPaths {
		if rec := h.do(c.method, c.path, s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestMetricsHistoryRoutesRefuseWhileOff(t *testing.T) {
	f := &fakeMetrics{enabled: false}
	h, s := m7Harness(t, func(d *Deps) { d.Metrics = f })
	for _, p := range []string{"/api/metrics/host", "/api/metrics/disks", "/api/metrics/services", "/api/metrics/sites/example.com"} {
		rec := h.do(http.MethodGet, p, s.cookie, "", nil)
		if rec.Code != http.StatusConflict {
			t.Errorf("GET %s while off = %d, want 409", p, rec.Code)
		}
		if env := h.errorEnvelopeOf(rec); env.Detail == "" {
			t.Errorf("GET %s gave no reason", p)
		}
	}
	// Status and clear stay usable so the file can be inspected and reset.
	if rec := h.do(http.MethodGet, "/api/metrics/status", s.cookie, "", nil); rec.Code != http.StatusOK {
		t.Errorf("status while off = %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/metrics/clear", s.cookie, s.csrf, nil); rec.Code != http.StatusOK || !f.cleared {
		t.Errorf("clear while off = %d cleared=%v", rec.Code, f.cleared)
	}
}

func TestMetricsRangeParsing(t *testing.T) {
	f := &fakeMetrics{enabled: true}
	h, s := m7Harness(t, func(d *Deps) { d.Metrics = f })
	get := func(q string) int {
		return h.do(http.MethodGet, "/api/metrics/host"+q, s.cookie, "", nil).Code
	}

	if code := get(""); code != http.StatusOK || f.lastRange.To-f.lastRange.From != 3600 {
		t.Errorf("default range: %d %+v, want one hour", code, f.lastRange)
	}
	for name, secs := range map[string]int64{"1h": 3600, "6h": 6 * 3600, "24h": 86400, "7d": 7 * 86400, "30d": 30 * 86400, "90d": 90 * 86400} {
		if code := get("?range=" + name); code != http.StatusOK || f.lastRange.To-f.lastRange.From != secs {
			t.Errorf("range=%s: %d %+v", name, code, f.lastRange)
		}
	}
	now := time.Now().Unix()
	from, to := now-7200, now-3600
	if code := get("?from=" + strconv.FormatInt(from, 10) + "&to=" + strconv.FormatInt(to, 10)); code != http.StatusOK ||
		f.lastRange.From != from || f.lastRange.To != to {
		t.Errorf("explicit range: %d %+v", code, f.lastRange)
	}
	for _, bad := range []string{"?range=2h", "?from=10", "?from=20&to=10", "?from=a&to=b", "?from=-5&to=10"} {
		if code := get(bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if code := get("?range="); code != http.StatusOK {
		t.Errorf("an empty range should mean the default, got %d", code)
	}
}

func TestMetricsSiteRoute(t *testing.T) {
	f := &fakeMetrics{enabled: true}
	h, s := m7Harness(t, func(d *Deps) { d.Metrics = f })
	if rec := h.do(http.MethodGet, "/api/metrics/sites/example.com?range=6h", s.cookie, "", nil); rec.Code != http.StatusOK || f.lastDomain != "example.com" {
		t.Fatalf("valid domain: %d %q", rec.Code, f.lastDomain)
	}
	f.lastDomain = ""
	for _, bad := range []string{"Example.COM", "-x.com", "exa%20mple.com", "..%2Fetc"} {
		if rec := h.do(http.MethodGet, "/api/metrics/sites/"+bad, s.cookie, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("domain %q = %d, want 404", bad, rec.Code)
		}
	}
	if f.lastDomain != "" {
		t.Errorf("an invalid domain reached the service: %q", f.lastDomain)
	}
	f.err = metrics.ErrUnknownSite
	if rec := h.do(http.MethodGet, "/api/metrics/sites/gone.example", s.cookie, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown site = %d, want 404", rec.Code)
	}
}

func TestMetricsServicesNameFilterAndErrors(t *testing.T) {
	f := &fakeMetrics{enabled: true}
	h, s := m7Harness(t, func(d *Deps) { d.Metrics = f })
	if rec := h.do(http.MethodGet, "/api/metrics/services?name=panel-nginx", s.cookie, "", nil); rec.Code != http.StatusOK || f.lastName != "panel-nginx" {
		t.Errorf("name filter: %d %q", rec.Code, f.lastName)
	}
	f.err = metrics.ErrBadRange
	if rec := h.do(http.MethodGet, "/api/metrics/host", s.cookie, "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("ErrBadRange = %d, want 400", rec.Code)
	}
	f.err = context.DeadlineExceeded
	if rec := h.do(http.MethodGet, "/api/metrics/host", s.cookie, "", nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("other error = %d, want 500", rec.Code)
	}
}

func TestMetricsHostBodyIsColumnar(t *testing.T) {
	f := &fakeMetrics{enabled: true}
	h, s := m7Harness(t, func(d *Deps) { d.Metrics = f })
	rec := h.do(http.MethodGet, "/api/metrics/host?range=1h", s.cookie, "", nil)
	var got struct {
		Step int64      `json:"step"`
		T    []int64    `json:"t"`
		CPU  []*float64 `json:"cpu"`
	}
	h.decode(rec, &got)
	if rec.Code != http.StatusOK || got.Step != 10 || len(got.T) != 1 || got.CPU[0] == nil || *got.CPU[0] != 12.5 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
