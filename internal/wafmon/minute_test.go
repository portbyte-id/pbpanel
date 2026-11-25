package wafmon

import (
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

func TestParseUpstreamTime(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0.004", 4, true},
		{"-", 0, false},
		{"", 0, false},
		{"0.010, 0.004", 14, true},
		{"0.002 : 0.003", 5, true},
		{"0.010, -", 10, true},
		{"abc", 0, false},
		{"-1", 0, false},
	}
	for _, c := range cases {
		got, ok := parseUpstreamTime(c.in)
		if ok != c.ok || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("parseUpstreamTime(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseAccessReadsQuotedUpstreamTime(t *testing.T) {
	const head = `203.0.113.7 - - [18/Nov/2026:10:00:01 +0000] "GET /a HTTP/1.1" 200 512 "-" "curl/8" `
	cases := []struct {
		name, tail string
		urt        float64
		hasURT     bool
		rid        string
	}{
		{"single", `rt=0.123 rid=abc urt="0.120"`, 120, true, "abc"},
		{"retries", `rt=0.123 rid=abc urt="0.100, 0.020"`, 120, true, "abc"},
		{"static file", `rt=0.001 rid=abc urt="-"`, 0, false, "abc"},
		{"old format without it", `rt=0.123 rid=abc`, 0, false, "abc"},
		{"urt first", `urt="0.5" rt=0.6 rid=q`, 500, true, "q"},
	}
	for _, c := range cases {
		r, ok := ParseAccess(head + c.tail)
		if !ok {
			t.Fatalf("%s: line rejected", c.name)
		}
		if r.hasURT != c.hasURT || math.Abs(r.URT-c.urt) > 1e-9 || r.RequestID != c.rid || !r.hasMS {
			t.Errorf("%s: URT=%v has=%v rid=%q hasMS=%v", c.name, r.URT, r.hasURT, r.RequestID, r.hasMS)
		}
	}
	// A user agent that merely looks like the field must not be read as it.
	r, ok := ParseAccess(`1.1.1.1 - - [18/Nov/2026:10:00:01 +0000] "GET / HTTP/1.1" 200 1 "-" "evil urt=\"9\"" rt=0.1 rid=x`)
	if !ok || r.hasURT {
		t.Errorf("agent text was read as upstream time: %+v", r)
	}
}

func urtLine(at time.Time, status int, urt string) string {
	return fmt.Sprintf(`9.9.9.9 - - [%s] "GET /p HTTP/1.1" %d 100 "-" "ua" rt=0.200 rid=r urt="%s"`, at.Format(accessTimeLayout), status, urt)
}

func TestDrainMinutesCountsAndHistograms(t *testing.T) {
	f := newFixture(t) // now = 10:00:30
	m0 := time.Date(2026, 11, 18, 9, 58, 0, 0, time.UTC)
	f.append("a.test.access.log",
		urtLine(m0.Add(5*time.Second), 200, "0.050"),
		urtLine(m0.Add(20*time.Second), 200, "-"),
		urtLine(m0.Add(70*time.Second), 502, "0.100, 0.100"), // the next minute
		urtLine(f.now.Add(-time.Second), 200, "0.010"),       // 10:00:29, still open
	)
	f.c.Poll()

	// Closed minutes only: 9:58 and 9:59 ended before 10:00:00.
	got := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC))
	if len(got) != 2 {
		t.Fatalf("drained %d minutes, want 2: %+v", len(got), got)
	}
	a, b := got[0], got[1]
	if a.Domain != "a.test" || a.Minute != m0.Unix() || a.Req != 2 || a.S2 != 2 || a.Bytes != 200 {
		t.Errorf("first minute = %+v", a)
	}
	if a.RT.Total() != 2 || a.URT.Total() != 1 {
		t.Errorf("first minute histograms: rt %d urt %d, want 2 and 1 (a static file has no upstream time)", a.RT.Total(), a.URT.Total())
	}
	if b.Minute != m0.Add(time.Minute).Unix() || b.S5 != 1 || b.URT.Total() != 1 {
		t.Errorf("second minute = %+v", b)
	}

	// Drained minutes are forgotten; the open minute is still there.
	if again := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)); len(again) != 0 {
		t.Errorf("drained twice: %+v", again)
	}
	f.now = f.now.Add(time.Minute)
	rest := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 1, 0, 0, time.UTC))
	if len(rest) != 1 || rest[0].Req != 1 {
		t.Errorf("open minute = %+v", rest)
	}
}

func TestLateLinesAreCountedNotReAdded(t *testing.T) {
	f := newFixture(t)
	m := time.Date(2026, 11, 18, 9, 58, 0, 0, time.UTC)
	f.append("a.test.access.log", urtLine(m.Add(time.Second), 200, "0.01"))
	f.c.Poll()
	if got := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)); len(got) != 1 {
		t.Fatalf("drained %d", len(got))
	}
	f.append("a.test.access.log", urtLine(m.Add(30*time.Second), 200, "0.01"))
	f.c.Poll()
	if got := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)); len(got) != 0 {
		t.Errorf("a late line re-opened a handed-over minute: %+v", got)
	}
	if f.c.LateLines() != 1 {
		t.Errorf("late lines = %d, want 1", f.c.LateLines())
	}
}

func TestDrainHoldsBackWhileALogHasBacklog(t *testing.T) {
	f := newFixture(t)
	m := time.Date(2026, 11, 18, 9, 58, 0, 0, time.UTC)
	f.append("a.test.access.log", urtLine(m.Add(time.Second), 200, "0.01"))
	f.c.Poll()
	f.c.backlog.Store(true)
	if got := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)); got != nil {
		t.Errorf("drained %d minutes with unread backlog", len(got))
	}
	f.c.backlog.Store(false)
	if got := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)); len(got) != 1 {
		t.Errorf("held-back minute was lost: %d", len(got))
	}
}

func TestRemovedSiteDropsItsMinutes(t *testing.T) {
	f := newFixture(t)
	m := time.Date(2026, 11, 18, 9, 58, 0, 0, time.UTC)
	f.append("a.test.access.log", urtLine(m.Add(time.Second), 200, "0.01"))
	f.c.Poll()
	if err := os.Remove(f.path("a.test.access.log")); err != nil {
		t.Fatal(err)
	}
	f.c.Poll()
	if got := f.c.DrainMinutes(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)); len(got) != 0 {
		t.Errorf("minutes of a removed site survived: %+v", got)
	}
}
