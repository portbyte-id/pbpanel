package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/audit"
	"github.com/portbyte-id/server-panel/internal/cron"
	"github.com/portbyte-id/server-panel/internal/logs"
	"github.com/portbyte-id/server-panel/internal/sysinfo"
)

// --- fakes -------------------------------------------------------------------

type fakeSysinfo struct {
	mu       sync.Mutex
	history  []sysinfo.Snapshot
	services []sysinfo.Service
	ctlErr   error
	ctlCalls []string
	ch       chan sysinfo.Snapshot
	canceled chan struct{}
}

func newFakeSysinfo() *fakeSysinfo {
	return &fakeSysinfo{ch: make(chan sysinfo.Snapshot, 4), canceled: make(chan struct{})}
}

func (f *fakeSysinfo) Interval() time.Duration { return 5 * time.Second }
func (f *fakeSysinfo) History() []sysinfo.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sysinfo.Snapshot(nil), f.history...)
}
func (f *fakeSysinfo) Latest() (sysinfo.Snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.history) == 0 {
		return sysinfo.Snapshot{}, false
	}
	return f.history[len(f.history)-1], true
}
func (f *fakeSysinfo) Subscribe() (<-chan sysinfo.Snapshot, func()) {
	var once sync.Once
	return f.ch, func() { once.Do(func() { close(f.canceled) }) }
}
func (f *fakeSysinfo) List(context.Context) []sysinfo.Service { return f.services }
func (f *fakeSysinfo) Control(_ context.Context, name string, a sysinfo.Action) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctlCalls = append(f.ctlCalls, name+" "+string(a))
	return f.ctlErr
}

func snapAt(sec int, cpu float64) sysinfo.Snapshot {
	return sysinfo.Snapshot{Time: time.Date(2026, 11, 17, 10, 0, sec, 0, time.UTC), Rates: true, CPUPercent: cpu,
		CPUCores: []float64{cpu}, Disks: []sysinfo.DiskStat{}, Net: []sysinfo.NetRate{}}
}

type fakeLogs struct {
	sources    []logs.Source
	listErr    error
	resolveErr error
	tailLines  []string
	tailErr    error
	tailOpts   logs.TailOptions
	hold       bool // keep the tail open until its context ends
	tailDone   chan struct{}
	matches    []logs.Match
	searchErr  error
	gotQuery   logs.Query
	gotOpts    logs.SearchOptions
}

func (f *fakeLogs) List(context.Context) ([]logs.Source, error) { return f.sources, f.listErr }
func (f *fakeLogs) Resolve(_ context.Context, id string) (logs.Source, error) {
	if f.resolveErr != nil {
		return logs.Source{}, f.resolveErr
	}
	for _, s := range f.sources {
		if s.ID == id {
			return s, nil
		}
	}
	return logs.Source{}, logs.ErrNotFound
}
func (f *fakeLogs) TailChan(ctx context.Context, _ string, opts logs.TailOptions) (<-chan string, <-chan error) {
	f.tailOpts = opts
	lines := make(chan string, 64)
	errc := make(chan error, 1)
	go func() {
		for _, l := range f.tailLines {
			lines <- l
		}
		err := f.tailErr
		if f.hold {
			<-ctx.Done()
			err = ctx.Err()
			if f.tailDone != nil {
				close(f.tailDone)
			}
		}
		errc <- err
		close(errc)
		close(lines)
	}()
	return lines, errc
}
func (f *fakeLogs) Search(ctx context.Context, id string, q logs.Query, o logs.SearchOptions, emit func(logs.Match) error) (logs.SearchStats, error) {
	f.gotQuery, f.gotOpts = q, o
	if _, err := f.Resolve(ctx, id); err != nil {
		return logs.SearchStats{}, err
	}
	if f.searchErr != nil {
		return logs.SearchStats{}, f.searchErr
	}
	n := 0
	for _, m := range f.matches {
		if err := emit(m); err != nil {
			return logs.SearchStats{}, err
		}
		n++
	}
	return logs.SearchStats{Files: 1, BytesScanned: 10, Matches: n}, nil
}

type fakeCron struct {
	jobs      []cron.Job
	runs      map[int64][]cron.Run
	err       error
	gotSpec   cron.JobSpec
	gotID     int64
	gotEnable *bool
	runRes    cron.RunNowResult
}

func (f *fakeCron) List(context.Context) ([]cron.Job, error) { return f.jobs, f.err }
func (f *fakeCron) find(id int64) (cron.Job, error) {
	if f.err != nil {
		return cron.Job{}, f.err
	}
	for _, j := range f.jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return cron.Job{}, cron.ErrNotFound
}
func (f *fakeCron) Get(_ context.Context, id int64) (cron.Job, error) { return f.find(id) }
func (f *fakeCron) Create(_ context.Context, s cron.JobSpec) (cron.Job, error) {
	f.gotSpec = s
	if f.err != nil {
		return cron.Job{}, f.err
	}
	return cron.Job{ID: 9, Name: s.Name, Schedule: s.Schedule, Kind: s.Kind, User: s.User, Argv: s.Argv, Enabled: true}, nil
}
func (f *fakeCron) Update(_ context.Context, id int64, s cron.JobSpec) (cron.Job, error) {
	f.gotID, f.gotSpec = id, s
	j, err := f.find(id)
	if err != nil {
		return cron.Job{}, err
	}
	j.Schedule = s.Schedule
	return j, nil
}
func (f *fakeCron) Delete(_ context.Context, id int64) error {
	f.gotID = id
	_, err := f.find(id)
	return err
}
func (f *fakeCron) SetEnabled(_ context.Context, id int64, on bool) (cron.Job, error) {
	f.gotID, f.gotEnable = id, &on
	j, err := f.find(id)
	j.Enabled = on
	return j, err
}
func (f *fakeCron) RunNow(_ context.Context, id int64) (cron.RunNowResult, error) {
	f.gotID = id
	if _, err := f.find(id); err != nil {
		return cron.RunNowResult{}, err
	}
	return f.runRes, nil
}
func (f *fakeCron) Runs(_ context.Context, id int64, limit int) ([]cron.Run, error) {
	if _, err := f.find(id); err != nil {
		return nil, err
	}
	r := f.runs[id]
	if limit > 0 && limit < len(r) {
		r = r[:limit]
	}
	return r, nil
}
func (f *fakeCron) GetRun(_ context.Context, runID int64) (cron.Run, error) {
	for _, rs := range f.runs {
		for _, r := range rs {
			if r.ID == runID {
				return r, nil
			}
		}
	}
	return cron.Run{}, cron.ErrRunNotFound
}

func m7Harness(t *testing.T, set func(*Deps)) (*harness, session) {
	t.Helper()
	h := newHarness(t)
	set(&h.deps)
	var err error
	if h.handler, err = New(h.deps); err != nil {
		t.Fatal(err)
	}
	return h, h.login()
}

func detailOf(e audit.Entry) map[string]any {
	m, _ := e.Detail.(map[string]any)
	return m
}

func lastAudit(t *testing.T, h *harness) audit.Entry {
	t.Helper()
	if len(h.audit.entries) == 0 {
		t.Fatal("no audit entry was written")
	}
	return h.audit.entries[len(h.audit.entries)-1]
}

// sseClient talks to a real server, because a stream cannot be read from a
// recorder while the handler is still writing to it.
type sseClient struct {
	t      *testing.T
	cancel context.CancelFunc
	resp   *http.Response
	rd     *bufio.Reader
}

func openSSE(t *testing.T, h *harness, s session, path string) *sseClient {
	t.Helper()
	srv := httptest.NewServer(h.handler)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: s.cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return &sseClient{t: t, cancel: cancel, resp: resp, rd: bufio.NewReader(resp.Body)}
}

// next returns the next event, skipping comments.
func (c *sseClient) next() (name, data string) {
	c.t.Helper()
	for {
		line, err := c.rd.ReadString('\n')
		if err != nil {
			c.t.Fatalf("reading the stream: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && name != "":
			return name, data
		}
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen after the client left", what)
	}
}

// --- sysinfo and services ------------------------------------------------------

func TestM7RoutesUnavailableWithoutDeps(t *testing.T) {
	h := newHarness(t)
	s := h.login()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/sysinfo"},
		{http.MethodGet, "/api/sysinfo/stream"},
		{http.MethodGet, "/api/services"},
		{http.MethodPost, "/api/services/panel/restart"},
		{http.MethodGet, "/api/logs"},
		{http.MethodGet, "/api/logs/x/tail"},
		{http.MethodGet, "/api/logs/x/search?q=a"},
		{http.MethodGet, "/api/cron"},
		{http.MethodPost, "/api/cron"},
		{http.MethodGet, "/api/cron/1/runs/2"},
	} {
		rec := h.do(c.method, c.path, s.cookie, s.csrf, map[string]any{})
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
			continue
		}
		if env := h.errorEnvelopeOf(rec); env.Error != errUnavailable {
			t.Errorf("%s %s error = %q", c.method, c.path, env.Error)
		}
	}
}

func TestSysinfoGet(t *testing.T) {
	f := newFakeSysinfo()
	h, s := m7Harness(t, func(d *Deps) { d.Sysinfo = f })

	rec := h.do(http.MethodGet, "/api/sysinfo", s.cookie, "", nil)
	var empty struct {
		Interval float64            `json:"interval_seconds"`
		Latest   *sysinfo.Snapshot  `json:"latest"`
		History  []sysinfo.Snapshot `json:"history"`
	}
	h.decode(rec, &empty)
	if rec.Code != 200 || empty.Latest != nil || empty.History == nil || len(empty.History) != 0 || empty.Interval != 5 {
		t.Fatalf("before the first sample: %d %s", rec.Code, rec.Body.String())
	}

	f.history = []sysinfo.Snapshot{snapAt(0, 1), snapAt(5, 2), snapAt(10, 3)}
	rec = h.do(http.MethodGet, "/api/sysinfo", s.cookie, "", nil)
	var got struct {
		Latest  *sysinfo.Snapshot  `json:"latest"`
		History []sysinfo.Snapshot `json:"history"`
	}
	h.decode(rec, &got)
	if got.Latest == nil || got.Latest.CPUPercent != 3 || len(got.History) != 3 || got.History[0].CPUPercent != 1 {
		t.Fatalf("got %s", rec.Body.String())
	}

	rec = h.do(http.MethodGet, "/api/sysinfo?limit=2", s.cookie, "", nil)
	h.decode(rec, &got)
	if len(got.History) != 2 || got.History[0].CPUPercent != 2 {
		t.Fatalf("limit=2: %s", rec.Body.String())
	}
	if rec := h.do(http.MethodGet, "/api/sysinfo?limit=-1", s.cookie, "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=-1: %d", rec.Code)
	}

	var raw map[string]map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"x":`+firstSnapshotJSON(t, rec)+`}`), &raw)
	for _, k := range []string{"time", "rates", "cpu_percent", "cpu_cores", "load", "memory", "uptime_seconds", "disks", "net"} {
		if _, ok := raw["x"][k]; !ok {
			t.Errorf("snapshot JSON lacks %q", k)
		}
	}
}

func firstSnapshotJSON(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var r struct {
		History []json.RawMessage `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || len(r.History) == 0 {
		t.Fatalf("no history in %s", rec.Body.String())
	}
	return string(r.History[0])
}

func TestSysinfoStream(t *testing.T) {
	f := newFakeSysinfo()
	f.history = []sysinfo.Snapshot{snapAt(0, 11)}
	h, s := m7Harness(t, func(d *Deps) { d.Sysinfo = f })

	c := openSSE(t, h, s, "/api/sysinfo/stream")
	if ct := c.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	name, data := c.next()
	var first sysinfo.Snapshot
	if err := json.Unmarshal([]byte(data), &first); err != nil || name != "snapshot" || first.CPUPercent != 11 {
		t.Fatalf("first event %q %q (%v)", name, data, err)
	}

	f.ch <- snapAt(5, 22)
	name, data = c.next()
	var second sysinfo.Snapshot
	if err := json.Unmarshal([]byte(data), &second); err != nil || name != "snapshot" || second.CPUPercent != 22 {
		t.Fatalf("second event %q %q (%v)", name, data, err)
	}

	c.cancel()
	waitClosed(t, f.canceled, "the unsubscribe")
}

func TestServicesList(t *testing.T) {
	f := newFakeSysinfo()
	f.services = []sysinfo.Service{
		{Name: "panel", Kind: sysinfo.KindSystemd, State: sysinfo.StateRunning, Detail: "active", Actions: []sysinfo.Action{"start", "restart"}},
		{Name: "containers", Kind: sysinfo.KindContainer, State: sysinfo.StateUnknown},
	}
	h, s := m7Harness(t, func(d *Deps) { d.Sysinfo = f })
	rec := h.do(http.MethodGet, "/api/services", s.cookie, "", nil)
	var got struct {
		Services []sysinfo.Service `json:"services"`
	}
	h.decode(rec, &got)
	if rec.Code != 200 || len(got.Services) != 2 || got.Services[0].Name != "panel" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"actions":[]`) {
		t.Fatalf("a row without controls must carry an empty actions array: %s", rec.Body.String())
	}
}

func TestServicesControlAuditsAndMaps(t *testing.T) {
	f := newFakeSysinfo()
	h, s := m7Harness(t, func(d *Deps) { d.Sysinfo = f })

	rec := h.do(http.MethodPost, "/api/services/app:site/restart", s.cookie, s.csrf, map[string]any{})
	if rec.Code != http.StatusNoContent || len(f.ctlCalls) != 1 || f.ctlCalls[0] != "app:site restart" {
		t.Fatalf("got %d calls %v", rec.Code, f.ctlCalls)
	}
	e := lastAudit(t, h)
	if e.Action != "POST /api/services/app:site/restart" || e.Target != "service:app:site" || e.Result != audit.Success {
		t.Fatalf("audit %+v", e)
	}
	if detailOf(e)["action"] != "restart" {
		t.Fatalf("audit detail %+v", detailOf(e))
	}

	if rec := h.do(http.MethodPost, "/api/services/panel/explode", s.cookie, s.csrf, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action: %d", rec.Code)
	}
	if len(f.ctlCalls) != 1 {
		t.Fatal("an unknown action reached the service")
	}
	if rec := h.doRaw(http.MethodPost, "/api/services/panel/start", s.cookie, s.csrf, `{"surprise":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown body field: %d", rec.Code)
	}

	for _, c := range []struct {
		err    error
		status int
		class  string
	}{
		{fmt.Errorf("%w: %q", sysinfo.ErrUnknownService, "x"), 404, errNotFound},
		{sysinfo.ErrUnknownAction, 400, errBadRequest},
		{fmt.Errorf("%w: panel cannot be stopped", sysinfo.ErrNotAllowed), 403, errNotAllowed},
		{errors.New("sysinfo: systemctl restart docker.service: exit status 1: Unit not found"), 502, errControlFailed},
	} {
		f.ctlErr = c.err
		rec := h.do(http.MethodPost, "/api/services/docker/restart", s.cookie, s.csrf, map[string]any{})
		env := h.errorEnvelopeOf(rec)
		if rec.Code != c.status || env.Error != c.class {
			t.Errorf("%v: got %d %+v, want %d %s", c.err, rec.Code, env, c.status, c.class)
		}
		if c.status == 502 && !strings.Contains(env.Output, "Unit not found") {
			t.Errorf("a failed control should show the command's output: %+v", env)
		}
		if e := lastAudit(t, h); e.Result != audit.Failure {
			t.Errorf("%v: audit result %q", c.err, e.Result)
		}
	}
}

// --- logs ---------------------------------------------------------------------

func twoSources() []logs.Source {
	return []logs.Source{
		{ID: "panel:audit", Label: "Panel audit trail", Kind: logs.KindAudit, Path: "/www/panel/logs/audit.log"},
		{ID: "docker:panel-nginx", Label: "panel-nginx", Kind: logs.KindDocker, Container: "panel-nginx"},
	}
}

func TestLogsListHidesPaths(t *testing.T) {
	f := &fakeLogs{sources: twoSources(), listErr: errors.New("a provider failed")}
	h, s := m7Harness(t, func(d *Deps) { d.Logs = f })
	rec := h.do(http.MethodGet, "/api/logs", s.cookie, "", nil)
	if rec.Code != 200 {
		t.Fatalf("a failing provider must not fail the list: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Sources []map[string]string `json:"sources"`
	}
	h.decode(rec, &got)
	if len(got.Sources) != 2 || got.Sources[0]["id"] != "panel:audit" || got.Sources[0]["kind"] != "audit" || got.Sources[0]["label"] == "" {
		t.Fatalf("got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/www/") || strings.Contains(rec.Body.String(), "container") {
		t.Fatalf("a server path or container name leaked: %s", rec.Body.String())
	}
}

func TestLogsTailNotFollowed(t *testing.T) {
	f := &fakeLogs{sources: twoSources(), tailLines: []string{"one", "two"}}
	h, s := m7Harness(t, func(d *Deps) { d.Logs = f })

	rec := h.do(http.MethodGet, "/api/logs/panel:audit/tail?lines=50", s.cookie, "", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	want := "event: line\ndata: {\"line\":\"one\"}\n\nevent: line\ndata: {\"line\":\"two\"}\n\nevent: end\ndata: {}\n\n"
	if rec.Body.String() != want {
		t.Fatalf("body %q, want %q", rec.Body.String(), want)
	}
	if f.tailOpts.Lines != 50 || f.tailOpts.Follow {
		t.Fatalf("options %+v", f.tailOpts)
	}

	rec = h.do(http.MethodGet, "/api/logs/panel:audit/tail?follow=1", s.cookie, "", nil)
	if rec.Code != 200 || !f.tailOpts.Follow {
		t.Fatalf("follow=1: %d %+v", rec.Code, f.tailOpts)
	}
}

func TestLogsTailErrors(t *testing.T) {
	f := &fakeLogs{sources: twoSources()}
	h, s := m7Harness(t, func(d *Deps) { d.Logs = f })

	if rec := h.do(http.MethodGet, "/api/logs/nope/tail", s.cookie, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
	for _, q := range []string{"lines=abc", "lines=-3", "follow=maybe"} {
		rec := h.do(http.MethodGet, "/api/logs/panel:audit/tail?"+q, s.cookie, "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}

	f.tailErr = fmt.Errorf("%w: no such file", logs.ErrUnavailable)
	rec := h.do(http.MethodGet, "/api/logs/panel:audit/tail", s.cookie, "", nil)
	if env := h.errorEnvelopeOf(rec); rec.Code != http.StatusConflict || env.Error != errSourceUnavailable {
		t.Fatalf("unavailable before any line: %d %+v", rec.Code, env)
	}

	f.tailErr = errors.New("read /secret/path: boom")
	rec = h.do(http.MethodGet, "/api/logs/panel:audit/tail", s.cookie, "", nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("internal failure before any line: %d %s", rec.Code, rec.Body.String())
	}

	f.tailLines, f.tailErr = []string{"a"}, errors.New("read /secret/path: boom")
	rec = h.do(http.MethodGet, "/api/logs/panel:audit/tail", s.cookie, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "event: error") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("failure after a line must end the stream with an error event: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLogsTailFollowStopsWhenClientLeaves(t *testing.T) {
	f := &fakeLogs{sources: twoSources(), tailLines: []string{"first"}, hold: true, tailDone: make(chan struct{})}
	h, s := m7Harness(t, func(d *Deps) { d.Logs = f })

	c := openSSE(t, h, s, "/api/logs/panel:audit/tail?follow=1")
	if name, data := c.next(); name != "line" || data != `{"line":"first"}` {
		t.Fatalf("got %q %q", name, data)
	}
	c.cancel()
	waitClosed(t, f.tailDone, "the tail's cancellation")
}

func TestLogsSearch(t *testing.T) {
	f := &fakeLogs{sources: twoSources(), matches: []logs.Match{
		{File: "audit.log.1", Line: 4, Text: "error one"},
		{File: "audit.log", Line: 2, Partial: true, Text: "error two"},
	}}
	h, s := m7Harness(t, func(d *Deps) { d.Logs = f })

	rec := h.do(http.MethodGet, "/api/logs/panel:audit/search?q=error&regex=1&ci=true&last_mb=8&limit=20", s.cookie, "", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if f.gotQuery != (logs.Query{Pattern: "error", Regex: true, CaseInsensitive: true}) || f.gotOpts.LastMB != 8 || f.gotOpts.Limit != 20 {
		t.Fatalf("query %+v opts %+v", f.gotQuery, f.gotOpts)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 2 matches and a done line, got %q", rec.Body.String())
	}
	var m1, m2, done map[string]any
	for i, dst := range []*map[string]any{&m1, &m2, &done} {
		if err := json.Unmarshal([]byte(lines[i]), dst); err != nil {
			t.Fatalf("line %d is not JSON: %q", i, lines[i])
		}
	}
	if m1["type"] != "match" || m1["file"] != "audit.log.1" || m1["line"] != float64(4) || m1["text"] != "error one" {
		t.Fatalf("first match %v", m1)
	}
	if m2["partial"] != true {
		t.Fatalf("second match %v", m2)
	}
	stats, _ := done["stats"].(map[string]any)
	if done["type"] != "done" || stats["matches"] != float64(2) {
		t.Fatalf("done line %v", done)
	}
}

func TestLogsSearchErrors(t *testing.T) {
	f := &fakeLogs{sources: twoSources()}
	h, s := m7Harness(t, func(d *Deps) { d.Logs = f })

	for _, p := range []string{
		"/api/logs/panel:audit/search",
		"/api/logs/panel:audit/search?q=",
		"/api/logs/panel:audit/search?q=a&last_mb=x",
		"/api/logs/panel:audit/search?q=a&limit=-1",
		"/api/logs/panel:audit/search?q=a&regex=2",
	} {
		if rec := h.do(http.MethodGet, p, s.cookie, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := h.do(http.MethodGet, "/api/logs/nope/search?q=a", s.cookie, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}

	f.searchErr = fmt.Errorf("%w: bad regexp", logs.ErrBadQuery)
	rec := h.do(http.MethodGet, "/api/logs/panel:audit/search?q=(&regex=1", s.cookie, "", nil)
	if env := h.errorEnvelopeOf(rec); rec.Code != http.StatusBadRequest || env.Error != errInvalidInput || env.Field != "q" {
		t.Fatalf("bad pattern: %d %+v", rec.Code, env)
	}
	f.searchErr = logs.ErrUnavailable
	if rec := h.do(http.MethodGet, "/api/logs/panel:audit/search?q=a", s.cookie, "", nil); rec.Code != http.StatusConflict {
		t.Errorf("unavailable: %d", rec.Code)
	}
}

// --- cron ----------------------------------------------------------------------

func cronFixture() *fakeCron {
	started := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	fin := started.Add(time.Second)
	return &fakeCron{
		jobs: []cron.Job{
			{ID: 1, Name: "backup", Schedule: "0 3 * * *", Kind: cron.KindArgv, User: "root", Argv: []string{"/usr/bin/true"}, Enabled: true},
			{ID: 2, Name: "off", Schedule: "* * * * *", Kind: cron.KindScript, User: "root", ScriptPath: "/opt/x.sh"},
		},
		runs: map[int64][]cron.Run{
			1: {{ID: 11, JobID: 1, Source: cron.SourceSchedule, StartedAt: started, FinishedAt: &fin, Status: cron.StatusSucceeded, OutputTail: "done"}},
		},
	}
}

func TestCronListAndGet(t *testing.T) {
	f := cronFixture()
	h, s := m7Harness(t, func(d *Deps) { d.Cron = f })

	rec := h.do(http.MethodGet, "/api/cron", s.cookie, "", nil)
	var list struct {
		Jobs []map[string]any `json:"jobs"`
	}
	h.decode(rec, &list)
	if rec.Code != 200 || len(list.Jobs) != 2 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	b, off := list.Jobs[0], list.Jobs[1]
	if b["name"] != "backup" || b["next_run"] == nil || b["schedule"] != "0 3 * * *" {
		t.Fatalf("enabled job %v", b)
	}
	last, _ := b["last_run"].(map[string]any)
	if last["status"] != "succeeded" || last["id"] != float64(11) {
		t.Fatalf("last_run %v", b["last_run"])
	}
	if _, leaked := last["output_tail"]; leaked {
		t.Fatalf("the list must not carry run output: %v", last)
	}
	if off["next_run"] != nil || off["last_run"] != nil {
		t.Fatalf("disabled job without runs: %v", off)
	}

	rec = h.do(http.MethodGet, "/api/cron/1", s.cookie, "", nil)
	var one map[string]any
	h.decode(rec, &one)
	if rec.Code != 200 || one["id"] != float64(1) || one["next_run"] == nil {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/api/cron/99", "/api/cron/abc", "/api/cron/0"} {
		if rec := h.do(http.MethodGet, p, s.cookie, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestCronCreateUpdateDeleteAudited(t *testing.T) {
	f := cronFixture()
	h, s := m7Harness(t, func(d *Deps) { d.Cron = f })

	body := map[string]any{"name": "newjob", "schedule": "*/5 * * * *", "kind": "argv", "user": "www",
		"argv": []string{"/bin/echo", "hi"}, "timeout_seconds": 30, "overlap": "queue", "retention": 5, "enabled": false}
	rec := h.do(http.MethodPost, "/api/cron", s.cookie, s.csrf, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	sp := f.gotSpec
	if sp.Name != "newjob" || sp.Kind != cron.KindArgv || sp.User != "www" || len(sp.Argv) != 2 || sp.TimeoutSeconds != 30 ||
		sp.Overlap != cron.OverlapQueue || sp.Retention != 5 || sp.Enabled == nil || *sp.Enabled {
		t.Fatalf("decoded wrong: %+v", sp)
	}
	if e := lastAudit(t, h); e.Action != "POST /api/cron" || e.Target != "cron:newjob" || e.Result != audit.Success {
		t.Fatalf("audit %+v", e)
	}

	if rec := h.doRaw(http.MethodPost, "/api/cron", s.cookie, s.csrf, `{"name":"x","bogus":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}

	rec = h.do(http.MethodPut, "/api/cron/1", s.cookie, s.csrf, map[string]any{"schedule": "1 1 * * *", "kind": "argv", "user": "root", "argv": []string{"/bin/true"}})
	if rec.Code != 200 || f.gotID != 1 || f.gotSpec.Schedule != "1 1 * * *" {
		t.Fatalf("update: %d %+v", rec.Code, f.gotSpec)
	}
	if e := lastAudit(t, h); e.Action != "PUT /api/cron/1" || e.Target != "cron:1" {
		t.Fatalf("audit %+v", e)
	}

	if rec := h.do(http.MethodDelete, "/api/cron/1", s.cookie, s.csrf, nil); rec.Code != http.StatusNoContent || f.gotID != 1 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if e := lastAudit(t, h); e.Action != "DELETE /api/cron/1" || e.Result != audit.Success {
		t.Fatalf("audit %+v", e)
	}
	if rec := h.do(http.MethodDelete, "/api/cron/77", s.cookie, s.csrf, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: %d", rec.Code)
	}
	if e := lastAudit(t, h); e.Result != audit.Failure {
		t.Fatalf("a failed delete must be audited as a failure: %+v", e)
	}
}

func TestCronErrorMapping(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		status int
		class  string
		field  string
	}{
		{"duplicate", cron.ErrDuplicate, 409, errConflict, "name"},
		{"invalid", fmt.Errorf("%w: schedule: want 5 fields", cron.ErrInvalid), 400, errInvalidInput, ""},
		{"shell off", cron.ErrShellNotAllowed, 403, errShellNotAllowed, ""},
		{"missing", cron.ErrNotFound, 404, errNotFound, ""},
		{"internal", errors.New("sql: /var/lib/secret.db locked"), 500, errInternal, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := cronFixture()
			f.err = c.err
			h, s := m7Harness(t, func(d *Deps) { d.Cron = f })
			rec := h.do(http.MethodPost, "/api/cron", s.cookie, s.csrf,
				map[string]any{"name": "a", "schedule": "* * * * *", "kind": "argv", "user": "root", "argv": []string{"/bin/true"}})
			env := h.errorEnvelopeOf(rec)
			if rec.Code != c.status || env.Error != c.class || env.Field != c.field {
				t.Fatalf("got %d %+v, want %d %s field=%q", rec.Code, env, c.status, c.class, c.field)
			}
			if c.status == 500 && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("an internal error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestCronEnableDisableRun(t *testing.T) {
	f := cronFixture()
	h, s := m7Harness(t, func(d *Deps) { d.Cron = f })

	rec := h.do(http.MethodPost, "/api/cron/2/enable", s.cookie, s.csrf, map[string]any{})
	var j map[string]any
	h.decode(rec, &j)
	if rec.Code != 200 || f.gotEnable == nil || !*f.gotEnable || j["enabled"] != true {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	rec = h.do(http.MethodPost, "/api/cron/2/disable", s.cookie, s.csrf, nil)
	if rec.Code != 200 || *f.gotEnable {
		t.Fatalf("disable: %d", rec.Code)
	}
	if e := lastAudit(t, h); e.Action != "POST /api/cron/2/disable" || e.Target != "cron:2" {
		t.Fatalf("audit %+v", e)
	}

	run := cron.Run{ID: 21, JobID: 1, Source: cron.SourceManual, Status: cron.StatusRunning}
	f.runRes = cron.RunNowResult{Outcome: cron.OutcomeStarted, Run: run}
	rec = h.do(http.MethodPost, "/api/cron/1/run", s.cookie, s.csrf, map[string]any{})
	var res struct {
		Outcome string          `json:"outcome"`
		Run     *map[string]any `json:"run"`
	}
	h.decode(rec, &res)
	if rec.Code != http.StatusAccepted || res.Outcome != "started" || res.Run == nil || (*res.Run)["id"] != float64(21) {
		t.Fatalf("run started: %d %s", rec.Code, rec.Body.String())
	}
	if e := lastAudit(t, h); detailOf(e)["outcome"] != "started" || detailOf(e)["run_id"] != int64(21) {
		t.Fatalf("audit detail %+v", detailOf(e))
	}

	f.runRes = cron.RunNowResult{Outcome: cron.OutcomeQueued}
	rec = h.do(http.MethodPost, "/api/cron/1/run", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"run":null`) {
		t.Fatalf("run queued: %d %s", rec.Code, rec.Body.String())
	}

	f.runRes = cron.RunNowResult{Outcome: cron.OutcomeSkipped, Run: cron.Run{ID: 22, JobID: 1, Status: cron.StatusSkipped}}
	if rec := h.do(http.MethodPost, "/api/cron/1/run", s.cookie, s.csrf, nil); rec.Code != 200 {
		t.Fatalf("run skipped: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/cron/55/run", s.cookie, s.csrf, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("run missing job: %d", rec.Code)
	}
}

func TestCronRuns(t *testing.T) {
	f := cronFixture()
	h, s := m7Harness(t, func(d *Deps) { d.Cron = f })

	rec := h.do(http.MethodGet, "/api/cron/1/runs?limit=5", s.cookie, "", nil)
	var got struct {
		Runs []cron.Run `json:"runs"`
	}
	h.decode(rec, &got)
	if rec.Code != 200 || len(got.Runs) != 1 || got.Runs[0].OutputTail != "done" {
		t.Fatalf("runs: %d %s", rec.Code, rec.Body.String())
	}
	rec = h.do(http.MethodGet, "/api/cron/2/runs", s.cookie, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"runs":[]`) {
		t.Fatalf("a job without runs must give an empty array: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do(http.MethodGet, "/api/cron/9/runs", s.cookie, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("runs of a missing job: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/cron/1/runs?limit=x", s.cookie, "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}

	rec = h.do(http.MethodGet, "/api/cron/1/runs/11", s.cookie, "", nil)
	var run cron.Run
	h.decode(rec, &run)
	if rec.Code != 200 || run.ID != 11 || run.Status != cron.StatusSucceeded {
		t.Fatalf("run detail: %d %s", rec.Code, rec.Body.String())
	}
	// A run is addressed through its own job.
	if rec := h.do(http.MethodGet, "/api/cron/2/runs/11", s.cookie, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("another job's run: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/cron/1/runs/404", s.cookie, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing run: %d", rec.Code)
	}
}
