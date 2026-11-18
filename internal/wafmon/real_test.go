package wafmon

import (
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/audit-real.json is a line ModSecurity v3.0.14 wrote with CRS 4.30.0
// in DetectionOnly mode, from the real-Docker firewall test, for an XSS probe.
// The rest of the parser tests use shapes written from the documentation; this
// one is what the engine actually emits.
func TestParseAuditRealModSecurityLine(t *testing.T) {
	b, err := os.ReadFile("testdata/audit-real.json")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ParseAudit(strings.TrimSpace(string(b)))
	if !ok {
		t.Fatal("the real audit line was not parsed")
	}
	if e.IP != "127.0.0.1" || e.Method != "GET" || e.Status != 200 || e.RequestID != "fb489b03c626cf0f7885506081dc9a88" {
		t.Errorf("transaction fields: %+v", e)
	}
	if want := time.Date(2026, 11, 18, 11, 1, 3, 0, time.UTC); !e.Time.Equal(want) {
		t.Errorf("time = %v, want %v", e.Time, want)
	}
	if e.Action != ActionDetected {
		t.Errorf("a DetectionOnly 200 is %q, want detected", e.Action)
	}
	if e.RuleID != "941100" || e.Severity != "critical" || !strings.Contains(e.Message, "XSS") {
		t.Errorf("primary rule = %s %s %q", e.RuleID, e.Severity, e.Message)
	}
	var anomaly bool
	for _, m := range e.Matches {
		anomaly = anomaly || m.RuleID == "949110"
		if len(m.Tags) == 0 || m.File == "" || m.Line == 0 {
			t.Errorf("match %s lost its tags, file or line: %+v", m.RuleID, m)
		}
	}
	if len(e.Matches) < 5 || !anomaly {
		t.Errorf("matches = %d, anomaly rule present = %v; want every rule that fired, with the summary rule", len(e.Matches), anomaly)
	}
}
