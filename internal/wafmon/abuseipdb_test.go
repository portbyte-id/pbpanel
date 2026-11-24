package wafmon

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"
)

// TestWriteAbuseIPDBGroupsByAddress proves the export is one row per public
// address, with the request counts, the outcomes, the rules and the window in its
// comment, and that private or empty addresses are left out.
func TestWriteAbuseIPDBGroupsByAddress(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	events := []Event{
		{IP: "198.51.100.4", Site: "a.test", Action: ActionBlocked, Time: t0.Add(time.Minute),
			Matches: []Match{{RuleID: "942100", Message: "SQL Injection Attack Detected via libinjection"}, {RuleID: "949110", Message: "Inbound Anomaly Score Exceeded"}}},
		{IP: "198.51.100.4", Site: "b.test", Action: ActionDetected, Time: t0,
			Matches: []Match{{RuleID: "942100", Message: "SQL Injection Attack Detected via libinjection"}}},
		{IP: "203.0.113.9", Site: "a.test", Action: ActionBlocked, Time: t0.Add(2 * time.Minute), RuleID: "930120", Message: "LFI"},
		{IP: "192.168.1.10", Site: "a.test", Action: ActionBlocked, Time: t0},
		{IP: "", Site: "a.test", Action: ActionBlocked, Time: t0},
	}

	var buf bytes.Buffer
	if err := WriteAbuseIPDB(&buf, events); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("output is not CSV: %v", err)
	}
	if got := strings.Join(rows[0], ","); got != "IP,Categories,ReportDate,Comment" {
		t.Fatalf("header = %q", got)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want header plus the two public addresses: %v", len(rows), rows)
	}

	// The busier address sorts first.
	busy := rows[1]
	if busy[0] != "198.51.100.4" || busy[1] != "21" {
		t.Errorf("first row = %v", busy)
	}
	if busy[2] != t0.Add(time.Minute).Format(time.RFC3339) {
		t.Errorf("report date = %q, want the latest event", busy[2])
	}
	for _, want := range []string{
		"2 requests (1 blocked, 1 detected)",
		"between 2026-11-24 09:00:00 and 2026-11-24 09:01:00 UTC",
		"sites: a.test, b.test",
		"942100 SQL Injection Attack Detected via libinjection",
	} {
		if !strings.Contains(busy[3], want) {
			t.Errorf("comment %q lacks %q", busy[3], want)
		}
	}
	if strings.Contains(busy[3], "949110") {
		t.Errorf("comment names an anomaly-score rule: %q", busy[3])
	}
	if rows[2][0] != "203.0.113.9" {
		t.Errorf("second row = %v", rows[2])
	}
}

// TestWriteAbuseIPDBWithNoEventsIsJustTheHeader proves an empty export is still a
// valid file to upload, not an error.
func TestWriteAbuseIPDBWithNoEventsIsJustTheHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAbuseIPDB(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "IP,Categories,ReportDate,Comment" {
		t.Errorf("output = %q", got)
	}
}
