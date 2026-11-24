package wafmon

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testdata/appsec-real.jsonl holds the three security events captured in step
// 0's spike (docs/design/open-appsec-plan.md §0.6), from deliberate attacks
// against a site on the Ubuntu sandbox. The agent's own long identifiers
// (agentId, eventReferenceId, the tail of the SQL-injection sample) are the
// elisions the plan trimmed, replaced with fixed values so the assertions are
// stable; the event lines are otherwise what the agent wrote.
func TestParseOpenAppsecRealLines(t *testing.T) {
	b, err := os.ReadFile("testdata/appsec-real.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("fixture holds %d lines, want the three captured events", len(lines))
	}
	tests := []struct {
		name      string
		want      Event
		wantMatch Match
	}{
		{
			name: "sql injection in a url parameter, full event",
			want: Event{
				Engine:     EngineOpenAppsec,
				Site:       "up5.test",
				IP:         "127.0.0.1",
				Method:     "GET",
				URI:        "/?id=1' OR '1'='1' UNION SELECT ",
				RequestID:  "2ff09af1-0000-4000-8000-000000000001",
				Action:     ActionBlocked,
				Severity:   "critical",
				Message:    "SQL Injection",
				Confidence: "Very High",
				AttackType: "SQL Injection",
				Parameter:  "id (url parameter)",
			},
			wantMatch: Match{
				Message:  "SQL Injection",
				Severity: "critical",
				Data:     "1' or '1'='1' union select username,password from users--",
			},
		},
		{
			name: "cross site scripting, which the agent names twice in one field",
			want: Event{
				Engine:     EngineOpenAppsec,
				Site:       "up5.test",
				IP:         "127.0.0.1",
				URI:        "?q=<script>alert(document.cookie)</script>",
				Action:     ActionBlocked,
				Severity:   "critical",
				Message:    "Cross Site Scripting, Remote Code Execution",
				AttackType: "Cross Site Scripting, Remote Code Execution",
				Parameter:  "q (url parameter)",
			},
			wantMatch: Match{
				Message:  "Cross Site Scripting, Remote Code Execution",
				Severity: "critical",
				Data:     "<script>alert(document.cookie)</script>",
			},
		},
		{
			name: "query without a path, time-based injection, no confidence field",
			want: Event{
				Engine:     EngineOpenAppsec,
				Site:       "up5.test",
				IP:         "127.0.0.1",
				URI:        "?cat=1 AND (SELECT 5 FROM (SELECT SLEEP(5))a)",
				Action:     ActionBlocked,
				Severity:   "critical",
				Message:    "SQL Injection",
				AttackType: "SQL Injection",
				Parameter:  "cat",
			},
			wantMatch: Match{Message: "SQL Injection", Severity: "critical"},
		},
	}
	wantTimes := []time.Time{
		time.Date(2026, 11, 24, 18, 1, 47, 380_000_000, time.Local),
		time.Date(2026, 11, 24, 18, 1, 47, 394_000_000, time.Local),
		time.Date(2026, 11, 24, 18, 1, 47, 405_000_000, time.Local),
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := ParseOpenAppsec(lines[i])
			if !ok {
				t.Fatal("the real event line was not parsed")
			}
			if !e.Time.Equal(wantTimes[i]) {
				t.Errorf("time = %v, want %v", e.Time, wantTimes[i])
			}
			e.Time = time.Time{}
			if len(e.Matches) != 1 {
				t.Fatalf("matches = %d, want the one synthetic match", len(e.Matches))
			}
			// The match list is asserted one entry at a time below; the event
			// comparison covers every other field.
			m := e.Matches[0]
			e.Matches = nil
			tc.wantMatch.Tags = nil
			// nil and an empty slice are the same "no tags" to the page.
			m.Tags = nil
			if !reflect.DeepEqual(e, tc.want) {
				t.Errorf("event = %+v\nwant      %+v", e, tc.want)
			}
			if !reflect.DeepEqual(m, tc.wantMatch) {
				t.Errorf("match = %+v\nwant    %+v", m, tc.wantMatch)
			}
		})
	}
}

// TestParseOpenAppsecRejectsNonEvents proves the agent's other log records (a
// policy load, a service start) are not events, so they never reach the page.
func TestParseOpenAppsecRejectsNonEvents(t *testing.T) {
	tests := []struct{ name, line string }{
		{"policy load", `{"eventTime":"2026-11-24T18:00:00.000","eventName":"Policy load","eventData":{"securityAction":""}}`},
		{"service start", `{"eventTime":"2026-11-24T18:00:00.000","eventName":"Service start"}`},
		{"no eventData", `{"eventTime":"2026-11-24T18:00:00.000","eventName":"Web Request"}`},
		{"not json", `open-appsec: started`},
		{"empty", ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if e, ok := ParseOpenAppsec(tc.line); ok {
				t.Fatalf("parsed as an event: %+v", e)
			}
		})
	}
}

func TestParseOpenAppsecFields(t *testing.T) {
	e, ok := ParseOpenAppsec(`{"eventTime":"2026-11-24T18:01:47.380","eventSeverity":"High",` +
		`"eventData":{"sourceIP":"203.0.113.9","httpHostName":"A.Test:8443.","httpMethod":"POST",` +
		`"httpUriPath":"/login","httpUriQuery":"user=a","securityAction":"Detect",` +
		`"waapIncidentType":"Cross Site Scripting","matchedLocation":"body","eventConfidence":"High"}}`)
	if !ok {
		t.Fatal("not parsed")
	}
	// The host loses its port and trailing dot and is lowercased, so it names the
	// site the Host header belongs to.
	if e.Site != "a.test" || e.URI != "/login?user=a" || e.Method != "POST" || e.IP != "203.0.113.9" {
		t.Errorf("event = %+v", e)
	}
	// Detect is the agent's detection-only action, the same as ModSecurity's.
	if e.Action != ActionDetected {
		t.Errorf("action = %q, want detected", e.Action)
	}
	// The severity word maps onto the monitor's scale, and a parameter with no
	// location keeps just its name.
	if e.Severity != "error" || e.Parameter != "body" {
		t.Errorf("severity = %q, parameter = %q", e.Severity, e.Parameter)
	}
	// No rule ids exist in this engine, so the filter must not pretend it has one.
	if e.RuleID != "" {
		t.Errorf("rule id = %q, want empty", e.RuleID)
	}
}

// TestParseOpenAppsecUnknownHostKeepsTheEvent proves an event whose Host header
// is no site's name is still an event: the events page shows it with no site,
// rather than the collector dropping what it cannot route.
func TestParseOpenAppsecUnknownHostKeepsTheEvent(t *testing.T) {
	for _, host := range []string{"", "not a host", "-", "a.test/path"} {
		line := `{"eventData":{"httpHostName":"` + host + `","securityAction":"Prevent","waapIncidentType":"SQL Injection"}}`
		e, ok := ParseOpenAppsec(line)
		if !ok {
			t.Fatalf("host %q: not parsed", host)
		}
		if e.Site != "" {
			t.Errorf("host %q: site = %q, want none", host, e.Site)
		}
	}
}

// TestParseAuditCarriesItsEngine proves the ModSecurity parser tags its events
// too, so nothing downstream has to guess which engine wrote an event.
func TestParseAuditCarriesItsEngine(t *testing.T) {
	e, ok := ParseAudit(`{"transaction":{"response":{"http_code":403},"messages":[{"message":"m","details":{"ruleId":"941100","severity":"2"}}]}}`)
	if !ok {
		t.Fatal("not parsed")
	}
	if e.Engine != EngineModSecurity {
		t.Errorf("engine = %q, want %q", e.Engine, EngineModSecurity)
	}
	// ModSecurity has none of open-appsec's own fields.
	if e.Confidence != "" || e.AttackType != "" || e.Parameter != "" {
		t.Errorf("open-appsec fields set on a ModSecurity event: %+v", e)
	}
	b, err := os.ReadFile("testdata/audit-real.json")
	if err != nil {
		t.Fatal(err)
	}
	if real, ok := ParseAudit(strings.TrimSpace(string(b))); !ok || real.Engine != EngineModSecurity {
		t.Errorf("the real audit line lost its engine tag: %+v", real)
	}
}
