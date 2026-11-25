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

// workerLog is one worker's log name, as the agent actually writes it: the bare
// name plus the worker's id. A real host has NO bare
// `cp-nano-http-transaction-handler.log` (step 4 findings, section E), so every
// test here writes numbered files, and TestAppsecBareNameAloneIsNotRead guards
// against a reader that goes back to the bare path.
func workerLog(n int) string { return appsecLogPrefix + itoa(n) }

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

// TestAppsecBareNameAloneIsNotRead is the regression test for the defect this
// file exists for: a real host has no bare `cp-nano-http-transaction-handler.log`
// on disk, so a reader that opens the bare path finds nothing, forever, while
// every other test still passes. The bare name must yield no events; the
// numbered set must yield them.
func TestAppsecBareNameAloneIsNotRead(t *testing.T) {
	f := newAppsecFixture(t)
	ev := f.event("a.test", "/x", "198.51.100.4", "Prevent", f.now)

	f.writeEvent(appsecLogPrefix, f.now, ev)
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 0 {
		t.Fatalf("a bare-named file was read: %d events; the bare name never exists on a real host", got.Total)
	}

	// The same event under a worker's numbered name is read.
	f.writeEvent(workerLog(1), f.now, ev)
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 1 {
		t.Fatalf("the numbered file was not read: %d events", got.Total)
	}
}

// TestAppsecReadsEveryWorkerLog proves all the workers' files are read: each
// worker writes its own log, so one file is only a fraction of the traffic.
func TestAppsecReadsEveryWorkerLog(t *testing.T) {
	f := newAppsecFixture(t)
	for n := 1; n <= 4; n++ {
		f.writeEvent(workerLog(n), f.now,
			f.event("a.test", "/w"+itoa(n), "198.51.100."+itoa(n), "Prevent", f.now))
	}
	f.c.Poll()
	got := f.c.Events(EventQuery{Limit: 10})
	if got.Total != 4 {
		t.Fatalf("events = %d, want one from each of the four workers", got.Total)
	}
	if tr := f.c.Traffic("a.test", 5*time.Minute); tr.Totals.WAFEvents != 4 {
		t.Errorf("a.test events = %d, want 4", tr.Totals.WAFEvents)
	}
}

// TestAppsecOrdersByEventTimeNotFileName proves the ring is ordered by each
// event's own eventTime. The numbering is the vendor's rotation scheme, so a
// lower-numbered file is not necessarily older; here log4 holds the oldest event
// and log1 the newest, and the newest must still come first.
func TestAppsecOrdersByEventTimeNotFileName(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(workerLog(1), f.now, f.event("a.test", "/newest", "198.51.100.1", "Prevent", f.now))
	f.writeEvent(workerLog(4), f.now, f.event("a.test", "/oldest", "198.51.100.4", "Prevent", f.now.Add(-time.Hour)))
	f.writeEvent(workerLog(2), f.now, f.event("a.test", "/middle", "198.51.100.2", "Prevent", f.now.Add(-30*time.Minute)))
	f.c.Poll()

	got := f.c.Events(EventQuery{Limit: 10})
	if got.Total != 3 {
		t.Fatalf("events = %d, want 3", got.Total)
	}
	want := []string{"/newest", "/middle", "/oldest"}
	for i, w := range want {
		if got.Events[i].URI != w {
			t.Errorf("position %d = %q, want %q (ordered by eventTime, not file name)", i, got.Events[i].URI, w)
		}
	}
	// A seq must follow the same order, since paging reads it.
	if got.Events[0].Seq <= got.Events[1].Seq || got.Events[1].Seq <= got.Events[2].Seq {
		t.Errorf("seqs out of order: %d %d %d", got.Events[0].Seq, got.Events[1].Seq, got.Events[2].Seq)
	}
}

// TestAppsecEventsRouteToTheirSite proves the agent's logs feed the same per-site
// view the ModSecurity path feeds: a site's counter rises for the event that
// names it, another site's does not, and an event naming no site stays on the
// events list with no site.
func TestAppsecEventsRouteToTheirSite(t *testing.T) {
	f := newAppsecFixture(t)
	// Spread across two workers, as a real host would.
	f.writeEvent(workerLog(1), f.now.Add(-time.Minute),
		f.event("a.test", "/x", "198.51.100.4", "Prevent", f.now.Add(-30*time.Second)),
		f.event("b.test", "/y", "198.51.100.5", "Detect", f.now.Add(-20*time.Second)),
	)
	f.writeEvent(workerLog(3), f.now.Add(-time.Minute),
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

// TestAppsecReopensAReplacedFile proves a numbered file replaced under the
// reader (the agent's rotation) is re-read from the top rather than from the old
// offset, and its events are counted once.
func TestAppsecReopensAReplacedFile(t *testing.T) {
	f := newAppsecFixture(t)
	name := workerLog(1)
	f.writeEvent(name, f.now.Add(-2*time.Minute),
		f.event("a.test", "/1", "198.51.100.4", "Prevent", f.now.Add(-time.Minute)))
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 1 {
		t.Fatalf("events = %d, want 1", got.Total)
	}

	// Replace the file the way a rotation does: rename the old one away, so the
	// new file is guaranteed a different inode. Removing it instead lets the
	// filesystem hand the same inode back, and with an equal-length body the
	// collector cannot tell the file was replaced.
	if err := os.Rename(filepath.Join(f.appsecDir, name), filepath.Join(f.appsecDir, "rotated.bak")); err != nil {
		t.Fatal(err)
	}
	f.writeEvent(name, f.now, f.event("a.test", "/2", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()

	got := f.c.Events(EventQuery{Limit: 10})
	if got.Total != 2 {
		t.Fatalf("after the file was replaced: %d events, want 2", got.Total)
	}
	if got.Events[0].URI != "/2" {
		t.Errorf("newest = %q, want /2", got.Events[0].URI)
	}
}

// TestAppsecForgetsARetiredFile proves a worker's log that disappears is dropped,
// so a later file reusing its name starts from the top instead of from a stale
// offset.
func TestAppsecForgetsARetiredFile(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(workerLog(1), f.now, f.event("a.test", "/1", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()

	if err := os.Remove(filepath.Join(f.appsecDir, workerLog(1))); err != nil {
		t.Fatal(err)
	}
	f.c.Poll()
	if len(f.c.appsecTails) != 0 {
		t.Errorf("tails = %d, want the retired file forgotten", len(f.c.appsecTails))
	}
	// A fresh file with the same name is read from its start.
	f.writeEvent(workerLog(1), f.now, f.event("a.test", "/2", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 2 {
		t.Errorf("events = %d, want 2", got.Total)
	}
}

// TestAppsecIgnoresNonEventFiles proves the agent's debug, error and install logs
// are not event logs, and neither is the bare template name.
func TestAppsecIgnoresNonEventFiles(t *testing.T) {
	f := newAppsecFixture(t)
	line := `{"eventTime":"2026-11-24T18:01:47.380","eventName":"Web Request","eventData":{"securityAction":"Prevent","waapIncidentType":"SQL Injection","httpHostName":"a.test"}}`
	for _, name := range []string{
		appsecLogPrefix + ".dbg1",
		appsecLogPrefix + ".err1",
		appsecLogPrefix + "-install.log",
		appsecLogPrefix,
		appsecLogPrefix + "x",
	} {
		f.writeEvent(name, f.now, line)
	}
	f.c.Poll()
	if got := f.c.Events(EventQuery{Limit: 10}); got.Total != 0 {
		t.Errorf("a non-event file was read: %d events", got.Total)
	}
	if paths := f.c.appsecLogFiles(); len(paths) != 0 {
		t.Errorf("appsecLogFiles = %v, want none", paths)
	}
}

// TestAppsecLogNumbers proves which names count as a worker's event log.
func TestAppsecLogNumbers(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"cp-nano-http-transaction-handler.log1", 1},
		{"cp-nano-http-transaction-handler.log4", 4},
		{"cp-nano-http-transaction-handler.log12", 12},
		{"cp-nano-http-transaction-handler.log", -1},
		{"cp-nano-http-transaction-handler.log.dbg1", -1},
		{"cp-nano-http-transaction-handler.dbg1", -1},
		{"cp-nano-http-transaction-handler.err1", -1},
		{"cp-nano-http-transaction-handler-install.log", -1},
		{"cp-nano-http-transaction-handler.log1.gz", -1},
		{"other.log1", -1},
		{"", -1},
	}
	for _, tc := range tests {
		if got := appsecLogNumber(tc.name); got != tc.want {
			t.Errorf("appsecLogNumber(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestAppsecIgnoresAgentHousekeeping proves the agent's non-event records never
// reach the events list or the counters.
func TestAppsecIgnoresAgentHousekeeping(t *testing.T) {
	f := newAppsecFixture(t)
	f.writeEvent(workerLog(1), f.now,
		`{"eventTime": "2026-11-24T17:57:28.249","eventName": "Web AppSec Policy Loaded Successfully","eventSeverity": "Info","eventData": {"logIndex": 1}}`,
		`{"eventTime": "2026-11-24T17:57:28.355","eventName": "Check Point Nano-service started","eventSeverity": "Info","eventData": {"logIndex": 2}}`,
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
	f.writeEvent(workerLog(1), f.now,
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
	f.writeEvent(workerLog(1), f.now.Add(-time.Minute),
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
	name := workerLog(1)
	f.writeEvent(name, f.now, f.event("a.test", "/1", "198.51.100.4", "Prevent", f.now))
	f.c.Poll()

	whole := f.event("a.test", "/2", "198.51.100.4", "Prevent", f.now)
	fh, err := os.OpenFile(filepath.Join(f.appsecDir, name), os.O_APPEND|os.O_WRONLY, 0)
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
