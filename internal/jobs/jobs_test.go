package jobs

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	return st
}

func newManager(t *testing.T, st *store.Store, opts ...Option) *Manager {
	t.Helper()
	m, err := New(st, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// runJob starts fn and waits for it, failing the test on any bookkeeping error.
func runJob(t *testing.T, m *Manager, kind string, fn Func) Job {
	t.Helper()
	job, err := m.Start(context.Background(), kind, fn)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done, err := m.Wait(ctx, job.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return done
}

func writeLines(n int) Func {
	return func(_ context.Context, out *Writer) error {
		for i := 1; i <= n; i++ {
			if err := out.Printf("line %d", i); err != nil {
				return err
			}
		}
		return nil
	}
}

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = ln.Text
	}
	return out
}

func TestStartRecordsRunningThenSucceeded(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))

	job, err := m.Start(context.Background(), "build", writeLines(2))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.State != StateRunning {
		t.Errorf("state right after Start = %q, want running", job.State)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done, err := m.Wait(ctx, job.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if done.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded (error %q)", done.State, done.Err)
	}
	if done.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", done.ExitCode)
	}
	if done.StartedAt == nil || done.FinishedAt == nil {
		t.Fatalf("started/finished not recorded: %+v", done)
	}
	if done.LastSeq != 2 {
		t.Errorf("last_seq = %d, want 2", done.LastSeq)
	}
}

func TestFailedJobRecordsError(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))

	done := runJob(t, m, "build", func(_ context.Context, out *Writer) error {
		if err := out.Line("compiling"); err != nil {
			return err
		}
		return errors.New("nginx -t: unexpected end of file")
	})

	if done.State != StateFailed {
		t.Fatalf("state = %q, want failed", done.State)
	}
	if done.ExitCode != exitFailed {
		t.Errorf("exit code = %d, want %d", done.ExitCode, exitFailed)
	}
	if !strings.Contains(done.Err, "unexpected end of file") {
		t.Errorf("error = %q, want the runner's message verbatim", done.Err)
	}
}

// The client's last seen seq is replayed from the database and nothing before it
// is sent again, which is what makes a reloaded page pick up where it left off.
func TestEventsResumeFromLastEventID(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))
	job := runJob(t, m, "build", writeLines(5))

	rec := getEvents(t, m, job.ID, "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	ids, data := parseSSE(t, rec.Body.String())
	if strings.Join(ids, ",") != "3,4,5" {
		t.Fatalf("event ids = %v, want 3,4,5: the replay resent lines the client had seen", ids)
	}
	if strings.Join(data, ",") != "line 3,line 4,line 5" {
		t.Fatalf("data = %v, want the three later lines", data)
	}
}

func TestEventsWithoutLastEventIDReplaysEverything(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))
	job := runJob(t, m, "build", writeLines(3))

	ids, _ := parseSSE(t, getEvents(t, m, job.ID, "").Body.String())
	if strings.Join(ids, ",") != "1,2,3" {
		t.Fatalf("event ids = %v, want 1,2,3", ids)
	}
}

func TestEventsResumePastTheEndSendsNoLines(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))
	job := runJob(t, m, "build", writeLines(2))

	ids, _ := parseSSE(t, getEvents(t, m, job.ID, "2").Body.String())
	if len(ids) != 0 {
		t.Fatalf("event ids = %v, want none: everything was already delivered", ids)
	}
}

// Each line must reach the client before the job ends: a handler that buffered
// would deliver nothing until the run finished.
func TestEventsStreamLiveLinesBeforeJobEnds(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))

	gate := make(chan struct{})
	job, err := m.Start(context.Background(), "build", func(ctx context.Context, out *Writer) error {
		if err := out.Line("first"); err != nil {
			return err
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		return out.Line("second")
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	srv := httptest.NewServer(eventsMux(m))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/jobs/" + job.ID + "/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := bufio.NewReader(resp.Body)
	if got := readEvent(t, reader); got != "first" {
		t.Fatalf("first event = %q, want %q before the job finished", got, "first")
	}

	close(gate)
	if got := readEvent(t, reader); got != "second" {
		t.Fatalf("second event = %q, want %q", got, "second")
	}
	if got := readEvent(t, reader); got != "event: end" {
		t.Fatalf("trailing frame = %q, want the end event", got)
	}
}

func TestCancelStopsJobAndRecordsCanceled(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))

	started := make(chan struct{})
	job, err := m.Start(context.Background(), "build", func(ctx context.Context, out *Writer) error {
		close(started)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
				if err := out.Line("tick"); err != nil {
					return err
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started

	if err := m.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done, err := m.Wait(ctx, job.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if done.State != StateCanceled {
		t.Fatalf("state = %q, want canceled", done.State)
	}
	if done.ExitCode != exitCanceled {
		t.Errorf("exit code = %d, want %d", done.ExitCode, exitCanceled)
	}
	if done.FinishedAt == nil {
		t.Error("finished_at was not recorded")
	}

	if err := m.Cancel(job.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second Cancel = %v, want ErrNotRunning", err)
	}
}

func TestCancelUnknownJobIsNotRunning(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))
	if err := m.Cancel("no-such-job"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Cancel = %v, want ErrNotRunning", err)
	}
}

// A job row left `running` by a process that died must come back interrupted:
// its goroutine is gone, so nothing will ever append to it again.
func TestRestartMarksRunningJobInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")

	first := openStore(t, path)
	const insert = `INSERT INTO jobs (id, kind, state, created_at, started_at)
	                VALUES ('deadbeef', 'build', 'running', '2026-11-16T10:00:00.000Z', '2026-11-16T10:00:00.000Z')`
	if _, err := first.DB().Exec(insert); err != nil {
		t.Fatalf("seed running job: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	// Reopen the database the way the daemon does at boot.
	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = second.Close() }()

	m := newManager(t, second)
	job, err := m.Get(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.State != StateInterrupted {
		t.Fatalf("state after restart = %q, want interrupted", job.State)
	}
	if job.FinishedAt == nil {
		t.Error("finished_at was not recorded on recovery")
	}
	if job.Err == "" {
		t.Error("error is empty: the recovery reason was not recorded")
	}
}

func TestOutputCapTruncatesOldestFirst(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")), WithMaxLines(3))
	job := runJob(t, m, "build", writeLines(5))

	stored, err := m.Replay(context.Background(), job.ID, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got := strings.Join(texts(stored), ","); got != "line 3,line 4,line 5" {
		t.Fatalf("stored lines = %q, want the three newest", got)
	}
	if len(stored) != 3 || stored[0].Seq != 3 || stored[1].Seq != 4 || stored[2].Seq != 5 {
		t.Fatalf("stored seqs = %+v, want 3,4,5", stored)
	}

	final, err := m.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !final.Truncated {
		t.Error("truncated = false, want true")
	}
	if final.Dropped != 2 {
		t.Errorf("dropped = %d, want 2", final.Dropped)
	}
	// last_seq must keep counting past the cap, or Last-Event-ID stops matching.
	if final.LastSeq != 5 {
		t.Errorf("last_seq = %d, want 5", final.LastSeq)
	}
}

func TestEventsResumeFromDroppedLineReportsTruncation(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")), WithMaxLines(2))
	job := runJob(t, m, "build", writeLines(4))

	// A client that saw seq 1 reconnects after the cap dropped it. The replay
	// starts at the oldest surviving line, and the truncated event says so.
	body := getEvents(t, m, job.ID, "1").Body.String()
	if !strings.Contains(body, "event: truncated") {
		t.Fatalf("stream has no truncated event:\n%s", body)
	}
	ids, _ := parseSSE(t, body)
	if strings.Join(ids, ",") != "3,4" {
		t.Fatalf("event ids = %v, want 3,4", ids)
	}
}

func TestEventsUnknownJobIs404(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))
	rec := getEvents(t, m, "nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestEventsRejectsNonNumericLastEventID(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))
	job := runJob(t, m, "build", writeLines(1))
	rec := getEvents(t, m, job.ID, "not-a-number")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A slow subscriber must not stall the job: it is dropped and replays instead.
func TestSlowSubscriberIsDroppedNotBlocking(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))

	gate := make(chan struct{})
	job, err := m.Start(context.Background(), "build", func(ctx context.Context, out *Writer) error {
		<-gate
		for i := range subBuffer * 4 {
			if err := out.Printf("line %d", i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	live, unsubscribe, err := m.Subscribe(job.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()

	close(gate)

	// Never read `live`, so its buffer fills; the job must still finish.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done, err := m.Wait(ctx, job.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if done.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded: a slow subscriber stalled the job", done.State)
	}

	stored, err := m.Replay(context.Background(), job.ID, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(stored) != subBuffer*4 {
		t.Fatalf("stored %d lines, want %d: output was lost with the subscriber", len(stored), subBuffer*4)
	}

	// The lagging subscriber was dropped, so its channel is closed with fewer
	// lines than the job produced: it must replay from the database instead.
	drained := 0
	for range live {
		drained++
	}
	if drained >= subBuffer*4 {
		t.Fatalf("subscriber received %d lines, want fewer: it was never dropped", drained)
	}
}

func TestWriterSplitsMultilineOutput(t *testing.T) {
	m := newManager(t, openStore(t, filepath.Join(t.TempDir(), "panel.db")))
	job := runJob(t, m, "build", func(_ context.Context, out *Writer) error {
		return out.Line("one\r\ntwo\nthree\n")
	})

	stored, err := m.Replay(context.Background(), job.ID, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got := strings.Join(texts(stored), ","); got != "one,two,three" {
		t.Fatalf("lines = %q, want one,two,three", got)
	}
}

func TestNewRejectsZeroMaxLines(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "panel.db"))
	if _, err := New(st, WithMaxLines(0)); err == nil {
		t.Fatal("New accepted a cap of 0")
	}
}

func TestStateTerminal(t *testing.T) {
	for _, s := range []State{StateSucceeded, StateFailed, StateCanceled, StateInterrupted} {
		if !s.Terminal() {
			t.Errorf("%q is not terminal", s)
		}
	}
	for _, s := range []State{StateQueued, StateRunning} {
		if s.Terminal() {
			t.Errorf("%q is terminal", s)
		}
	}
}

// --- helpers ---

func eventsMux(m *Manager) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/jobs/{id}/events", m.ServeEvents)
	return mux
}

func getEvents(t *testing.T, m *Manager, id, lastEventID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+id+"/events", nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	rec := httptest.NewRecorder()
	eventsMux(m).ServeHTTP(rec, req)
	return rec
}

// parseSSE returns the `id:` and `data:` fields of each data frame, in order.
// Named frames (truncated, end) are skipped: they carry no line.
func parseSSE(t *testing.T, body string) (ids, data []string) {
	t.Helper()
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var (
			id    string
			val   string
			named bool
			has   bool
		)
		for _, ln := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(ln, "id: "):
				id = strings.TrimPrefix(ln, "id: ")
			case strings.HasPrefix(ln, "event: "):
				named = true
			case strings.HasPrefix(ln, "data: "):
				val = strings.TrimPrefix(ln, "data: ")
				has = true
			}
		}
		if has && !named {
			ids = append(ids, id)
			data = append(data, val)
		}
	}
	return ids, data
}

// readEvent reads one SSE frame: its data for a plain line frame, or
// "event: <name>" for a named frame.
func readEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var (
		event string
		data  string
	)
	for {
		ln, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		ln = strings.TrimRight(ln, "\r\n")
		switch {
		case ln == "":
			if event != "" {
				return "event: " + event
			}
			return data
		case strings.HasPrefix(ln, "event: "):
			event = strings.TrimPrefix(ln, "event: ")
		case strings.HasPrefix(ln, "data: "):
			data = strings.TrimPrefix(ln, "data: ")
		}
	}
}
