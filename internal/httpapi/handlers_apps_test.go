package httpapi

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/apps"
	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/runtimes"
)

type fakeApps struct {
	err      error
	warn     []string
	gotIn    apps.Input
	gotName  string
	calls    []string
	logChunk apps.LogChunk
	gotLog   [2]int64
}

func (f *fakeApps) status(name string) apps.Status {
	return apps.Status{App: apps.App{ID: 1, Name: name, Type: apps.TypeGo, Args: []string{}, Env: map[string]string{}}, State: "RUNNING"}
}
func (f *fakeApps) List(context.Context) ([]apps.Status, error) {
	return []apps.Status{f.status("web")}, f.err
}
func (f *fakeApps) Get(_ context.Context, name string) (apps.Status, error) {
	return f.status(name), f.err
}
func (f *fakeApps) Create(_ context.Context, in apps.Input) (apps.Status, []string, error) {
	f.gotIn = in
	return f.status(in.Name), f.warn, f.err
}
func (f *fakeApps) Update(_ context.Context, name string, in apps.Input) (apps.Status, []string, error) {
	f.gotName, f.gotIn = name, in
	return f.status(name), f.warn, f.err
}
func (f *fakeApps) Delete(_ context.Context, name string) error { f.gotName = name; return f.err }
func (f *fakeApps) Start(_ context.Context, name string) error {
	f.calls = append(f.calls, "start "+name)
	return f.err
}
func (f *fakeApps) Stop(_ context.Context, name string) error {
	f.calls = append(f.calls, "stop "+name)
	return f.err
}
func (f *fakeApps) Restart(_ context.Context, name string) error {
	f.calls = append(f.calls, "restart "+name)
	return f.err
}
func (f *fakeApps) Log(_ context.Context, _ string, offset int64, max int) (apps.LogChunk, error) {
	f.gotLog = [2]int64{offset, int64(max)}
	return f.logChunk, f.err
}

type fakeRuntimes struct {
	state     runtimes.State
	installed chan string
	removed   chan string
	err       error
}

func (f *fakeRuntimes) List(context.Context) (runtimes.State, error) { return f.state, f.err }
func (f *fakeRuntimes) Install(_ context.Context, v string, onLine func(string)) error {
	onLine("installing " + v)
	f.installed <- v
	return nil
}
func (f *fakeRuntimes) Uninstall(_ context.Context, v string, onLine func(string)) error {
	onLine("removing " + v)
	f.removed <- v
	return nil
}

func appsHarness(t *testing.T, a AppsService, r RuntimesService) (*harness, session) {
	t.Helper()
	h := newHarness(t)
	h.deps.Apps, h.deps.Runtimes = a, r
	var err error
	if h.handler, err = New(h.deps); err != nil {
		t.Fatal(err)
	}
	return h, h.login()
}

func TestAppsRoutesUnavailableWithoutServices(t *testing.T) {	h, s := appsHarness(t, nil, nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/apps"},
		{http.MethodGet, "/api/apps/web"},
		{http.MethodPost, "/api/apps/web/start"},
		{http.MethodGet, "/api/runtimes"},
	} {
		if rec := h.do(c.method, c.path, s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestAppsRoutesNeedLogin(t *testing.T) {
	h, _ := appsHarness(t, &fakeApps{}, &fakeRuntimes{})
	for _, path := range []string{"/api/apps", "/api/apps/web/logs", "/api/apps/web/logs/stream", "/api/runtimes"} {
		if rec := h.do(http.MethodGet, path, "", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
}

func TestAppsCreateUpdateDeleteRoundTrip(t *testing.T) {
	f := &fakeApps{warn: []string{"web did not start: boom"}}
	h, s := appsHarness(t, f, nil)

	rec := h.do(http.MethodPost, "/api/apps", s.cookie, s.csrf, map[string]any{
		"name": "web", "type": "go", "command": "/bin/web", "port": 3000, "autostart": false,
		"env": map[string]string{"A": "b"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if f.gotIn.Name != "web" || f.gotIn.Port == nil || *f.gotIn.Port != 3000 || f.gotIn.Autostart == nil || *f.gotIn.Autostart {
		t.Fatalf("decoded wrong: %+v", f.gotIn)
	}
	var created appResponse
	h.decode(rec, &created)
	if created.App.Name != "web" || len(created.Warnings) != 1 {
		t.Fatalf("response: %+v", created)
	}

	if rec := h.do(http.MethodPut, "/api/apps/web", s.cookie, s.csrf, map[string]any{"type": "go", "command": "/bin/web"}); rec.Code != http.StatusOK || f.gotName != "web" {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do(http.MethodDelete, "/api/apps/web", s.cookie, s.csrf, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/apps", s.cookie, s.csrf, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"apps":[`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.doRaw(http.MethodPost, "/api/apps", s.cookie, s.csrf, `{"name":"a","nope":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestAppsErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		class  string
		field  string
	}{
		{"validation", &apps.ValidationError{Field: "port", Msg: "must be between 1 and 65535"}, 422, "invalid_input", "port"},
		{"not found", apps.ErrNotFound, 404, "not_found", ""},
		{"exists", apps.ErrExists, 409, "conflict", ""},
		{"port in use", apps.ErrPortInUse, 409, "conflict", "port"},
		{"node missing", apps.ErrNodeMissing, 409, "conflict", "node_version"},
		{"supervisord down", apps.ErrNoSupervisor, 503, "unavailable", ""},
		{"supervisord down inside an apply error", &confedit.ApplyError{Stage: confedit.StageValidate, Err: apps.ErrNoSupervisor}, 503, "unavailable", ""},
		{"already running", apps.ErrAlreadyRunning, 409, "conflict", ""},
		{"rejected file", &confedit.ApplyError{Stage: confedit.StageValidate, Output: "line 3: bad", Err: errors.New("rejected")}, 422, "config_rejected", ""},
		{"reload failed", &confedit.ApplyError{Stage: confedit.StageReload, Err: errors.New("x")}, 502, "apply_failed", ""},
		{"rollback failed", &confedit.ApplyError{Stage: confedit.StageReload, Err: errors.New("x"), RollbackErr: errors.New("y")}, 500, "apply_failed", ""},
		{"unknown", errors.New("database is locked: /secret/path"), 500, "internal_error", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, s := appsHarness(t, &fakeApps{err: c.err}, nil)
			rec := h.do(http.MethodPost, "/api/apps", s.cookie, s.csrf, map[string]any{"name": "web", "type": "go", "command": "/bin/web"})
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body.String())
			}
			env := h.errorEnvelopeOf(rec)
			if env.Error != c.class || env.Field != c.field {
				t.Fatalf("envelope %+v, want class %s field %q", env, c.class, c.field)
			}
			if strings.Contains(rec.Body.String(), "/secret/path") {
				t.Fatalf("an internal error leaked into the response: %s", rec.Body.String())
			}
			if c.name == "rejected file" && env.Output != "line 3: bad" {
				t.Fatalf("supervisord's output must reach the operator verbatim: %+v", env)
			}
		})
	}
}

func TestAppsControls(t *testing.T) {
	f := &fakeApps{}
	h, s := appsHarness(t, f, nil)
	for _, action := range []string{"start", "stop", "restart"} {
		if rec := h.do(http.MethodPost, "/api/apps/web/"+action, s.cookie, s.csrf, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", action, rec.Code, rec.Body.String())
		}
	}
	if got := strings.Join(f.calls, ","); got != "start web,stop web,restart web" {
		t.Fatalf("calls: %s", got)
	}
}

func TestAppsLogParametersAndStream(t *testing.T) {
	f := &fakeApps{logChunk: apps.LogChunk{Data: "hello\n", Offset: 6}}
	h, s := appsHarness(t, f, nil)

	if rec := h.do(http.MethodGet, "/api/apps/web/logs?offset=abc", s.cookie, s.csrf, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad offset: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/apps/web/logs?max=-1", s.cookie, s.csrf, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad max: %d", rec.Code)
	}
	rec := h.do(http.MethodGet, "/api/apps/web/logs?offset=3&max=100", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusOK || f.gotLog[0] != 3 || f.gotLog[1] != 100 || !strings.Contains(rec.Body.String(), `"offset":6`) {
		t.Fatalf("logs: %d %s got=%v", rec.Code, rec.Body.String(), f.gotLog)
	}

	// The stream sends the first chunk at once and ends when the client goes.
	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/apps/web/logs/stream", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: s.cookie})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	var event, data string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if event != "log" || !strings.Contains(data, `"data":"hello\n"`) || !strings.Contains(data, `"offset":6`) {
		t.Fatalf("first event %q %q", event, data)
	}
	if f.gotLog[0] != -1 || f.gotLog[1] != logInitialBytes {
		t.Fatalf("the first read of a new stream is the tail: %v", f.gotLog)
	}
}

func TestRuntimesList(t *testing.T) {
	rt := &fakeRuntimes{state: runtimes.State{NVMTag: "v0.40.3", Node: []runtimes.Node{{Version: "22.11.0", Path: "/n", UsedBy: []string{"api"}}}}}
	h, s := appsHarness(t, nil, rt)
	rec := h.do(http.MethodGet, "/api/runtimes", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"used_by":["api"]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRuntimesInstallStartsAJobAndValidatesTheVersion(t *testing.T) {
	rt := &fakeRuntimes{installed: make(chan string, 1), removed: make(chan string, 1)}
	h, s := appsHarness(t, nil, rt)

	for _, bad := range []string{"", "latest", "22;rm -rf /", "$(id)"} {
		rec := h.do(http.MethodPost, "/api/runtimes/node", s.cookie, s.csrf, map[string]any{"version": bad})
		if rec.Code != http.StatusUnprocessableEntity || h.errorEnvelopeOf(rec).Field != "version" {
			t.Errorf("version %q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	select {
	case v := <-rt.installed:
		t.Fatalf("an invalid version reached the installer: %q", v)
	default:
	}

	rec := h.do(http.MethodPost, "/api/runtimes/node", s.cookie, s.csrf, map[string]any{"version": "22"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Job jobs.Job `json:"job"`
	}
	h.decode(rec, &resp)
	if resp.Job.ID == "" || resp.Job.Kind != "runtimes.node.install" {
		t.Fatalf("job: %+v", resp.Job)
	}
	select {
	case v := <-rt.installed:
		if v != "22" {
			t.Fatalf("installed %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job never ran")
	}
}

func TestRuntimesUninstallChecksUsersFirst(t *testing.T) {
	rt := &fakeRuntimes{
		removed: make(chan string, 1), installed: make(chan string, 1),
		state: runtimes.State{Node: []runtimes.Node{
			{Version: "22.11.0", UsedBy: []string{"api", "web"}},
			{Version: "20.18.0", UsedBy: []string{}},
		}},
	}
	h, s := appsHarness(t, nil, rt)

	rec := h.do(http.MethodDelete, "/api/runtimes/node/22.11.0", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "api, web") {
		t.Fatalf("in use: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do(http.MethodDelete, "/api/runtimes/node/18.0.0", s.cookie, s.csrf, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/runtimes/node/22", s.cookie, s.csrf, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("partial version: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/runtimes/node/20.18.0", s.cookie, s.csrf, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case v := <-rt.removed:
		if v != "20.18.0" {
			t.Fatalf("removed %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the job never ran")
	}
}

// proxyApps is a fakeApps whose app has a port, so its ProxyURL names where the
// proxy sites point.
type proxyApps struct {
	*fakeApps
	port int
}

func (p *proxyApps) Get(_ context.Context, name string) (apps.Status, error) {
	st := p.status(name)
	st.Port = &p.port
	st.ProxyURL = st.App.ProxyURL()
	return st, nil
}

// Deleting an app also removes the reverse-proxy sites that pass requests to its
// port: they would point at a port nothing serves.
func TestDeleteAppRemovesItsProxySites(t *testing.T) {
	h := newNginxHarness(t)
	apps := &proxyApps{fakeApps: &fakeApps{}, port: 3000}
	h.deps.Apps = apps
	var err error
	if h.handler, err = New(h.deps); err != nil {
		t.Fatal(err)
	}
	h.s = h.login()

	h.mustCreate(map[string]any{"domain": "api.test", "kind": "proxy", "proxy_target": "http://127.0.0.1:3000"})
	h.mustCreate(map[string]any{"domain": "other.test", "kind": "proxy", "proxy_target": "http://127.0.0.1:4000"})

	code, out := h.call(http.MethodDelete, "/api/apps/api", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete app = %d %v", code, out)
	}

	_, out = h.call(http.MethodGet, "/api/sites", nil)
	sites := out["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("want one site left, got %v", sites)
	}
	if got := sites[0].(map[string]any)["domain"]; got != "other.test" {
		t.Fatalf("wrong site removed: %v remains", got)
	}
}
