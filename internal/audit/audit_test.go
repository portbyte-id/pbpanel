package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// newWriter returns a Writer over a fresh database and a mirror path inside a
// temp dir, so nothing touches /www.
func newWriter(t *testing.T, opts ...Option) (*Writer, *store.Store, string) {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})

	mirror := filepath.Join(t.TempDir(), "logs", "audit.log")
	all := append([]Option{WithMirrorPath(mirror), WithLogger(discardLogger())}, opts...)
	return New(s.DB(), all...), s, mirror
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type row struct {
	at     string
	actor  string
	action string
	target string
	result string
	detail string
}

func readRows(t *testing.T, s *store.Store) []row {
	t.Helper()

	rows, err := s.DB().Query(`SELECT at, actor, action, target, result, detail FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.at, &r.actor, &r.action, &r.target, &r.result, &r.detail); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func readMirror(t *testing.T, path string) string {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mirror %s: %v", path, err)
	}
	return string(buf)
}

func TestMigrationCreatesAuditLogTable(t *testing.T) {
	_, s, _ := newWriter(t)

	var n int
	err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='audit_log'`).Scan(&n)
	if err != nil {
		t.Fatalf("look up audit_log: %v", err)
	}
	if n != 1 {
		t.Fatal("audit_log table was not created by the migration")
	}
}

func TestWriteRecordsSuccessAndFailure(t *testing.T) {
	w, s, _ := newWriter(t)
	ctx := context.Background()

	if err := w.Write(ctx, Entry{
		Actor: "admin", Action: "settings.update",
		Target: "panel.port", Result: Success, Detail: map[string]any{"value": "8443"},
	}); err != nil {
		t.Fatalf("Write success: %v", err)
	}
	if err := w.Write(ctx, Entry{
		Actor: "admin", Action: "login",
		Target: "session", Result: Failure, Detail: map[string]any{"reason": "bad password"},
	}); err != nil {
		t.Fatalf("Write failure: %v", err)
	}

	got := readRows(t, s)
	if len(got) != 2 {
		t.Fatalf("audit_log has %d rows, want 2", len(got))
	}
	if got[0].result != string(Success) {
		t.Errorf("first row result = %q, want %q", got[0].result, Success)
	}
	if got[1].result != string(Failure) {
		t.Errorf("second row result = %q, want %q", got[1].result, Failure)
	}
	if got[0].action != "settings.update" || got[0].target != "panel.port" || got[0].actor != "admin" {
		t.Errorf("first row = %+v, want action/target/actor from the entry", got[0])
	}
	if _, err := time.Parse(time.RFC3339Nano, got[0].at); err != nil {
		t.Errorf("first row at = %q, want RFC 3339: %v", got[0].at, err)
	}
}

func TestWriteStampsUTC(t *testing.T) {
	w, s, _ := newWriter(t)

	before := time.Now().UTC().Truncate(time.Second)
	if err := w.Write(context.Background(), Entry{Actor: "admin", Action: "logout", Result: Success}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := readRows(t, s)
	if len(got) != 1 {
		t.Fatalf("audit_log has %d rows, want 1", len(got))
	}
	if !strings.HasSuffix(got[0].at, "Z") {
		t.Errorf("at = %q, want a UTC timestamp ending in Z", got[0].at)
	}
	at, err := time.Parse(time.RFC3339Nano, got[0].at)
	if err != nil {
		t.Fatalf("parse at %q: %v", got[0].at, err)
	}
	if at.Before(before) || at.After(time.Now().UTC().Add(time.Minute)) {
		t.Errorf("at = %v, want the current time", at)
	}
}

func TestWriteRedactsSecretsInRowAndMirror(t *testing.T) {
	w, s, mirror := newWriter(t)

	err := w.Write(context.Background(), Entry{
		Actor:  "admin",
		Action: "settings.update",
		Target: "panel",
		Result: Success,
		Detail: map[string]any{
			"password":      "hunter2",
			"new_password":  "s3cr3t-value",
			"token":         "tok_live_abcdef",
			"csrf":          "csrf-123",
			"authorization": "Bearer abc.def.ghi",
			"cookie":        "pb_session=deadbeef",
			"api_key":       "key-xyz",
			"site":          "example.test",
		},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	secrets := []string{
		"hunter2", "s3cr3t-value", "tok_live_abcdef", "csrf-123",
		"Bearer abc.def.ghi", "pb_session=deadbeef", "key-xyz",
	}
	rows := readRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("audit_log has %d rows, want 1", len(rows))
	}
	file := readMirror(t, mirror)

	for _, secret := range secrets {
		if strings.Contains(rows[0].detail, secret) {
			t.Errorf("row detail contains secret %q: %s", secret, rows[0].detail)
		}
		if strings.Contains(file, secret) {
			t.Errorf("mirror contains secret %q: %s", secret, file)
		}
	}

	if !strings.Contains(rows[0].detail, `"site":"example.test"`) {
		t.Errorf("row detail dropped the non-secret field: %s", rows[0].detail)
	}
	if !strings.Contains(rows[0].detail, Redacted) {
		t.Errorf("row detail has no %s marker: %s", Redacted, rows[0].detail)
	}
}

func TestWriteRedactsNestedAndStructDetails(t *testing.T) {
	w, s, mirror := newWriter(t)
	ctx := context.Background()

	type creds struct {
		User     string            `json:"user"`
		Password string            `json:"password"`
		Headers  map[string]string `json:"headers"`
	}
	if err := w.Write(ctx, Entry{
		Actor: "admin", Action: "probe", Result: Success,
		Detail: creds{
			User:     "admin",
			Password: "deep-secret",
			Headers:  map[string]string{"X-CSRF-Token": "nested-csrf", "Accept": "text/html"},
		},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rows := readRows(t, s)
	file := readMirror(t, mirror)
	for _, secret := range []string{"deep-secret", "nested-csrf"} {
		if strings.Contains(rows[0].detail, secret) {
			t.Errorf("row detail contains nested secret %q: %s", secret, rows[0].detail)
		}
		if strings.Contains(file, secret) {
			t.Errorf("mirror contains nested secret %q: %s", secret, file)
		}
	}
	if !strings.Contains(rows[0].detail, "admin") || !strings.Contains(rows[0].detail, "text/html") {
		t.Errorf("row detail dropped non-secret values: %s", rows[0].detail)
	}
}

func TestWriteMirrorIsOneJSONObjectPerLineAndCreatesDirectory(t *testing.T) {
	w, _, mirror := newWriter(t)
	ctx := context.Background()

	dir := filepath.Dir(mirror)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mirror directory already exists before the first write: %v", err)
	}

	for i, action := range []string{"first", "second", "third"} {
		if err := w.Write(ctx, Entry{
			Actor: "admin", Action: action, Target: "t", Result: Success,
			Detail: map[string]any{"n": i},
		}); err != nil {
			t.Fatalf("Write %s: %v", action, err)
		}
	}

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("mirror directory was not created: %v", err)
	}

	body := readMirror(t, mirror)
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("mirror has %d lines, want 3:\n%s", len(lines), body)
	}

	wantActions := []string{"first", "second", "third"}
	for i, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %d is not a JSON object: %v (%q)", i, err, line)
		}
		if decoded["action"] != wantActions[i] {
			t.Errorf("line %d action = %v, want %q", i, decoded["action"], wantActions[i])
		}
		for _, field := range []string{"at", "actor", "action", "target", "result", "detail"} {
			if _, ok := decoded[field]; !ok {
				t.Errorf("line %d has no %q field: %s", i, field, line)
			}
		}
	}
}

func TestMirrorFailureDoesNotFailTheWriteAndIsReportedOnce(t *testing.T) {
	// A file where the directory should be makes MkdirAll fail on every platform.
	blocker := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create blocker: %v", err)
	}

	var logs logBuffer
	w, s, _ := newWriter(t,
		WithMirrorPath(filepath.Join(blocker, "audit.log")),
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
	)

	ctx := context.Background()
	for i := range 3 {
		if err := w.Write(ctx, Entry{Actor: "admin", Action: "repeat", Result: Success, Detail: map[string]any{"n": i}}); err != nil {
			t.Fatalf("Write %d returned an error despite the mirror failing: %v", i, err)
		}
	}

	if got := len(readRows(t, s)); got != 3 {
		t.Fatalf("audit_log has %d rows, want 3: the database must still be written", got)
	}

	if got := strings.Count(logs.String(), "file mirror failed"); got != 1 {
		t.Fatalf("mirror failure logged %d times, want exactly 1: %s", got, logs.String())
	}
}

// logBuffer is a sync-safe io.Writer for a slog handler used from test code.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestWriteRejectsInvalidEntries(t *testing.T) {
	w, s, mirror := newWriter(t)
	ctx := context.Background()

	cases := map[string]Entry{
		"no action":   {Actor: "admin", Result: Success},
		"no result":   {Actor: "admin", Action: "x"},
		"bad result":  {Actor: "admin", Action: "x", Result: "maybe"},
		"empty entry": {},
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			err := w.Write(ctx, entry)
			if !errors.Is(err, ErrInvalidEntry) {
				t.Fatalf("Write error = %v, want ErrInvalidEntry", err)
			}
		})
	}

	if got := readRows(t, s); len(got) != 0 {
		t.Fatalf("audit_log has %d rows, want 0: a rejected entry must not be stored", len(got))
	}
	if _, err := os.Stat(mirror); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mirror exists after rejected entries: %v", err)
	}
}

// The mirror must never be ahead of the database: if the insert fails there is
// no row, so there must be no line either.
func TestMirrorIsNotWrittenWhenTheInsertFails(t *testing.T) {
	w, s, mirror := newWriter(t)

	if _, err := s.DB().Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatalf("drop audit_log: %v", err)
	}

	if err := w.Write(context.Background(), Entry{Actor: "admin", Action: "x", Result: Success}); err == nil {
		t.Fatal("Write succeeded with no audit_log table")
	}
	if _, err := os.Stat(mirror); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mirror exists although the row was never stored: %v", err)
	}
}

func TestWriteRedactsTLSSecrets(t *testing.T) {
	w, s, mirror := newWriter(t)
	if err := w.Write(context.Background(), Entry{
		Actor: "admin", Action: "PUT /api/tls/providers/cf", Result: Success,
		Detail: map[string]any{
			"private_key": "-----BEGIN PRIVATE KEY-----abc",
			"credentials": map[string]any{"api_token": "cf-token-1", "zone": "z1"},
			"api_token":   "cf-token-2",
			"private":     "p-1",
			"domain":      "a.test",
		},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rows := readRows(t, s)
	file := readMirror(t, mirror)
	for _, secret := range []string{"BEGIN PRIVATE KEY", "cf-token-1", "cf-token-2", "p-1", "z1"} {
		if strings.Contains(rows[0].detail, secret) || strings.Contains(file, secret) {
			t.Errorf("secret %q reached the audit log: %s", secret, rows[0].detail)
		}
	}
	if !strings.Contains(rows[0].detail, "a.test") {
		t.Errorf("the domain was redacted too: %s", rows[0].detail)
	}
}
