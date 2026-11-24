package httpapi

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/wafmon"
)

type fakeWAFMon struct {
	site    string
	window  time.Duration
	reqQ    wafmon.RequestQuery
	evQ     wafmon.EventQuery
	summary string
}

func (f *fakeWAFMon) Traffic(site string, w time.Duration) wafmon.Traffic {
	f.site, f.window = site, w
	return wafmon.Traffic{Series: []wafmon.Point{}, TopIPs: []wafmon.IPRow{}, TopPaths: []wafmon.PathRow{},
		TopStatuses: []wafmon.StatusRow{}, TopAgents: []wafmon.AgentRow{}, Sites: []wafmon.SiteRow{}, StepSeconds: 10}
}

func (f *fakeWAFMon) Requests(q wafmon.RequestQuery) wafmon.RequestPage {
	f.reqQ = q
	return wafmon.RequestPage{Requests: []wafmon.Request{{Seq: 9, Site: "a.test", Status: 200}}, LatestSeq: 9}
}

func (f *fakeWAFMon) Events(q wafmon.EventQuery) wafmon.EventPage {
	f.evQ = q
	return wafmon.EventPage{Events: []wafmon.Event{}, Total: 0}
}

func (f *fakeWAFMon) SourceCountsOver(window time.Duration) wafmon.SourceCounts {
	return wafmon.SourceCounts{Requests: 12, IPs: []wafmon.IPRow{
		{IP: "198.51.100.4", Requests: 9, Errors: 2},
		{IP: "10.0.0.5", Requests: 3},
	}}
}

func (f *fakeWAFMon) Matching(q wafmon.EventQuery) []wafmon.Event {
	f.evQ = q
	return []wafmon.Event{{Seq: 7, Site: "a.test", IP: "198.51.100.4", Action: wafmon.ActionBlocked, RuleID: "942100",
		Message: "SQL Injection Attack Detected via libinjection", Time: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)}}
}

func (f *fakeWAFMon) Summary(site string, w time.Duration) wafmon.Summary {
	f.summary, f.window = site, w
	return wafmon.Summary{TopRules: []wafmon.RuleRow{}, BySite: []wafmon.SiteCount{}, ByAction: map[string]int{}}
}

func newWAFMonHarness(t *testing.T) (*harness, *fakeWAFMon, session) {
	t.Helper()
	h := newHarness(t)
	fake := &fakeWAFMon{}
	h.deps.WAFMon = fake
	handler, err := New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h, fake, h.login()
}

func TestWAFMonRequiresAuthAndAnswers503WithoutMonitor(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(http.MethodGet, "/api/waf/traffic", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: %d", rec.Code)
	}
	s := h.login()
	for _, p := range []string{"/api/waf/traffic", "/api/waf/requests", "/api/waf/events", "/api/waf/events/summary"} {
		if rec := h.do(http.MethodGet, p, s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without a monitor: %d", p, rec.Code)
		}
	}
}

func TestWAFMonParameters(t *testing.T) {
	h, fake, s := newWAFMonHarness(t)
	get := func(path string, q url.Values) int {
		return h.do(http.MethodGet, path+"?"+q.Encode(), s.cookie, s.csrf, nil).Code
	}

	if got := get("/api/waf/traffic", url.Values{}); got != http.StatusOK || fake.window != 15*time.Minute || fake.site != "" {
		t.Errorf("default traffic: %d, %v, %q", got, fake.window, fake.site)
	}
	if got := get("/api/waf/traffic", url.Values{"range": {"1h"}, "site": {"A.Test"}}); got != http.StatusOK || fake.window != time.Hour || fake.site != "a.test" {
		t.Errorf("traffic 1h: %d, %v, %q", got, fake.window, fake.site)
	}
	for name, q := range map[string]url.Values{
		"range":    {"range": {"2h"}},
		"site":     {"site": {"../etc/passwd"}},
		"site dot": {"site": {"a/b"}},
	} {
		if got := get("/api/waf/traffic", q); got != http.StatusBadRequest {
			t.Errorf("traffic %s: %d", name, got)
		}
	}

	if got := get("/api/waf/requests", url.Values{"after": {"5"}, "limit": {"9999"}, "status": {"4XX"}, "ip": {"2001:DB8::1"}, "q": {"x"}}); got != http.StatusOK {
		t.Fatalf("requests: %d", got)
	}
	if q := fake.reqQ; q.After != 5 || q.Limit != 500 || q.Status != "4xx" || q.IP != "2001:db8::1" || q.Q != "x" {
		t.Errorf("request query = %+v", q)
	}
	for name, q := range map[string]url.Values{
		"limit":  {"limit": {"0"}},
		"after":  {"after": {"-1"}},
		"status": {"status": {"abc"}},
		"ip":     {"ip": {"not-an-ip"}},
	} {
		if got := get("/api/waf/requests", q); got != http.StatusBadRequest {
			t.Errorf("requests %s: %d", name, got)
		}
	}

	if got := get("/api/waf/events", url.Values{"rule": {"941100"}, "severity": {"Critical"}, "action": {"blocked"}, "before": {"12"}}); got != http.StatusOK {
		t.Fatalf("events: %d", got)
	}
	if q := fake.evQ; q.Rule != "941100" || q.Severity != "critical" || q.Action != "blocked" || q.Before != 12 || q.Limit != 100 {
		t.Errorf("event query = %+v", q)
	}
	for name, q := range map[string]url.Values{
		"rule":     {"rule": {"x'; drop"}},
		"severity": {"severity": {"loud"}},
		"action":   {"action": {"maybe"}},
	} {
		if got := get("/api/waf/events", q); got != http.StatusBadRequest {
			t.Errorf("events %s: %d", name, got)
		}
	}

	if got := get("/api/waf/events/summary", url.Values{}); got != http.StatusOK || fake.window != time.Hour {
		t.Errorf("summary default: %d, %v", got, fake.window)
	}
	if got := get("/api/waf/events/summary", url.Values{"range": {"5m"}, "site": {"a.test"}}); got != http.StatusOK || fake.window != 5*time.Minute || fake.summary != "a.test" {
		t.Errorf("summary 5m: %d, %v, %q", got, fake.window, fake.summary)
	}
}

func TestWAFMonResponseShape(t *testing.T) {
	h, _, s := newWAFMonHarness(t)
	rec := h.do(http.MethodGet, "/api/waf/requests", s.cookie, s.csrf, nil)
	var out struct {
		Requests  []map[string]any `json:"requests"`
		LatestSeq uint64           `json:"latest_seq"`
	}
	h.decode(rec, &out)
	if len(out.Requests) != 1 || out.LatestSeq != 9 || out.Requests[0]["request_id"] == nil {
		t.Errorf("body = %s", rec.Body.String())
	}
}
