package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/metrics"
	"github.com/portbyte-id/server-panel/internal/wafmon"
)

// MetricsService is the part of *metrics.Service the history routes use; tests
// substitute a fake. A nil MetricsService leaves every /api/metrics route
// answering 503.
type MetricsService interface {
	Enabled() bool
	Host(ctx context.Context, r metrics.Range) (metrics.HostSeries, error)
	Disks(ctx context.Context, r metrics.Range) (metrics.DiskSeries, error)
	Services(ctx context.Context, r metrics.Range, name string) (metrics.ServicesSeries, error)
	Site(ctx context.Context, domain string, r metrics.Range) (metrics.SiteSeries, error)
	Status(ctx context.Context) (metrics.StatusReport, error)
	Clear(ctx context.Context) error
}

const metricsDefaultRange = "1h"

func (s *server) metricsRoutes(mux *http.ServeMux) {
	h := func(history bool, fn func(MetricsService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m := s.deps.Metrics
			if m == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"metrics history is not available; see the daemon log")
				return
			}
			// The history routes refuse while recording is off, as the plan says:
			// the charts would otherwise end at an arbitrary moment with no
			// explanation. Status and clear stay usable so it can be inspected
			// and reset.
			if history && !m.Enabled() {
				writeError(w, http.StatusConflict, errConflict,
					"metrics history is turned off (metrics.enabled); turn it on in Settings to record and view it")
				return
			}
			fn(m, w, r)
		}))
	}
	mux.Handle("GET /api/metrics/host", h(true, metricsHost))
	mux.Handle("GET /api/metrics/disks", h(true, metricsDisks))
	mux.Handle("GET /api/metrics/services", h(true, metricsServices))
	mux.Handle("GET /api/metrics/sites/{domain}", h(true, metricsSite))
	mux.Handle("GET /api/metrics/status", h(false, metricsStatus))
	mux.Handle("POST /api/metrics/clear", h(false, metricsClear))
}

// metricsRange reads ?range=1h|6h|24h|7d|30d|90d, or ?from=&to= as unix seconds.
// It answers 400 itself and reports false when the query is bad.
func metricsRange(w http.ResponseWriter, r *http.Request) (metrics.Range, bool) {
	q := r.URL.Query()
	now := time.Now().Unix()
	from, to := strings.TrimSpace(q.Get("from")), strings.TrimSpace(q.Get("to"))
	if from != "" || to != "" {
		f, ferr := strconv.ParseInt(from, 10, 64)
		t, terr := strconv.ParseInt(to, 10, 64)
		if ferr != nil || terr != nil || f < 0 || t <= f {
			writeError(w, http.StatusBadRequest, errBadRequest, "from and to must be unix seconds with to after from")
			return metrics.Range{}, false
		}
		if t > now+60 {
			t = now
		}
		return metrics.Range{From: f, To: t}, true
	}
	name := strings.TrimSpace(q.Get("range"))
	if name == "" {
		name = metricsDefaultRange
	}
	rng, ok := metrics.PresetRange(name, now)
	if !ok {
		writeError(w, http.StatusBadRequest, errBadRequest, "range must be one of 1h, 6h, 24h, 7d, 30d, 90d")
		return metrics.Range{}, false
	}
	return rng, true
}

// metricsFail maps the metrics package's errors to statuses.
func metricsFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, metrics.ErrBadRange):
		writeError(w, http.StatusBadRequest, errBadRequest, "that time range is not valid")
	case errors.Is(err, metrics.ErrUnknownSite):
		writeError(w, http.StatusNotFound, errNotFound, "no such site")
	default:
		writeError(w, http.StatusInternalServerError, errInternal, "metrics request failed; see the daemon log")
	}
}

func metricsHost(m MetricsService, w http.ResponseWriter, r *http.Request) {
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	out, err := m.Host(r.Context(), rng)
	if err != nil {
		metricsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func metricsDisks(m MetricsService, w http.ResponseWriter, r *http.Request) {
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	out, err := m.Disks(r.Context(), rng)
	if err != nil {
		metricsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func metricsServices(m MetricsService, w http.ResponseWriter, r *http.Request) {
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	out, err := m.Services(r.Context(), rng, strings.TrimSpace(r.URL.Query().Get("name")))
	if err != nil {
		metricsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func metricsSite(m MetricsService, w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if !wafmon.ValidDomain(domain) {
		writeError(w, http.StatusNotFound, errNotFound, "no such site")
		return
	}
	rng, ok := metricsRange(w, r)
	if !ok {
		return
	}
	out, err := m.Site(r.Context(), domain, rng)
	if err != nil {
		metricsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func metricsStatus(m MetricsService, w http.ResponseWriter, r *http.Request) {
	out, err := m.Status(r.Context())
	if err != nil {
		metricsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// metricsClear deletes the whole history. The audit middleware records the POST,
// so the reset is in the audit log with the actor.
func metricsClear(m MetricsService, w http.ResponseWriter, r *http.Request) {
	if err := m.Clear(r.Context()); err != nil {
		metricsFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}
