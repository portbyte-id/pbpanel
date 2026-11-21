package sshauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeJournal is a Journal whose output a test writes directly.
type fakeJournal struct {
	entries []byte
	err     error
	gotFrom time.Time
	gotTo   time.Time
	gotN    int
	gotUnit []string
	calls   int
}

func (f *fakeJournal) Entries(_ context.Context, units []string, since, until time.Time, limit int) ([]byte, error) {
	f.calls++
	f.gotUnit, f.gotFrom, f.gotTo, f.gotN = units, since, until, limit
	return f.entries, f.err
}

// rec builds one `journalctl -o json` line. ts is in microseconds.
func rec(ts int64, ident, message string) string {
	return fmt.Sprintf(`{"__REALTIME_TIMESTAMP":%q,"SYSLOG_IDENTIFIER":%q,"_SYSTEMD_UNIT":"ssh.service","MESSAGE":%q}`,
		itoa64(ts), ident, message)
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

func lines(rs ...string) []byte { return []byte(strings.Join(rs, "\n") + "\n") }

// The record shape in testdata/journal-failed.json is the field set
// `journalctl -o json` writes; this pins that the reader still finds the message,
// the identifier and the timestamp in it.
func TestParseRecordFromJournalctlJSON(t *testing.T) {
	b, err := os.ReadFile("testdata/journal-failed.json")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := parseRecord(b)
	if !ok {
		t.Fatal("the real journal record was not parsed")
	}
	if a.IP != "45.155.205.233" || a.User != "admin" || a.Port != 44556 ||
		a.Outcome != OutcomeFailed || a.Method != MethodPassword {
		t.Errorf("fields = %+v", a)
	}
	want := time.Date(2024, 8, 7, 3, 6, 40, 123456000, time.UTC)
	if !a.At.Equal(want) {
		t.Errorf("at = %v, want %v", a.At, want)
	}
}

// A unit filter matches the unit's other programs too; a record from one of them
// is not sshd's and must be skipped. A record sshd wrote under any of its own
// identifiers is taken.
func TestParseRecordUsesTheIdentifier(t *testing.T) {
	for _, c := range []struct {
		ident string
		ok    bool
	}{
		{"sshd", true},
		{"sshd-session", true},
		{"ssh", true},
		{"systemd", false},
		{"sudo", false},
		{"panel", false},
	} {
		if _, ok := parseRecord(lines(rec(1, c.ident, "Failed password for deploy from 203.0.113.7 port 1 ssh2"))); ok != c.ok {
			t.Errorf("identifier %q: ok = %v, want %v", c.ident, ok, c.ok)
		}
	}
}

// Ubuntu's journald records the program tag inside MESSAGE as well as in the
// identifier; carrying it into the parser would make every line unknown.
func TestParseRecordStripsTheProgramTag(t *testing.T) {
	for _, msg := range []string{
		"sshd[4821]: Failed password for deploy from 203.0.113.7 port 1 ssh2",
		"sshd-session[9044]: Accepted publickey for root from 198.51.100.4 port 4022 ssh2",
		"Failed password for deploy from 203.0.113.7 port 1 ssh2",
	} {
		if _, ok := parseRecord(lines(rec(1, "sshd", msg))); !ok {
			t.Errorf("%q was dropped", msg)
		}
	}
}

// journald writes MESSAGE as a byte array when the text is not valid UTF-8.
func TestParseRecordReadsByteArrayMessage(t *testing.T) {
	line := []byte(`{"__REALTIME_TIMESTAMP":"1700000000000000","SYSLOG_IDENTIFIER":"sshd","MESSAGE":[70,97,105,108,101,100]}`)
	a, ok := parseRecord(line)
	if ok {
		t.Fatalf("a non-sshd message parsed as %+v", a)
	}
	msg := `{"__REALTIME_TIMESTAMP":"1700000000000000","SYSLOG_IDENTIFIER":"sshd","MESSAGE":[70,97,105,108,101,100,32,112,97,115,115,119,111,114,100,32,102,111,114,32,100,101,112,108,111,121,32,102,114,111,109,32,50,48,51,46,48,46,49,49,51,46,55,32,112,111,114,116,32,49,32,115,115,104,50]}`
	if a, ok = parseRecord([]byte(msg)); !ok {
		t.Fatal("the byte-array message was not parsed")
	}
	if a.IP != "203.0.113.7" || a.User != "deploy" || a.Port != 1 {
		t.Errorf("fields = %+v", a)
	}
}

func TestParseRecordSkipsBrokenLines(t *testing.T) {
	for _, line := range []string{
		`not json`,
		`{}`,
		`{"MESSAGE":"Failed password for deploy from 203.0.113.7 port 1 ssh2"}`,
		`{"__REALTIME_TIMESTAMP":"abc","SYSLOG_IDENTIFIER":"sshd","MESSAGE":"Failed password for deploy from 203.0.113.7 port 1 ssh2"}`,
		`{"__REALTIME_TIMESTAMP":"1700000000000000","SYSLOG_IDENTIFIER":"sshd","MESSAGE":"Server listening on 0.0.0.0 port 22."}`,
	} {
		if a, ok := parseRecord([]byte(line)); ok {
			t.Errorf("%q parsed as %+v", line, a)
		}
	}
}

func TestSplitRecords(t *testing.T) {
	b := []byte("{\"a\":1}\n\n{\"b\":2}\n{\"partial")
	got := splitRecords(b)
	if len(got) != 2 || string(got[0]) != `{"a":1}` || string(got[1]) != `{"b":2}` {
		t.Fatalf("got %q", got)
	}
	// A record over MaxRecord is dropped rather than held.
	huge := append([]byte(`{"x":"`), make([]byte, MaxRecord+10)...)
	for i := range huge {
		if huge[i] == 0 {
			huge[i] = 'a'
		}
	}
	huge = append(huge, '"', '}', '\n', 'o', 'k', '\n')
	if got := splitRecords(huge); len(got) != 1 || string(got[0]) != "ok" {
		t.Fatalf("huge record kept: %d", len(got))
	}
}

// newMonitor builds a Monitor over fake with a pinned clock.
func newMonitor(t *testing.T, j Journal, now time.Time) *Monitor {
	t.Helper()
	m, err := New(Options{Journal: j, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMonitorQueriesTheWindowAndSortsNewestFirst(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	base := now.Add(-2 * time.Hour).UnixMicro()
	j := &fakeJournal{entries: lines(
		rec(base, "sshd", "Failed password for deploy from 203.0.113.7 port 1 ssh2"),
		rec(base+1_000_000, "sshd", "Accepted password for deploy from 203.0.113.7 port 2 ssh2"),
		rec(base+2_000_000, "sshd", "Failed password for deploy from 198.51.100.9 port 3 ssh2"),
	)}
	m := newMonitor(t, j, now)
	rep, err := m.Attempts(context.Background(), Query{Range: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if j.gotN != MaxScan || j.gotTo != now || !j.gotFrom.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("journal queried with (%d, %v, %v)", j.gotN, j.gotFrom, j.gotTo)
	}
	if len(rep.Attempts) != 3 || rep.Attempts[0].Port != 3 || rep.Attempts[2].Port != 1 {
		t.Fatalf("attempts = %+v", rep.Attempts)
	}
	if rep.Total != 3 || rep.Summary.Failed != 2 || rep.Summary.Succeeded != 1 {
		t.Errorf("totals = %d %+v", rep.Total, rep.Summary)
	}
	if rep.Summary.DistinctIPs != 2 || rep.Summary.TopIPs[0].IP != "203.0.113.7" || rep.Summary.TopIPs[0].Total != 2 {
		t.Errorf("top ips = %+v", rep.Summary.TopIPs)
	}
}

func TestMonitorFiltersByOutcome(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	base := now.Add(-time.Hour).UnixMicro()
	j := &fakeJournal{entries: lines(
		rec(base, "sshd", "Failed password for deploy from 203.0.113.7 port 1 ssh2"),
		rec(base+1, "sshd", "Accepted password for deploy from 203.0.113.7 port 2 ssh2"),
	)}
	m := newMonitor(t, j, now)
	rep, err := m.Attempts(context.Background(), Query{Outcome: OutcomeFailed})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Attempts) != 1 || rep.Attempts[0].Outcome != OutcomeFailed {
		t.Errorf("failed filter = %+v", rep.Attempts)
	}
	// The summary is the window's, not the page's: a filtered page still says
	// how much happened in total.
	if rep.Summary.Failed != 1 || rep.Summary.Succeeded != 1 {
		t.Errorf("summary = %+v", rep.Summary)
	}
}

func TestMonitorBoundsRangeAndLimit(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	far := now.Add(-40 * 24 * time.Hour).UnixMicro()
	j := &fakeJournal{entries: lines(
		rec(far, "sshd", "Failed password for deploy from 203.0.113.7 port 1 ssh2"),
	)}
	m := newMonitor(t, j, now)

	if _, err := m.Attempts(context.Background(), Query{Range: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if !j.gotFrom.Equal(now.Add(-MinRange)) {
		t.Errorf("a tiny range was not raised to the minimum: %v", j.gotFrom)
	}
	if _, err := m.Attempts(context.Background(), Query{Range: 400 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !j.gotFrom.Equal(now.Add(-MaxRange)) {
		t.Errorf("a huge range was not cut to the maximum: %v", j.gotFrom)
	}

	base := now.Add(-time.Hour).UnixMicro()
	var many []string
	for i := range MaxLimit + 50 {
		many = append(many, rec(base+int64(i), "sshd", fmt.Sprintf("Failed password for deploy from 203.0.113.7 port %d ssh2", i+1)))
	}
	j.entries = lines(many...)
	rep, err := m.Attempts(context.Background(), Query{Limit: MaxLimit + 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Attempts) != MaxLimit || rep.Total != MaxLimit+50 {
		t.Errorf("limit cut: %d attempts, total %d", len(rep.Attempts), rep.Total)
	}
	// Defaults, not zeroes: a caller that names nothing still gets a page.
	rep, err = m.Attempts(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Attempts) != DefaultLimit || rep.Limit != DefaultLimit {
		t.Errorf("default limit: %d attempts, limit %d", len(rep.Attempts), rep.Limit)
	}
}

// A Journal that cannot read says so as ErrUnavailable, which the HTTP layer
// turns into a 503; the monitor does not try to guess before it asks.
func TestMonitorReportsAnUnreadableJournal(t *testing.T) {
	m := newMonitor(t, &fakeJournal{err: ErrUnavailable}, time.Now())
	if _, err := m.Attempts(context.Background(), Query{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestMonitorSummaryMatchesTheAttemptsReport(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	base := now.Add(-time.Hour).UnixMicro()
	j := &fakeJournal{entries: lines(
		rec(base, "sshd", "Failed password for root from 203.0.113.7 port 1 ssh2"),
		rec(base+1, "sshd", "Failed password for root from 203.0.113.7 port 2 ssh2"),
		rec(base+2, "sshd", "Failed password for admin from 198.51.100.9 port 3 ssh2"),
	)}
	m := newMonitor(t, j, now)
	s, err := m.Summary(context.Background(), time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Failed != 3 || s.Succeeded != 0 || s.DistinctIPs != 2 || s.DistinctUsers != 2 {
		t.Errorf("summary = %+v", s)
	}
	if s.TopIPs[0].IP != "203.0.113.7" || s.TopIPs[0].Failed != 2 {
		t.Errorf("top ips = %+v", s.TopIPs)
	}
	if s.TopUsers[0].User != "root" || s.TopUsers[0].Failed != 2 {
		t.Errorf("top users = %+v", s.TopUsers)
	}
}

// runner records the argv journalctl is called with, which is the only thing
// standing between this package and a wrong journal read.
type runner struct {
	name string
	args []string
	out  []byte
	err  error
}

func (r *runner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, args
	return r.out, r.err
}

func TestJournalctlAsksForJustWhatItNeeds(t *testing.T) {
	r := &runner{out: []byte("{}\n")}
	j := Journalctl{Run: r}
	since := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	if _, err := j.Entries(context.Background(), DefaultUnits, since, until, 20000); err != nil {
		t.Fatal(err)
	}
	want := []string{"--no-pager", "-o", "json", "-q",
		"--since", "2026-11-20T12:00:00Z", "--until", "2026-11-21T12:00:00Z", "-n", "20000",
		"-u", "sshd.service", "-u", "ssh.service", "-u", "sshd-session.service"}
	if r.name != "journalctl" || strings.Join(r.args, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %s %v", r.name, r.args)
	}
}

// A unit is one argv, so nothing shaped like a path, a shell word or an option
// may reach journalctl.
func TestJournalctlRefusesANonUnit(t *testing.T) {
	for _, u := range []string{"", "../etc", "sshd.service; id", "-u", "a b", "ssh/../../x"} {
		j := Journalctl{Run: &runner{}}
		if _, err := j.Entries(context.Background(), []string{u}, time.Now(), time.Now(), 1); !errors.Is(err, ErrUnavailable) {
			t.Errorf("unit %q accepted: %v", u, err)
		}
	}
}

func TestJournalctlFailureIsUnavailable(t *testing.T) {
	j := Journalctl{Run: &runner{err: errors.New("exit status 1"), out: []byte("No journal files were found.")}}
	if _, err := j.Entries(context.Background(), DefaultUnits, time.Now(), time.Now(), 1); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if _, err := j.Entries(context.Background(), DefaultUnits, time.Now(), time.Now(), 1); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v on the second call", err)
	}
}
