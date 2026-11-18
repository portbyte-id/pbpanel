package wafmon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAccess(t *testing.T) {
	const ts = `[18/Nov/2026:10:00:01 +0000]`
	tests := []struct {
		name   string
		line   string
		ok     bool
		want   Request
		wantMS bool
	}{
		{
			name: "pbpanel",
			line: `203.0.113.7 - - ` + ts + ` "GET /a?b=1 HTTP/1.1" 200 512 "https://x.test/" "curl/8" rt=0.123 rid=abc123`,
			ok:   true, wantMS: true,
			want: Request{IP: "203.0.113.7", Method: "GET", Path: "/a?b=1", Status: 200, Bytes: 512, Referer: "https://x.test/", Agent: "curl/8", MS: 123, RequestID: "abc123"},
		},
		{
			name: "combined without rt and rid",
			line: `203.0.113.7 - bob ` + ts + ` "POST /login HTTP/2.0" 403 0 "-" "Mozilla/5.0"`,
			ok:   true,
			want: Request{IP: "203.0.113.7", Method: "POST", Path: "/login", Status: 403, Agent: "Mozilla/5.0", Referer: "-"},
		},
		{
			name: "ipv6",
			line: `2001:db8::1 - - ` + ts + ` "GET / HTTP/1.1" 304 0 "-" "-" rt=0.000 rid=r`,
			ok:   true, wantMS: true,
			want: Request{IP: "2001:db8::1", Method: "GET", Path: "/", Status: 304, Referer: "-", Agent: "-", RequestID: "r"},
		},
		{
			name: "escaped quotes in the agent",
			line: `10.0.0.1 - - ` + ts + ` "GET / HTTP/1.1" 200 1 "-" "a \"quoted\" agent" rt=0.5 rid=z`,
			ok:   true, wantMS: true,
			want: Request{IP: "10.0.0.1", Method: "GET", Path: "/", Status: 200, Bytes: 1, Referer: "-", Agent: `a "quoted" agent`, MS: 500, RequestID: "z"},
		},
		{
			name: "binary request line keeps the raw text",
			line: `10.0.0.1 - - ` + ts + ` "\x16\x03\x01" 400 150 "-" "-"`,
			ok:   true,
			want: Request{IP: "10.0.0.1", Path: `\x16\x03\x01`, Status: 400, Bytes: 150, Referer: "-", Agent: "-"},
		},
		{name: "empty", line: ``},
		{name: "garbage", line: `hello world`},
		{name: "truncated", line: `10.0.0.1 - - ` + ts + ` "GET / HTT`},
		{name: "bad status", line: `10.0.0.1 - - ` + ts + ` "GET / HTTP/1.1" abc 1 "-" "-"`},
		{name: "unterminated agent", line: `10.0.0.1 - - ` + ts + ` "GET / HTTP/1.1" 200 1 "-" "oops`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseAccess(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			if got.Time.IsZero() {
				t.Error("time was not parsed")
			}
			got.Time = time.Time{}
			if got.hasMS != tc.wantMS {
				t.Errorf("hasMS = %v, want %v", got.hasMS, tc.wantMS)
			}
			got.hasMS = false
			if got.IP != tc.want.IP || got.Method != tc.want.Method || got.Path != tc.want.Path ||
				got.Status != tc.want.Status || got.Bytes != tc.want.Bytes || got.Referer != tc.want.Referer ||
				got.Agent != tc.want.Agent || got.MS != tc.want.MS || got.RequestID != tc.want.RequestID {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseAudit(t *testing.T) {
	blocked := `{"transaction":{"client_ip":"198.51.100.4","time_stamp":"Wed Nov 18 10:00:02 2026","unique_id":"rid1",` +
		`"request":{"method":"GET","http_version":1.1,"uri":"/?q=<script>"},"response":{"http_code":403},` +
		`"messages":[` +
		`{"message":"XSS Attack","details":{"ruleId":"941100","severity":"2","data":"x","tags":["attack-xss"],"file":"/crs/941.conf","lineNumber":"55"}},` +
		`{"message":"SQLi","details":{"ruleId":942100,"severity":"CRITICAL","tags":null,"lineNumber":9}},` +
		`{"message":"Inbound Anomaly Score Exceeded","details":{"ruleId":"949110","severity":"0"}}]}}`

	e, ok := ParseAudit(blocked)
	if !ok {
		t.Fatal("not parsed")
	}
	if e.Action != ActionBlocked || e.Status != 403 || e.RequestID != "rid1" || e.IP != "198.51.100.4" || e.URI != "/?q=<script>" {
		t.Errorf("event = %+v", e)
	}
	// The anomaly rule is the most severe (0) but the row shows a specific one;
	// of the two specific rules the first of equal severity wins.
	if e.RuleID != "941100" || e.Severity != "critical" || e.Message != "XSS Attack" {
		t.Errorf("row rule = %s %s %q", e.RuleID, e.Severity, e.Message)
	}
	if len(e.Matches) != 3 || e.Matches[1].RuleID != "942100" || e.Matches[1].Line != 9 || e.Matches[1].Tags == nil || e.Matches[0].Line != 55 {
		t.Errorf("matches = %+v", e.Matches)
	}
	if e.Time.IsZero() || e.Time.Year() != 2026 {
		t.Errorf("time = %v", e.Time)
	}

	tests := []struct {
		name, line string
		ok         bool
		action     string
		ruleID     string
	}{
		{"detection only stays detected", `{"transaction":{"response":{"http_code":200},"messages":[{"message":"m","details":{"ruleId":"949110","severity":"2"}}]}}`, true, ActionDetected, "949110"},
		{"app 404 beside a match is not a block", `{"transaction":{"response":{"http_code":"404"},"messages":[{"message":"m","details":{"ruleId":"920350","severity":"4"}}]}}`, true, ActionDetected, "920350"},
		{"403 with a match is a block", `{"transaction":{"response":{"http_code":403},"messages":[{"message":"m","details":{"ruleId":"1001","severity":"3"}}]}}`, true, ActionBlocked, "1001"},
		{"no messages", `{"transaction":{"client_ip":"1.1.1.1"}}`, true, ActionDetected, ""},
		{"not json", `nope`, false, "", ""},
		{"not a transaction", `{"other":1}`, false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := ParseAudit(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v", ok)
			}
			if ok && (e.Action != tc.action || e.RuleID != tc.ruleID) {
				t.Errorf("action %s rule %s, want %s %s", e.Action, e.RuleID, tc.action, tc.ruleID)
			}
		})
	}
}

// fixture is a collector over a temp directory with a clock the test moves.
type fixture struct {
	t   *testing.T
	dir string
	c   *Collector
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), now: time.Date(2026, 11, 18, 10, 0, 30, 0, time.UTC)}
	f.c = New(Options{Dir: f.dir, Now: func() time.Time { return f.now }})
	return f
}

func (f *fixture) path(name string) string { return filepath.Join(f.dir, name) }

func (f *fixture) append(name string, lines ...string) {
	f.t.Helper()
	fh, err := os.OpenFile(f.path(name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	for _, l := range lines {
		if _, err := fh.WriteString(l + "\n"); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) line(ip, path string, status int, at time.Time) string {
	return fmt.Sprintf(`%s - - [%s] "GET %s HTTP/1.1" %d 100 "-" "ua" rt=0.100 rid=r`, ip, at.Format(accessTimeLayout), path, status)
}

func TestCollectorBucketsRingsAndTops(t *testing.T) {
	f := newFixture(t)
	f.append("a.test.access.log",
		f.line("1.1.1.1", "/x?token=1", 200, f.now.Add(-5*time.Second)),
		f.line("1.1.1.1", "/x?token=2", 404, f.now.Add(-4*time.Second)),
		f.line("2.2.2.2", "/y", 500, f.now.Add(-30*time.Second)),
		"garbage line",
	)
	f.append("b.test.access.log", f.line("3.3.3.3", "/z", 200, f.now.Add(-2*time.Second)))
	f.append("a.test.waf-audit.log",
		`{"transaction":{"client_ip":"1.1.1.1","time_stamp":"Wed Nov 18 10:00:27 2026","unique_id":"r","request":{"method":"GET","uri":"/x"},"response":{"http_code":403},"messages":[{"message":"m","details":{"ruleId":"941100","severity":"2"}}]}}`,
	)
	f.c.Poll()

	tr := f.c.Traffic("", 5*time.Minute)
	if tr.Totals.Requests != 4 || tr.Totals.S2xx != 2 || tr.Totals.S4xx != 1 || tr.Totals.S5xx != 1 {
		t.Errorf("totals = %+v", tr.Totals)
	}
	if tr.Totals.WAFEvents != 1 || tr.Totals.AvgMS != 100 || tr.Totals.Bytes != 400 {
		t.Errorf("totals = %+v", tr.Totals)
	}
	if len(tr.Series) != 30 || tr.StepSeconds != 10 {
		t.Errorf("series has %d points", len(tr.Series))
	}
	if tr.TopIPs[0].IP != "1.1.1.1" || tr.TopIPs[0].Requests != 2 || tr.TopIPs[0].Errors != 1 {
		t.Errorf("top ips = %+v", tr.TopIPs)
	}
	// Query strings are dropped from the path top list.
	if tr.TopPaths[0].Path != "/x" || tr.TopPaths[0].Requests != 2 {
		t.Errorf("top paths = %+v", tr.TopPaths)
	}
	if len(tr.Sites) != 2 || tr.Sites[0].Domain != "a.test" || tr.Sites[0].WAFEvents != 1 || tr.Sites[0].Errors != 2 {
		t.Errorf("sites = %+v", tr.Sites)
	}
	one := f.c.Traffic("b.test", time.Hour)
	if one.Totals.Requests != 1 || len(one.Series) != 360 {
		t.Errorf("b.test totals = %+v, %d points", one.Totals, len(one.Series))
	}
	empty := f.c.Traffic("nope.test", 5*time.Minute)
	if empty.Totals.Requests != 0 || empty.TopIPs == nil || empty.Sites == nil || empty.Series == nil {
		t.Errorf("empty traffic = %+v", empty)
	}

	reqs := f.c.Requests(RequestQuery{Limit: 10})
	if len(reqs.Requests) != 4 || reqs.LatestSeq != 4 || reqs.Requests[0].Seq != 4 {
		t.Errorf("requests = %+v", reqs)
	}
	if got := f.c.Requests(RequestQuery{Limit: 10, After: 3}); len(got.Requests) != 1 {
		t.Errorf("after 3 = %d requests", len(got.Requests))
	}
	if got := f.c.Requests(RequestQuery{Limit: 10, Status: "4xx"}); len(got.Requests) != 1 || got.Requests[0].Status != 404 {
		t.Errorf("4xx = %+v", got.Requests)
	}
	if got := f.c.Requests(RequestQuery{Limit: 10, Site: "b.test"}); len(got.Requests) != 1 {
		t.Errorf("site filter = %d", len(got.Requests))
	}
	if got := f.c.Requests(RequestQuery{Limit: 1}); len(got.Requests) != 1 {
		t.Errorf("limit = %d", len(got.Requests))
	}

	ev := f.c.Events(EventQuery{Limit: 10})
	if ev.Total != 1 || len(ev.Events) != 1 || ev.Events[0].Site != "a.test" || ev.NextBefore != 0 {
		t.Errorf("events = %+v", ev)
	}
	sum := f.c.Summary("", time.Hour)
	if sum.ByAction[ActionBlocked] != 1 || len(sum.TopRules) != 1 || sum.TopRules[0].RuleID != "941100" || sum.BySite[0].Domain != "a.test" {
		t.Errorf("summary = %+v", sum)
	}

	// Only what is appended afterwards is read, and a partial line waits for its
	// newline.
	fh, err := os.OpenFile(f.path("b.test.access.log"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	whole := f.line("4.4.4.4", "/p", 200, f.now)
	_, _ = fh.WriteString(whole[:20])
	_ = fh.Close()
	f.c.Poll()
	if f.c.Requests(RequestQuery{Limit: 10}).LatestSeq != 4 {
		t.Error("a partial line was ingested")
	}
	fh, _ = os.OpenFile(f.path("b.test.access.log"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = fh.WriteString(whole[20:] + "\n")
	_ = fh.Close()
	f.c.Poll()
	if got := f.c.Requests(RequestQuery{Limit: 10, IP: "4.4.4.4"}); len(got.Requests) != 1 {
		t.Errorf("completed line: %d requests", len(got.Requests))
	}
}

func TestCollectorRotationTruncationAndRemoval(t *testing.T) {
	f := newFixture(t)
	f.append("a.test.access.log", f.line("1.1.1.1", "/1", 200, f.now), f.line("1.1.1.1", "/2", 200, f.now))
	f.c.Poll()

	// Rotation: the old file is renamed away and a new live file appears.
	if err := os.Rename(f.path("a.test.access.log"), f.path("a.test.access.log.1")); err != nil {
		t.Fatal(err)
	}
	f.append("a.test.access.log", f.line("2.2.2.2", "/3", 200, f.now))
	f.c.Poll()
	if got := f.c.Requests(RequestQuery{Limit: 10}); len(got.Requests) != 3 || got.Requests[0].Path != "/3" {
		t.Fatalf("after rotation: %+v", got.Requests)
	}

	// Truncation in place: the file is shorter than the offset.
	if err := os.WriteFile(f.path("a.test.access.log"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	f.append("a.test.access.log", f.line("::1", "/4", 200, f.now))
	f.c.Poll()
	if got := f.c.Requests(RequestQuery{Limit: 1}); got.Requests[0].Path != "/4" {
		t.Fatalf("after truncation: %+v", got.Requests)
	}

	// The files vanish and the site's counters go with them.
	_ = os.Remove(f.path("a.test.access.log"))
	f.c.Poll()
	if tr := f.c.Traffic("", 5*time.Minute); tr.Totals.Requests != 0 || len(tr.Sites) != 0 {
		t.Errorf("site was kept: %+v", tr.Totals)
	}
}

func TestCollectorBackfillAlignsToLineStart(t *testing.T) {
	f := newFixture(t)
	var b strings.Builder
	n := 0
	for b.Len() < accessBackfill+4096 {
		fmt.Fprintln(&b, f.line("9.9.9.9", fmt.Sprintf("/%d", n), 200, f.now))
		n++
	}
	if err := os.WriteFile(f.path("big.test.access.log"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f.c.Poll()
	got := f.c.Requests(RequestQuery{Limit: 500})
	if len(got.Requests) == 0 {
		t.Fatal("nothing backfilled")
	}
	// The newest line is the file's last, and no half line was parsed (every
	// request has an intact path).
	if want := fmt.Sprintf("/%d", n-1); got.Requests[0].Path != want {
		t.Errorf("newest = %s, want %s", got.Requests[0].Path, want)
	}
	if tr := f.c.Traffic("", 5*time.Minute); tr.Totals.Requests > accessBackfill/60 || tr.Totals.Requests >= n {
		t.Errorf("backfilled %d requests, more than the last MiB holds", tr.Totals.Requests)
	}
}

func TestCollectorBoundsAndOverlongLines(t *testing.T) {
	f := newFixture(t)
	var lines []string
	for i := range RingSize + 50 {
		lines = append(lines, f.line(fmt.Sprintf("10.0.%d.%d", i/250, i%250), fmt.Sprintf("/p%d", i), 200, f.now))
	}
	lines = append(lines, `1.1.1.1 - - [`+f.now.Format(accessTimeLayout)+`] "GET /`+strings.Repeat("a", MaxLine)+` HTTP/1.1" 200 1 "-" "-"`)
	f.append("a.test.access.log", lines...)
	f.c.Poll()

	if got := f.c.Requests(RequestQuery{Limit: wafMaxForTest}); len(got.Requests) != RingSize {
		t.Errorf("ring holds %d requests, want %d", len(got.Requests), RingSize)
	}
	tr := f.c.Traffic("", time.Hour)
	if tr.Totals.Requests != RingSize+50 {
		t.Errorf("totals count %d, want every request", tr.Totals.Requests)
	}
	if len(tr.TopIPs) > topN {
		t.Errorf("top ips has %d rows", len(tr.TopIPs))
	}
	f.c.mu.RLock()
	for _, b := range f.c.sites["a.test"].buckets {
		if len(b.ips) > keysPerBucket || len(b.paths) > keysPerBucket {
			t.Errorf("bucket keys not capped: %d ips, %d paths", len(b.ips), len(b.paths))
		}
	}
	f.c.mu.RUnlock()
}

// wafMaxForTest is larger than the ring so a query can return all of it.
const wafMaxForTest = RingSize * 2

func TestCollectorEventsPaging(t *testing.T) {
	f := newFixture(t)
	var lines []string
	for i := range 5 {
		lines = append(lines, fmt.Sprintf(`{"transaction":{"client_ip":"1.1.1.%d","time_stamp":"Wed Nov 18 10:00:0%d 2026","request":{"uri":"/u%d"},"response":{"http_code":403},"messages":[{"message":"m%d","details":{"ruleId":"94110%d","severity":"3"}}]}}`, i, i, i, i, i))
	}
	f.append("a.test.waf-audit.log", lines...)
	f.c.Poll()

	p1 := f.c.Events(EventQuery{Limit: 2})
	if p1.Total != 5 || len(p1.Events) != 2 || p1.Events[0].Seq != 5 || p1.NextBefore != 4 {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2 := f.c.Events(EventQuery{Limit: 2, Before: p1.NextBefore})
	if len(p2.Events) != 2 || p2.Events[0].Seq != 3 || p2.NextBefore != 2 || p2.Total != 5 {
		t.Fatalf("page 2 = %+v", p2)
	}
	p3 := f.c.Events(EventQuery{Limit: 2, Before: p2.NextBefore})
	if len(p3.Events) != 1 || p3.NextBefore != 0 {
		t.Fatalf("page 3 = %+v", p3)
	}
	if got := f.c.Events(EventQuery{Limit: 10, Rule: "941102"}); len(got.Events) != 1 || got.Events[0].IP != "1.1.1.2" {
		t.Errorf("rule filter = %+v", got)
	}
	if got := f.c.Events(EventQuery{Limit: 10, Q: "u3"}); len(got.Events) != 1 {
		t.Errorf("q filter = %+v", got)
	}
	if got := f.c.Events(EventQuery{Limit: 10, Severity: "error", Action: ActionBlocked}); got.Total != 5 {
		t.Errorf("severity+action = %d", got.Total)
	}
	if got := f.c.Events(EventQuery{Limit: 10, Severity: "critical"}); got.Total != 0 {
		t.Errorf("severity = %d", got.Total)
	}
}
