package httpapi

import (
	"bytes"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/wafmon"
)

// WAFMonitor is the part of *wafmon.Collector the routes use; tests substitute a
// fake.
type WAFMonitor interface {
	Traffic(site string, window time.Duration) wafmon.Traffic
	Requests(q wafmon.RequestQuery) wafmon.RequestPage
	Events(q wafmon.EventQuery) wafmon.EventPage
	Matching(q wafmon.EventQuery) []wafmon.Event
	Summary(site string, window time.Duration) wafmon.Summary
	SourceCountsOver(window time.Duration) wafmon.SourceCounts
}

const (
	wafMonDefaultLimit = 100
	wafMonMaxLimit     = 500
	wafMonMaxQuery     = 200
)

var (
	wafRulePattern   = regexp.MustCompile(`^[0-9]{1,12}$`)
	wafStatusPattern = regexp.MustCompile(`^(?:[1-5]xx|[1-5][0-9]{2})$`)
	wafSeverities    = map[string]bool{"critical": true, "error": true, "warning": true, "notice": true, "info": true, "debug": true}
	wafRanges        = map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "1h": time.Hour}
)

// wafMonRoutes registers the traffic and WAF event routes. They are registered
// even when Deps.WAFMon is nil, so the UI gets a 503 that says why.
func (s *server) wafMonRoutes(mux *http.ServeMux) {
	h := func(fn func(WAFMonitor, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.WAFMon == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"traffic monitoring is not available; see the daemon log")
				return
			}
			fn(s.deps.WAFMon, w, r)
		}))
	}
	mux.Handle("GET /api/waf/traffic", h(s.wafTraffic))
	mux.Handle("GET /api/waf/requests", h(s.wafRequests))
	mux.Handle("GET /api/waf/events", h(s.wafEvents))
	mux.Handle("GET /api/waf/events/summary", h(s.wafEventsSummary))
	mux.Handle("GET /api/waf/events/abuseipdb", h(s.wafEventsAbuseIPDB))
}

// wafParams reads query parameters and remembers the first bad one.
type wafParams struct {
	w    http.ResponseWriter
	q    map[string][]string
	done bool
}

func newWAFParams(w http.ResponseWriter, r *http.Request) *wafParams {
	return &wafParams{w: w, q: r.URL.Query()}
}

func (p *wafParams) get(name string) string {
	if v := p.q[name]; len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

// fail answers 400 once and makes every later read return its zero value.
func (p *wafParams) fail(field, detail string) {
	if p.done {
		return
	}
	p.done = true
	writeJSON(p.w, http.StatusBadRequest, errorEnvelope{Error: errBadRequest, Detail: detail, Field: field})
}

func (p *wafParams) site() string {
	v := strings.ToLower(p.get("site"))
	if v != "" && !wafmon.ValidDomain(v) {
		p.fail("site", "site is not a domain name")
		return ""
	}
	return v
}

func (p *wafParams) window() time.Duration {
	v := p.get("range")
	if v == "" {
		return 15 * time.Minute
	}
	d, ok := wafRanges[v]
	if !ok {
		p.fail("range", "range must be 5m, 15m or 1h")
	}
	return d
}

func (p *wafParams) limit() int {
	v := p.get("limit")
	if v == "" {
		return wafMonDefaultLimit
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		p.fail("limit", "limit must be a positive number")
		return 0
	}
	return min(n, wafMonMaxLimit)
}

func (p *wafParams) seq(name string) uint64 {
	v := p.get(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		p.fail(name, name+" must be a sequence number")
	}
	return n
}

func (p *wafParams) ip() string {
	v := p.get("ip")
	if v == "" {
		return ""
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		p.fail("ip", "ip is not an address")
		return ""
	}
	return a.String()
}

func (p *wafParams) text(name string) string {
	v := p.get(name)
	if len(v) > wafMonMaxQuery {
		p.fail(name, name+" is too long")
		return ""
	}
	return v
}

func (s *server) wafTraffic(m WAFMonitor, w http.ResponseWriter, r *http.Request) {
	p := newWAFParams(w, r)
	site, window := p.site(), p.window()
	if p.done {
		return
	}
	writeJSON(w, http.StatusOK, m.Traffic(site, window))
}

func (s *server) wafRequests(m WAFMonitor, w http.ResponseWriter, r *http.Request) {
	p := newWAFParams(w, r)
	q := wafmon.RequestQuery{Site: p.site(), After: p.seq("after"), Limit: p.limit(), IP: p.ip(), Q: p.text("q")}
	if st := p.get("status"); st != "" {
		if !wafStatusPattern.MatchString(strings.ToLower(st)) {
			p.fail("status", "status must be a code such as 404 or a class such as 4xx")
		}
		q.Status = strings.ToLower(st)
	}
	if p.done {
		return
	}
	writeJSON(w, http.StatusOK, m.Requests(q))
}

func (s *server) wafEvents(m WAFMonitor, w http.ResponseWriter, r *http.Request) {
	p := newWAFParams(w, r)
	q := wafEventFilters(p)
	q.Limit = p.limit()
	q.Before = p.seq("before")
	if p.done {
		return
	}
	writeJSON(w, http.StatusOK, m.Events(q))
}

// wafEventsAbuseIPDB streams the events that match the filters as an AbuseIPDB
// bulk-report CSV. It takes the same filters as the events list and no paging: the
// export is the whole filtered set, so the file matches what the table would show
// if it were not paged.
func (s *server) wafEventsAbuseIPDB(m WAFMonitor, w http.ResponseWriter, r *http.Request) {
	p := newWAFParams(w, r)
	q := wafEventFilters(p)
	if p.done {
		return
	}
	// Built in memory first, so a failure never leaves a 200 and a half file behind
	// the error the browser would otherwise save as the download.
	var buf bytes.Buffer
	if err := wafmon.WriteAbuseIPDB(&buf, m.Matching(q)); err != nil {
		s.writeInternal(w, r, err)
		return
	}
	name := "waf-events-abuseipdb-" + time.Now().UTC().Format("20060102") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// wafEventFilters reads the filters the events list and its export share. Paging
// is left to the caller: the list adds limit and before, the export has neither.
func wafEventFilters(p *wafParams) wafmon.EventQuery {
	q := wafmon.EventQuery{Site: p.site(), IP: p.ip(), Q: p.text("q")}
	if v := p.get("rule"); v != "" {
		if !wafRulePattern.MatchString(v) {
			p.fail("rule", "rule must be a numeric rule id")
		}
		q.Rule = v
	}
	if v := strings.ToLower(p.get("severity")); v != "" {
		if !wafSeverities[v] {
			p.fail("severity", "severity must be critical, error, warning, notice, info or debug")
		}
		q.Severity = v
	}
	if v := p.get("action"); v != "" {
		if v != wafmon.ActionBlocked && v != wafmon.ActionDetected {
			p.fail("action", "action must be blocked or detected")
		}
		q.Action = v
	}
	return q
}

func (s *server) wafEventsSummary(m WAFMonitor, w http.ResponseWriter, r *http.Request) {
	p := newWAFParams(w, r)
	site := p.site()
	window := time.Hour
	if p.get("range") != "" {
		window = p.window()
	}
	if p.done {
		return
	}
	writeJSON(w, http.StatusOK, m.Summary(site, window))
}
