package sshauth

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWriteAbuseIPDBOneRowPerPublicAddress(t *testing.T) {
	t0 := time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC)
	failed := []Attempt{
		{At: t0.Add(2 * time.Minute), IP: "198.51.100.9", User: "test", Outcome: OutcomeFailed, Method: MethodPassword},
		{At: t0, IP: "203.0.113.7", User: "root", Outcome: OutcomeFailed, Method: MethodPassword},
		{At: t0.Add(time.Minute), IP: "203.0.113.7", User: "admin", Outcome: OutcomeFailed, Method: MethodPassword},
		{At: t0.Add(time.Minute), IP: "203.0.113.7", User: "root", Outcome: OutcomeFailed, Method: MethodPassword},
		// None of these may be reported: private, loopback, not an address, or not a failure.
		{At: t0, IP: "10.0.0.5", User: "root", Outcome: OutcomeFailed},
		{At: t0, IP: "127.0.0.1", User: "root", Outcome: OutcomeFailed},
		{At: t0, IP: "not-an-ip", User: "root", Outcome: OutcomeFailed},
		{At: t0, IP: "192.0.2.1", User: "deploy", Outcome: OutcomeSuccess},
	}

	var buf bytes.Buffer
	if err := WriteAbuseIPDB(&buf, failed); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, buf.String())
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want header + 2:\n%s", len(rows), buf.String())
	}
	if strings.Join(rows[0], ",") != "IP,Categories,ReportDate,Comment" {
		t.Errorf("header = %v", rows[0])
	}
	// The address with three attempts ranks first.
	top := rows[1]
	if top[0] != "203.0.113.7" || top[1] != "18,22" || top[2] != "2026-11-21T11:01:00Z" {
		t.Errorf("first row = %v", top)
	}
	if want := "SSH login failed: 3 attempts between 2026-11-21 11:00:00 and 2026-11-21 11:01:00 UTC; usernames tried: root, admin"; top[3] != want {
		t.Errorf("comment = %q\nwant      %q", top[3], want)
	}
	if rows[2][0] != "198.51.100.9" || rows[2][2] != "2026-11-21T11:02:00Z" {
		t.Errorf("second row = %v", rows[2])
	}
}

func TestWriteAbuseIPDBNamesAtMostTenUsers(t *testing.T) {
	t0 := time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC)
	var failed []Attempt
	for i := range 12 {
		failed = append(failed, Attempt{At: t0, IP: "203.0.113.7", User: fmt.Sprintf("user%02d", i), Outcome: OutcomeFailed})
	}
	var buf bytes.Buffer
	if err := WriteAbuseIPDB(&buf, failed); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(rows[1][3], "user09 and 2 more") || strings.Contains(rows[1][3], "user10") {
		t.Errorf("comment = %q", rows[1][3])
	}
}

func TestFailuresReturnsEveryFailureNewestFirst(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	base := now.Add(-2 * time.Hour).UnixMicro()
	j := &fakeJournal{entries: lines(
		rec(base, "sshd", "Failed password for root from 203.0.113.7 port 1 ssh2"),
		rec(base+1_000_000, "sshd", "Accepted password for deploy from 203.0.113.7 port 2 ssh2"),
		rec(base+2_000_000, "sshd", "Failed password for admin from 198.51.100.9 port 3 ssh2"),
	)}
	m := newMonitor(t, j, now)
	got, err := m.Failures(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Port != 3 || got[1].Port != 1 {
		t.Fatalf("failures = %+v", got)
	}
	if j.gotN != MaxScan {
		t.Errorf("scan cap = %d, want %d", j.gotN, MaxScan)
	}
}
