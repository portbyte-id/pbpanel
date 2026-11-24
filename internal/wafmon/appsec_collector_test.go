package wafmon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appsecFixture is a collector over a temp site-log directory and a temp agent
// directory, so both sources can be driven.
type appsecFixture struct {
	*fixture
	appsecDir string
}

func newAppsecFixture(t *testing.T) *appsecFixture {
	t.Helper()
	f := newFixture(t)
	dir := filepath.Join(f.dir, "nano_agent")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.c.appsecDir = dir
	return &appsecFixture{fixture: f, appsecDir: dir}
}

// writeEvent appends JSON lines to a file in the agent directory, creating it
// with a modification time the test can order.
func (f *appsecFixture) writeEvent(name string, mod time.Time, lines ...string) {
	f.t.Helper()
	p := filepath.Join(f.appsecDir, name)
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, l := range lines {
		if _, err := fh.WriteString(l + "\n"); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := fh.Close(); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		f.t.Fatal(err)
	}
}

// event builds one agent event. The agent writes local time with no zone, so the
// instant is formatted in the local zone the parser reads it back in.
func (f *appsecFixture) event(host, path, ip, action string, at time.Time) string {
	return `{"eventTime":"` + at.In(time.Local).Format(appsecTimeLayout) + `","eventSeverity":"Critical",` +
		`"eventData":{"eventReferenceId":"rid","eventConfidence":"Very High","sourceIP":"` + ip + `",` +
		`"httpHostName":"` + host + `","httpMethod":"GET","httpUriPath":"` + path + `",` +
		`"securityAction":"` + action + `","waapIncidentType":"SQL Injection","matchedParameter":"id"}}`
}

// TestAppsecEventsRouteToTheirSite proves the agent's single log feeds the same
// per-site view the ModSecurity path feeds: a site's counter rises for the event
// that names it, another site's does not, and an event naming no site stays on
// the events list with no site.
func TestAppsecEventsRouteToTheirSite(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(appsecLogPrefix, f.now.Add(-time.Minute),
		f.event("a.test", "/x", "198.51.100.4", "Prevent", f.now.Add(-30*time.Second)),
		f.event("b.test", "/y", "198.51.100.5", "Detect", f.now.Add(-20*time.Second)),
		f.event("not a host", "/z", "198.51.100.6", "Prevent", f.now.Add(-10*time.Second)),
	)
	f.c.Poll()

	ev := f.c.Events(EventQuery{Limit: 10})
	if ev.Total != 3 {
		t.Fatalf("events = %d, want the three real ones", ev.Total)
	}
	// Newest first: the unmatched-host event, then b.test, then a.test.
	if ev.Events[0].Site != "" || ev.Events[1].Site != "b.test" || ev.Events[2].Site != "a.test" {
		t.Errorf("sites = %q %q %q", ev.Events[0].Site, ev.Events[1].Site, ev.Events[2].Site)
	}
	for _, e := range ev.Events {
		if e.Engine != EngineOpenAppsec {
			t.Errorf("event %s has engine %q", e.URI, e.Engine)
		}
	}

	// The per-site counter counts only the events that name the site, and
	// b.test's Detect event is a detection, not a block.
	if tr := f.c.Traffic("a.test", 5*time.Minute); tr.Totals.WAFEvents != 1 {
		t.Errorf("a.test events = %d, want 1", tr.Totals.WAFEvents)
	}
	if tr := f.c.Traffic("b.test", 5*time.Minute); tr.Totals.WAFEvents != 1 {
		t.Errorf("b.test events = %d, want 1", tr.Totals.WAFEvents)
	}
	// The unmatched host has no site to count under, but the event is not lost.
	if tr := f.c.Traffic("", 5*time.Minute); tr.Totals.WAFEvents != 2 {
		t.Errorf("all-site events = %d, want the two routed events", tr.Totals.WAFEvents)
	}
	sum := f.c.Summary("", time.Hour)
	if sum.ByAction[ActionBlocked] != 2 || sum.ByAction[ActionDetected] != 1 {
		t.Errorf("by action = %+v", sum.ByAction)
	}
}

// TestAppsecMissingDirectoryIsNotAnError proves a host without the agent (no log
// directory at all) is a monitor with no open-appsec events, not a broken one.
func TestAppsecMissingDirectoryIsNotAnError(t *testing.T) {
	f := newFixture(t)
	f.c.appsecDir = filepath.Join(f.dir, "nano_agent")
	// The site logs still work.
	f.append("a.test.access.log", f.line("1.1.1.1", "/x", 200, f.now))

	for range 2 {
		f.c.Poll()
	}
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 0 {
		t.Errorf("events = %d, want none", got.Total)
	}
	if tr := f.c.Traffic("", 5*time.Minute); tr.Totals.Requests != 1 {
		t.Errorf("the site logs stopped being read: %+v", tr.Totals)
	}
}

// TestAppsecFollowsTheRotatedLog proves the agent's own rotation is followed
// without counting an event twice. The vendor rotates by renumbering, so the
// live file becomes `…handler.log1` and a fresh `…handler.log` is written: the
// newest file is followed, and the rotated one is never read again.
func TestAppsecFollowsTheRotatedLog(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(appsecLogPrefix, f.now.Add(-3*time.Minute),
		f.event("a.test", "/1", "198.51.100.4", "Prevent", f.now.Add(-2*time.Minute)))
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 1 {
		t.Fatalf("events = %d, want 1", got.Total)
	}

	// Rotate: the live file is renamed to .log1 and a new live file appears.
	rotated := appsecLogPrefix + "1"
	if err := os.Rename(filepath.Join(f.appsecDir, appsecLogPrefix), filepath.Join(f.appsecDir, rotated)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(f.appsecDir, rotated), f.now.Add(-time.Minute), f.now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.writeEvent(appsecLogPrefix, f.now,
		f.event("a.test", "/2", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()

	got := f.c.Events(EventQuery{Limit: 10})
	if got.Total != 2 {
		t.Fatalf("after rotation: %d events, want 2 (the first not counted twice)", got.Total)
	}
	if got.Events[0].URI != "/2" {
		t.Errorf("newest = %q, want /2", got.Events[0].URI)
	}

	// The rotated file is not re-read even though its modification time is newer
	// than the file the tail knew, because only the newest file is followed.
	f.writeEvent(rotated, f.now.Add(time.Minute),
		f.event("a.test", "/stale", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 2 {
		t.Errorf("a rotated sibling was read: %d events", got.Total)
	}
}

// TestAppsecIgnoresDebugFiles proves `…handler.dbg` is not an event log: the
// agent writes its own debug output there, and none of it is an attack.
func TestAppsecIgnoresDebugFiles(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(appsecLogPrefix+".dbg", f.now,
		`{"eventTime":"2026-11-24T18:01:47.380","eventName":"Web Request","eventData":{"securityAction":"Prevent","waapIncidentType":"SQL Injection","httpHostName":"a.test"}}`)
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 0 {
		t.Errorf("a debug file was read: %d events", got.Total)
	}
}

// TestAppsecIgnoresAgentHousekeeping proves the agent's non-event records in its
// live log never reach the events list or the counters.
func TestAppsecIgnoresAgentHousekeeping(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(appsecLogPrefix, f.now,
		`{"eventTime":"2026-11-24T18:00:00.000","eventName":"Policy load","eventData":{"securityAction":""}}`,
		`{"eventTime":"2026-11-24T18:00:01.000","eventName":"Service start"}`,
		"not json at all",
	)
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 0 {
		t.Errorf("housekeeping was ingested: %d events", got.Total)
	}
	if tr := f.c.Traffic("a.test", time.Hour); tr.Totals.WAFEvents != 0 {
		t.Errorf("housekeeping counted: %+v", tr.Totals)
	}
}

// TestAppsecDefaultDirectory proves an empty AppsecDir falls back to the agent's
// documented location, so the daemon does not have to know it.
func TestAppsecDefaultDirectory(t *testing.T) {
	c := New(Options{Dir: t.TempDir()})
	if c.appsecDir != DefaultAppsecDir {
		t.Errorf("appsec dir = %q, want %q", c.appsecDir, DefaultAppsecDir)
	}
}

// TestAppsecBothEnginesShareTheEventsList proves the two parsers' output lands in
// one ring, each tagged with its own engine, so the page and the filters see a
// single stream.
func TestAppsecBothEnginesShareTheEventsList(t *testing.T) {
	f := newAppsecFixture(t)
	f.append("a.test.waf-audit.log",
		`{"transaction":{"client_ip":"203.0.113.7","time_stamp":"Wed Nov 18 10:00:27 2026","request":{"uri":"/m"},"response":{"http_code":403},"messages":[{"message":"m","details":{"ruleId":"941100","severity":"2"}}]}}`)
	f.writeEvent(appsecLogPrefix, f.now,
		f.event("a.test", "/o", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()

	got := f.c.Events(EventQuery{Limit: 10, Site: "a.test"})
	if got.Total != 2 {
		t.Fatalf("events = %d, want both engines", got.Total)
	}
	engines := map[string]bool{}
	for _, e := range got.Events {
		engines[e.Engine] = true
	}
	if !engines[EngineModSecurity] || !engines[EngineOpenAppsec] {
		t.Errorf("engines = %v, want both tagged", engines)
	}
}

// TestAppsecWindowedCounters proves an event older than the retention horizon is
// still listed but does not inflate a bucket that has already been trimmed.
func TestAppsecWindowedCounters(t *testing.T) {
	f := newAppsecFixture(t)
	old := f.now.Add(-2 * Retention)
	f.writeEvent(appsecLogPrefix, f.now.Add(-time.Minute),
		f.event("a.test", "/old", "198.51.100.4", "Prevent", old),
		f.event("a.test", "/new", "198.51.100.4", "Prevent", f.now),
	)
	f.c.Poll()
	if tr := f.c.Traffic("a.test", time.Hour); tr.Totals.WAFEvents != 1 {
		t.Errorf("counted %d events in the hour, want only the recent one", tr.Totals.WAFEvents)
	}
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 2 {
		t.Errorf("listed %d events, want both", got.Total)
	}
}

// TestAppsecLogGrows proves a line appended after the first poll is read and a
// partial line waits for its newline, the way the site logs do.
func TestAppsecLogGrows(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(appsecLogPrefix, f.now, f.event("a.test", "/1", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()

	whole := f.event("a.test", "/2", "198.51.100.4", "Prevent", f.now)
	fh, err := os.OpenFile(filepath.Join(f.appsecDir, appsecLogPrefix), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(whole[:40]); err != nil {
		t.Fatal(err)
	}
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 1 {
		t.Errorf("a partial line was ingested: %d events", got.Total)
	}
	if _, err := fh.WriteString(whole[40:] + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 2 {
		t.Errorf("the completed line was not read: %d events", got.Total)
	}
}

// TestAppsecFixtureLinesAreTheRealOnes keeps the fixture file honest: every
// captured line is an event the parser accepts.
func TestAppsecFixtureLinesAreTheRealOnes(t *testing.T) {
	b, err := os.ReadFile("testdata/appsec-real.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if _, ok := ParseOpenAppsec(l); !ok {
			t.Errorf("captured line not parsed: %q", l)
		}
	}
}
