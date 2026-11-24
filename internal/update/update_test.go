package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadStatusMissingFileIsIdle(t *testing.T) {
	st, err := ReadStatus(filepath.Join(t.TempDir(), "status.json"))
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if st.Phase != PhaseIdle || st.Active() {
		t.Fatalf("want idle and not active, got %+v", st)
	}
}

func TestReporterWritesPhasesProgressAndLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update", "status.json")
	rep, err := OpenReporter(path)
	if err != nil {
		t.Fatalf("OpenReporter: %v", err)
	}
	if err := rep.Phase(PhaseDownloading, "downloading v0.2.0"); err != nil {
		t.Fatalf("Phase: %v", err)
	}
	rep.Progress(50, 100)
	rep.Progress(100, 100) // the last report of a transfer is always written
	rep.Log("  downloading v0.2.0\n")
	rep.Log("")

	st, err := ReadStatus(path)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if st.Phase != PhaseDownloading || st.Done != 100 || st.Total != 100 {
		t.Fatalf("progress not persisted: %+v", st)
	}
	if len(st.Log) != 1 || st.Log[0] != "  downloading v0.2.0" {
		t.Fatalf("log = %q", st.Log)
	}
	if !st.Active() {
		t.Fatal("a downloading upgrade must count as active")
	}
}

func TestReporterKeepsLastLogLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	rep, err := OpenReporter(path)
	if err != nil {
		t.Fatalf("OpenReporter: %v", err)
	}
	for i := 0; i < maxLog+5; i++ {
		rep.Log(fmt.Sprintf("line %d", i))
	}
	st, _ := ReadStatus(path)
	if len(st.Log) != maxLog {
		t.Fatalf("kept %d lines, want %d", len(st.Log), maxLog)
	}
	if last := st.Log[len(st.Log)-1]; last != fmt.Sprintf("line %d", maxLog+4) {
		t.Fatalf("last line = %q", last)
	}
}

func TestFinishRecordsOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")

	rep, _ := OpenReporter(path)
	_ = rep.Phase(PhaseRolledBack, "the new binary did not become healthy")
	_ = rep.Finish(errors.New("upgrade: rolled back"))
	st, _ := ReadStatus(path)
	if st.Phase != PhaseRolledBack || st.Error != "upgrade: rolled back" {
		t.Fatalf("rollback outcome = %+v", st)
	}

	rep, _ = OpenReporter(path)
	_ = rep.Phase(PhaseDownloading, "x")
	_ = rep.Finish(errors.New("checksum mismatch"))
	st, _ = ReadStatus(path)
	if st.Phase != PhaseFailed || st.Error != "checksum mismatch" {
		t.Fatalf("failure outcome = %+v", st)
	}

	rep, _ = OpenReporter(path)
	_ = rep.Phase(PhaseDone, "")
	_ = rep.Finish(nil)
	st, _ = ReadStatus(path)
	if st.Phase != PhaseDone || !st.Terminal() {
		t.Fatalf("success outcome = %+v", st)
	}
}

func TestOpenReporterContinuesTheQueuedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	queued := Status{ID: "abc123", Tag: "v0.2.0", Phase: PhaseQueued, StartedAt: time.Unix(100, 0).UTC(), Log: []string{}}
	if err := writeStatus(path, queued); err != nil {
		t.Fatal(err)
	}
	rep, err := OpenReporter(path)
	if err != nil {
		t.Fatalf("OpenReporter: %v", err)
	}
	_ = rep.Phase(PhaseInstalling, "")
	st := rep.Status()
	if st.ID != "abc123" || st.Tag != "v0.2.0" || !st.StartedAt.Equal(queued.StartedAt) {
		t.Fatalf("the panel's record was not kept: %+v", st)
	}
}

// fakeGitHub serves the latest release for the running arch, with its checksum
// file. A non-200 status makes every request fail that way.
func fakeGitHub(t *testing.T, tag, notes string, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/panel/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     tag,
			"body":         notes,
			"html_url":     "https://example.test/releases/" + tag,
			"published_at": "2026-11-24T10:00:00Z",
			"assets": []map[string]any{
				{"name": "pbp-linux-" + runtime.GOARCH, "url": "https://example.test/bin", "size": 4321},
				{"name": "SHA256SUMS", "url": "https://example.test/sums", "size": 100},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCheckerReportsAnAvailableUpdateAndCachesIt(t *testing.T) {
	srv, hits := fakeGitHub(t, "v0.2.0", "## Changes\n- faster", http.StatusOK)
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	c := &Checker{Current: "v0.1.0", Repo: "owner/panel", BaseURL: srv.URL, Now: func() time.Time { return now }}

	info := c.Info(context.Background(), false)
	if !info.Available || info.Latest != "v0.2.0" || info.Current != "v0.1.0" {
		t.Fatalf("info = %+v", info)
	}
	if info.Changelog != "## Changes\n- faster" || !info.Installable || info.BinarySize != 4321 {
		t.Fatalf("release details missing: %+v", info)
	}

	c.Info(context.Background(), false)
	if hits.Load() != 1 {
		t.Fatalf("second call hit GitHub again: %d requests", hits.Load())
	}
	c.Info(context.Background(), true)
	if hits.Load() != 2 {
		t.Fatalf("force did not refresh: %d requests", hits.Load())
	}
}

func TestCheckerSameVersionIsNotAnUpdate(t *testing.T) {
	srv, _ := fakeGitHub(t, "v0.1.0", "", http.StatusOK)
	c := &Checker{Current: "v0.1.0", Repo: "owner/panel", BaseURL: srv.URL}
	if info := c.Info(context.Background(), false); info.Available {
		t.Fatalf("the running version is reported as available: %+v", info)
	}
}

func TestCheckerFailureIsReturnedAndNotCached(t *testing.T) {
	srv, hits := fakeGitHub(t, "", "", http.StatusNotFound)
	c := &Checker{Current: "v0.1.0", Repo: "owner/panel", BaseURL: srv.URL}

	info := c.Info(context.Background(), false)
	if info.Error == "" || info.Current != "v0.1.0" {
		t.Fatalf("a failed check must still report the running version and the error: %+v", info)
	}
	c.Info(context.Background(), false)
	if hits.Load() != 2 {
		t.Fatalf("a failed check was cached: %d requests", hits.Load())
	}
}

type fakeRunner struct {
	name string
	args []string
	err  error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.name, f.args = name, args
	return f.err
}

func newStarter(t *testing.T, r runner) *Starter {
	t.Helper()
	dir := t.TempDir()
	return &Starter{
		BinaryPath: filepath.Join(dir, "bin", "pbp"),
		StatusFile: StatusPath(filepath.Join(dir, "panel")),
		Runner:     r,
		Env: func(key string) (string, bool) {
			if key == "PB_REPO" {
				return "owner/panel", true
			}
			return "", false
		},
	}
}

func TestStartQueuesADetachedUpgradeUnit(t *testing.T) {
	runner := &fakeRunner{}
	s := newStarter(t, runner)

	st, err := s.Start(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st.Phase != PhaseQueued || st.Tag != "v0.2.0" || st.ID == "" {
		t.Fatalf("queued status = %+v", st)
	}
	if runner.name != "systemd-run" {
		t.Fatalf("ran %q, want systemd-run", runner.name)
	}
	joined := strings.Join(runner.args, " ")
	for _, want := range []string{
		"--unit=pbp-upgrade-" + st.ID,
		"--collect",
		"--setenv=PB_REPO=owner/panel",
		"-- " + s.BinaryPath + " upgrade --status-file " + s.StatusFile + " --version v0.2.0",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("systemd-run args %q missing %q", joined, want)
		}
	}
}

func TestStartRefusesAnInvalidTag(t *testing.T) {
	s := newStarter(t, &fakeRunner{})
	for _, tag := range []string{"", "v1; rm -rf /", "../../x", "a b"} {
		if _, err := s.Start(context.Background(), tag); !errors.Is(err, ErrInvalidTag) {
			t.Fatalf("tag %q: err = %v, want ErrInvalidTag", tag, err)
		}
	}
}

func TestStartRefusesWhileAnUpgradeIsRunning(t *testing.T) {
	s := newStarter(t, &fakeRunner{})
	if _, err := s.Start(context.Background(), "latest"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := s.Start(context.Background(), "latest"); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Start: err = %v, want ErrRunning", err)
	}
}

func TestStartRecordsAFailedLaunch(t *testing.T) {
	s := newStarter(t, &fakeRunner{err: errors.New("no systemd")})
	if _, err := s.Start(context.Background(), "v0.2.0"); err == nil {
		t.Fatal("want an error when systemd-run fails")
	}
	read, _ := s.Status()
	if read.Phase != PhaseFailed || !strings.Contains(read.Error, "no systemd") {
		t.Fatalf("failed launch not recorded: %+v", read)
	}
}

func TestStatusReportsAStalledUpgradeAsInterrupted(t *testing.T) {
	s := newStarter(t, &fakeRunner{})
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	if err := writeStatus(s.StatusFile, Status{ID: "x1", Phase: PhaseDownloading, UpdatedAt: now.Add(-time.Hour), Log: []string{}}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Phase != PhaseInterrupted || st.Active() {
		t.Fatalf("stalled upgrade = %+v", st)
	}
	// An interrupted record is terminal, so a new upgrade may start.
	if _, err := s.Start(context.Background(), "latest"); err != nil {
		t.Fatalf("Start after an interrupted upgrade: %v", err)
	}
}

func TestStatusPathLivesUnderThePanelTree(t *testing.T) {
	if got, want := StatusPath("/www/panel"), filepath.Join("/www/panel", "update", "status.json"); got != want {
		t.Fatalf("StatusPath = %s, want %s", got, want)
	}
}
