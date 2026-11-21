package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/sites"
)

// memFS is the filesystem behind the sites manager in these tests.
type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
	dirs  map[string]bool
}

type memInfo struct{ fs.FileInfo }

func (m *memFS) ReadFile(n string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.files[n]; ok {
		return b, nil
	}
	return nil, &fs.PathError{Op: "open", Path: n, Err: fs.ErrNotExist}
}

func (m *memFS) Stat(n string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[n]; ok || m.dirs[n] {
		return memInfo{}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: n, Err: fs.ErrNotExist}
}

func (m *memFS) MkdirAll(n string, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[n] = true
	return nil
}

func (m *memFS) WriteFile(n string, d []byte, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[n] = d
	return nil
}

func (m *memFS) Remove(n string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, n)
	delete(m.dirs, n)
	return nil
}

func (m *memFS) Chown(string, int, int) error { return nil }

// memApplier stands in for confedit: it writes into memFS, or rejects the
// change and leaves the files alone, which is what a rollback amounts to.
type memApplier struct {
	fs  *memFS
	err error
}

func (a *memApplier) Apply(_ context.Context, ch confedit.Change) (confedit.Result, error) {
	if a.err != nil {
		return confedit.Result{}, a.err
	}
	for _, f := range ch.Files {
		if f.Delete {
			_ = a.fs.Remove(f.Path)
			continue
		}
		_ = a.fs.WriteFile(f.Path, f.Content, 0o644)
	}
	return confedit.Result{Versions: map[string]int{}}, nil
}

type fakeNginx struct {
	status   *nginx.Status // nil: the unit does not exist
	stateErr error
}

func (f *fakeNginx) Name() string                             { return "nginx" }
func (f *fakeNginx) Validate(context.Context) (string, error) { return "", nil }
func (f *fakeNginx) Reload(context.Context) error             { return nil }
func (f *fakeNginx) Healthy(context.Context) error            { return nil }
func (f *fakeNginx) State(context.Context) (nginx.Status, bool, error) {
	if f.stateErr != nil {
		return nginx.Status{}, false, f.stateErr
	}
	if f.status == nil {
		return nginx.Status{}, false, nil
	}
	return *f.status, true, nil
}

type fakeHistory struct {
	versions map[string][]confedit.Version
	content  map[string]string // path + "#" + version
	restored []string
	err      error
}

func (f *fakeHistory) History(_ context.Context, p string) ([]confedit.Version, error) {
	return f.versions[p], nil
}

func (f *fakeHistory) ReadVersion(_ context.Context, p string, v int) ([]byte, error) {
	c, ok := f.content[p+"#"+itoa(v)]
	if !ok {
		return nil, confedit.ErrNoVersion
	}
	return []byte(c), nil
}

func (f *fakeHistory) Restore(_ context.Context, p string, v int, svcs []confedit.Service) (confedit.Result, error) {
	if f.err != nil {
		return confedit.Result{}, f.err
	}
	if _, ok := f.content[p+"#"+itoa(v)]; !ok {
		return confedit.Result{}, confedit.ErrNoVersion
	}
	f.restored = append(f.restored, p+"#"+itoa(v)+" via "+svcs[0].Name())
	return confedit.Result{}, nil
}

type fakeReconcile struct{ status sites.Status }

func (f fakeReconcile) Status() sites.Status { return f.status }

type nginxHarness struct {
	*harness
	s       session
	fs      *memFS
	applier *memApplier
	svc     *fakeNginx
	history *fakeHistory
	// php is the PHP layout, when the harness wires one.
	php php.Paths
}

func rejected(output string) error {
	return &confedit.ApplyError{Stage: confedit.StageValidate, Service: "nginx", Output: output, Err: errors.New("nginx -t rejected the configuration (exit 1)")}
}

func newNginxHarness(t *testing.T) *nginxHarness {
	t.Helper()
	h := newHarness(t)
	paths := nginx.Paths{Root: "/www"}
	mfs := &memFS{files: map[string][]byte{}, dirs: map[string]bool{}}
	ap := &memApplier{fs: mfs}
	svc := &fakeNginx{status: &nginx.Status{Loaded: true, Active: true, State: "active", PID: 7}}
	hist := &fakeHistory{versions: map[string][]confedit.Version{}, content: map[string]string{}}

	mgr, err := sites.New(sites.Options{Store: h.store, Applier: ap, Service: svc, Paths: paths, FS: mfs})
	if err != nil {
		t.Fatal(err)
	}
	conf, err := paths.RenderMainConf()
	if err != nil {
		t.Fatal(err)
	}
	mfs.files[paths.ConfPath()] = conf

	h.deps.Nginx = &NginxDeps{
		Sites:   mgr,
		Global:  nginx.NewGlobal(h.store, ap, svc, paths).ReadWith(mfs.ReadFile),
		History: hist, Service: svc, Paths: paths,
		Reconciler: fakeReconcile{sites.Status{Done: true, OK: true, Steps: []sites.Step{{Name: "container", Detail: "none", OK: true}}}},
	}
	h.handler, err = New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	return &nginxHarness{harness: h, s: h.login(), fs: mfs, applier: ap, svc: svc, history: hist}
}

func (h *nginxHarness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	rec := h.do(method, path, h.s.cookie, h.s.csrf, body)
	var out map[string]any
	if rec.Body.Len() > 0 {
		h.decode(rec, &out)
	}
	return rec.Code, out
}

func (h *nginxHarness) mustCreate(body map[string]any) {
	h.t.Helper()
	if code, out := h.call(http.MethodPost, "/api/sites", body); code != http.StatusCreated {
		h.t.Fatalf("create %v: %d %v", body, code, out)
	}
}

func TestNginxRoutesAnswer503WithoutNginx(t *testing.T) {
	h := newHarness(t)
	s := h.login()
	rec := h.do(http.MethodGet, "/api/sites", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errUnavailable {
		t.Errorf("error = %q", env.Error)
	}
}

func TestSiteLifecycle(t *testing.T) {
	h := newNginxHarness(t)

	code, out := h.call(http.MethodGet, "/api/sites", nil)
	if code != 200 || len(out["sites"].([]any)) != 0 {
		t.Fatalf("empty list: %d %v", code, out)
	}

	code, out = h.call(http.MethodPost, "/api/sites", map[string]any{"domain": "A.test", "aliases": []string{"www.a.test"}, "kind": "static"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	site := out["site"].(map[string]any)
	if site["domain"] != "a.test" || site["root"] != "/www/services/sites/a.test" {
		t.Errorf("site = %v", site)
	}
	if _, ok := out["warnings"].([]any); !ok {
		t.Errorf("warnings must be a list, got %v", out["warnings"])
	}

	h.mustCreate(map[string]any{"domain": "api.test", "kind": "proxy", "proxy_target": "http://127.0.0.1:3000"})

	code, out = h.call(http.MethodGet, "/api/sites", nil)
	if code != 200 || len(out["sites"].([]any)) != 2 {
		t.Fatalf("list: %d %v", code, out)
	}

	code, out = h.call(http.MethodGet, "/api/sites/api.test", nil)
	if code != 200 || len(out["features"].([]any)) != 5 || out["site_file"] != "/www/panel/nginx/sites/api.test.conf" {
		t.Fatalf("detail: %d %v", code, out)
	}
	for _, f := range out["features"].([]any) {
		if f.(map[string]any)["attached"] != true {
			t.Errorf("feature %v is detached on a new site", f)
		}
	}

	code, out = h.call(http.MethodPut, "/api/sites/api.test", map[string]any{"proxy_target": "http://127.0.0.1:4000", "aliases": []string{"api2.test"}})
	if code != 200 || out["site"].(map[string]any)["proxy_target"] != "http://127.0.0.1:4000" {
		t.Fatalf("update: %d %v", code, out)
	}
	// The single target stays one route on "/", and the list carries it too.
	routes := out["site"].(map[string]any)["proxy_routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["path"] != "/" || routes[0].(map[string]any)["target"] != "http://127.0.0.1:4000" {
		t.Errorf("routes = %v, want one / route to the new target", routes)
	}

	if code, _ := h.call(http.MethodDelete, "/api/sites/a.test", nil); code != http.StatusBadRequest {
		t.Errorf("delete without confirm = %d, want 400", code)
	}
	if code, _ := h.call(http.MethodDelete, "/api/sites/a.test?confirm=api.test", nil); code != http.StatusBadRequest {
		t.Errorf("delete with another site's name = %d, want 400", code)
	}
	code, out = h.call(http.MethodDelete, "/api/sites/a.test?confirm=a.test", nil)
	if code != 200 || out["web_root"] != "/www/services/sites/a.test" {
		t.Fatalf("delete: %d %v", code, out)
	}
	if code, _ := h.call(http.MethodGet, "/api/sites/a.test", nil); code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", code)
	}
}

func TestSiteInputErrors(t *testing.T) {
	h := newNginxHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	cases := []struct {
		name   string
		body   map[string]any
		status int
		class  string
		field  string
	}{
		{"wildcard", map[string]any{"domain": "*.a.test", "kind": "static"}, 422, errInvalidInput, "domain"},
		{"no kind", map[string]any{"domain": "b.test"}, 422, errInvalidInput, "kind"},
		{"proxy without target", map[string]any{"domain": "b.test", "kind": "proxy"}, 422, errInvalidInput, "proxy_routes"},
		{"root outside", map[string]any{"domain": "b.test", "kind": "static", "root": "/etc"}, 422, errInvalidInput, "root"},
		{"duplicate", map[string]any{"domain": "a.test", "kind": "static"}, 409, errConflict, ""},
		{"alias taken", map[string]any{"domain": "b.test", "aliases": []string{"a.test"}, "kind": "static"}, 409, errConflict, ""},
		// A "/" route next to a web root is the classic two-`location /` bug,
		// refused with the field the operator has to change.
		{"static with / route", map[string]any{"domain": "b.test", "kind": "static", "proxy_routes": []map[string]any{
			{"path": "/", "target": "http://127.0.0.1:3000"}}}, 422, errInvalidInput, "proxy_routes"},
		{"route path without a slash", map[string]any{"domain": "b.test", "kind": "proxy", "proxy_routes": []map[string]any{
			{"path": "api", "target": "http://127.0.0.1:3000"}}}, 422, errInvalidInput, "proxy_routes"},
		{"route target with a path", map[string]any{"domain": "b.test", "kind": "proxy", "proxy_routes": []map[string]any{
			{"path": "/api", "target": "http://127.0.0.1:3000/app"}}}, 422, errInvalidInput, "proxy_routes"},
		{"duplicate route paths", map[string]any{"domain": "b.test", "kind": "proxy", "proxy_routes": []map[string]any{
			{"path": "/api", "target": "http://127.0.0.1:3000"}, {"path": "/api", "target": "http://127.0.0.1:4000"}}}, 422, errInvalidInput, "proxy_routes"},
		{"both spellings", map[string]any{"domain": "b.test", "kind": "proxy", "proxy_target": "http://127.0.0.1:3000",
			"proxy_routes": []map[string]any{{"path": "/api", "target": "http://127.0.0.1:4000"}}}, 422, errInvalidInput, "proxy_routes"},
	}
	for _, c := range cases {
		rec := h.do(http.MethodPost, "/api/sites", h.s.cookie, h.s.csrf, c.body)
		env := h.errorEnvelopeOf(rec)
		if rec.Code != c.status || env.Error != c.class || env.Field != c.field {
			t.Errorf("%s: %d %+v, want %d %s field %q", c.name, rec.Code, env, c.status, c.class, c.field)
		}
	}

	rec := h.doRaw(http.MethodPost, "/api/sites", h.s.cookie, h.s.csrf, `{"domain":"b.test","kind":"static","bogus":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", rec.Code)
	}
	if code, _ := h.call(http.MethodPut, "/api/sites/a.test", map[string]any{}); code != 422 {
		t.Errorf("empty update = %d, want 422", code)
	}
	if code, _ := h.call(http.MethodPut, "/api/sites/nope.test", map[string]any{"aliases": []string{}}); code != 404 {
		t.Errorf("update of an unknown site = %d, want 404", code)
	}
}

// The request and response shape of proxy routes: strip_prefix defaults to true
// when omitted, the detail carries the routes, and updating the list rewrites
// proxy.conf without touching anything else.
func TestSiteProxyRoutesShape(t *testing.T) {
	h := newNginxHarness(t)

	code, out := h.call(http.MethodPost, "/api/sites", map[string]any{
		"domain": "a.test", "kind": "static",
		"proxy_routes": []map[string]any{
			{"path": "/api", "target": "http://127.0.0.1:8080"},
			{"path": "/api/v2", "target": "http://127.0.0.1:8081", "strip_prefix": false},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	routes := out["site"].(map[string]any)["proxy_routes"].([]any)
	if len(routes) != 2 {
		t.Fatalf("routes = %v, want two", routes)
	}
	first, second := routes[0].(map[string]any), routes[1].(map[string]any)
	if first["path"] != "/api" || first["strip_prefix"] != true {
		t.Errorf("omitted strip_prefix = %v; want the default true", first)
	}
	if second["path"] != "/api/v2" || second["strip_prefix"] != false {
		t.Errorf("explicit strip_prefix = %v; want false kept", second)
	}
	if got := out["site"].(map[string]any)["proxy_target"]; got != nil && got != "" {
		t.Errorf("proxy_target = %v, want empty (there is no / route)", got)
	}

	// The detail exposes the same list.
	code, out = h.call(http.MethodGet, "/api/sites/a.test", nil)
	if code != 200 || len(out["proxy_routes"].([]any)) != 2 {
		t.Fatalf("detail: %d %v", code, out)
	}

	// A static site cannot take a "/" route: its own folder claims that
	// location. The refusal names the field.
	code, out = h.call(http.MethodPut, "/api/sites/a.test", map[string]any{"proxy_target": "http://127.0.0.1:9090"})
	if code != 422 {
		t.Fatalf("a / route on a static site = %d %v, want 422", code, out)
	}

	// A proxy site does take it, and the old single-target spelling collapses
	// the list to one "/" route.
	h.mustCreate(map[string]any{"domain": "p.test", "kind": "proxy", "proxy_routes": []map[string]any{
		{"path": "/api", "target": "http://127.0.0.1:8080"}, {"path": "/v2", "target": "http://127.0.0.1:8081"}}})
	code, out = h.call(http.MethodPut, "/api/sites/p.test", map[string]any{"proxy_target": "http://127.0.0.1:9090"})
	if code != 200 {
		t.Fatalf("update: %d %v", code, out)
	}
	routes = out["site"].(map[string]any)["proxy_routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["path"] != "/" {
		t.Errorf("routes after a proxy_target update = %v", routes)
	}
	proxy, err := h.fs.ReadFile("/www/panel/nginx/includes/p.test/proxy.conf")
	if err != nil || !strings.Contains(string(proxy), "location / {") ||
		!strings.Contains(string(proxy), "proxy_pass http://127.0.0.1:9090;") ||
		strings.Contains(string(proxy), "/api") {
		t.Errorf("proxy.conf = %q, %v", proxy, err)
	}
}

func TestRejectedConfigIs422WithValidatorOutput(t *testing.T) {
	h := newNginxHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	before, _ := h.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")

	const out = "nginx: [emerg] unknown directive \"bogus\" in /www/panel/nginx/sites/a.test.conf:3\nnginx: configuration file /www/panel/nginx/nginx.conf test failed\n"
	h.applier.err = rejected(out)

	for name, req := range map[string]struct {
		method, path string
		body         any
	}{
		"site file":      {http.MethodPut, "/api/sites/a.test/file", map[string]any{"content": "server { bogus; }"}},
		"create":         {http.MethodPost, "/api/sites", map[string]any{"domain": "b.test", "kind": "static"}},
		"regenerate":     {http.MethodPost, "/api/sites/a.test/regenerate?confirm=a.test", nil},
		"nginx.conf":     {http.MethodPut, "/api/nginx/conf", map[string]any{"content": "bogus;"}},
		"nginx settings": {http.MethodPut, "/api/nginx/settings", nginx.DefaultHTTPSettings()},
	} {
		rec := h.do(req.method, req.path, h.s.cookie, h.s.csrf, req.body)
		env := h.errorEnvelopeOf(rec)
		if rec.Code != http.StatusUnprocessableEntity || env.Error != errConfigRejected {
			t.Errorf("%s: %d %+v, want 422 config_rejected", name, rec.Code, env)
		}
		if env.Output != out {
			t.Errorf("%s: output = %q, want nginx's text verbatim", name, env.Output)
		}
	}

	after, _ := h.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
	if string(after) != string(before) {
		t.Error("the site file changed although the edit was rejected")
	}
	if code, _ := h.call(http.MethodGet, "/api/sites/b.test", nil); code != 404 {
		t.Errorf("a rejected create left a site behind: %d", code)
	}
}

func TestApplyFailuresOtherThanValidation(t *testing.T) {
	h := newNginxHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	h.applier.err = &confedit.ApplyError{Stage: confedit.StageReload, Service: "nginx", Err: errors.New("container is exited")}
	rec := h.do(http.MethodPut, "/api/sites/a.test/file", h.s.cookie, h.s.csrf, map[string]any{"content": "server {}"})
	if env := h.errorEnvelopeOf(rec); rec.Code != http.StatusBadGateway || env.Error != errApplyFailed || !strings.Contains(env.Detail, "reload") {
		t.Errorf("reload failure: %d %+v", rec.Code, env)
	}

	h.applier.err = &confedit.ApplyError{Stage: confedit.StageHealth, Err: errors.New("x"), RollbackErr: errors.New("disk full")}
	rec = h.do(http.MethodPut, "/api/sites/a.test/file", h.s.cookie, h.s.csrf, map[string]any{"content": "server {}"})
	if env := h.errorEnvelopeOf(rec); rec.Code != http.StatusInternalServerError || strings.Contains(env.Detail, "disk full") {
		t.Errorf("failed rollback: %d %+v (the cause belongs in the log, not the response)", rec.Code, env)
	}
}

func TestSiteFileAndDetachedFeature(t *testing.T) {
	h := newNginxHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "proxy", "proxy_target": "http://127.0.0.1:3000"})

	code, out := h.call(http.MethodGet, "/api/sites/a.test/file", nil)
	if code != 200 || out["history_file"] != "sites/a.test.conf" || out["missing"] != false || !strings.Contains(out["content"].(string), "server {") {
		t.Fatalf("file: %d %v", code, out)
	}

	edited := strings.Replace(out["content"].(string), "    include /www/panel/nginx/includes/a.test/proxy.conf;\n", "    return 204;\n", 1)
	if code, out := h.call(http.MethodPut, "/api/sites/a.test/file", map[string]any{"content": edited}); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}

	_, out = h.call(http.MethodGet, "/api/sites/a.test", nil)
	state := map[string]bool{}
	for _, f := range out["features"].([]any) {
		m := f.(map[string]any)
		state[m["feature"].(string)] = m["attached"].(bool)
		if m["attached"] == false && m["reason"] == nil {
			t.Errorf("%v is detached without a reason", m["feature"])
		}
	}
	if state["proxy"] || !state["names"] || !state["listen"] {
		t.Errorf("features = %v, want only proxy detached", state)
	}

	rec := h.do(http.MethodPut, "/api/sites/a.test", h.s.cookie, h.s.csrf, map[string]any{"proxy_target": "http://127.0.0.1:9"})
	if env := h.errorEnvelopeOf(rec); rec.Code != http.StatusConflict || env.Error != errDetached {
		t.Errorf("update of a detached control: %d %+v", rec.Code, env)
	}

	if code, _ := h.call(http.MethodPost, "/api/sites/a.test/regenerate", nil); code != 400 {
		t.Errorf("regenerate without confirm = %d, want 400", code)
	}
	if code, out := h.call(http.MethodPost, "/api/sites/a.test/regenerate?confirm=a.test", nil); code != 200 {
		t.Fatalf("regenerate: %d %v", code, out)
	}
	if code, _ := h.call(http.MethodPut, "/api/sites/a.test", map[string]any{"proxy_target": "http://127.0.0.1:9"}); code != 200 {
		t.Errorf("update after regenerate = %d", code)
	}
}

func TestNginxSettingsRoutes(t *testing.T) {
	h := newNginxHarness(t)

	code, out := h.call(http.MethodGet, "/api/nginx/settings", nil)
	if code != 200 || out["client_max_body_size"] != "100m" || out["worker_processes"] != "auto" {
		t.Fatalf("defaults: %d %v", code, out)
	}

	s := nginx.DefaultHTTPSettings()
	s.ClientMaxBodySize = "512m"
	s.Gzip = false
	code, out = h.call(http.MethodPut, "/api/nginx/settings", s)
	if code != 200 || out["client_max_body_size"] != "512m" || out["gzip"] != false {
		t.Fatalf("save: %d %v", code, out)
	}
	if b, _ := h.fs.ReadFile("/www/panel/nginx/panel-http.conf"); !strings.Contains(string(b), "client_max_body_size 512m;") {
		t.Errorf("panel-http.conf = %s", b)
	}

	s.WorkerProcesses = "auto; user root"
	rec := h.do(http.MethodPut, "/api/nginx/settings", h.s.cookie, h.s.csrf, s)
	if env := h.errorEnvelopeOf(rec); rec.Code != 422 || env.Field != "worker_processes" {
		t.Errorf("injection attempt: %d %+v", rec.Code, env)
	}
	// A partial body leaves the timeouts at zero, which is invalid, rather
	// than silently resetting them.
	if code, _ := h.call(http.MethodPut, "/api/nginx/settings", map[string]any{"worker_processes": "2"}); code != 422 {
		t.Errorf("partial settings = %d, want 422", code)
	}
}

func TestNginxConfRoutes(t *testing.T) {
	h := newNginxHarness(t)

	code, out := h.call(http.MethodGet, "/api/nginx/conf", nil)
	if code != 200 || out["history_file"] != "nginx.conf" || len(out["missing_links"].([]any)) != 0 {
		t.Fatalf("conf: %d %v", code, out)
	}

	edited := strings.Replace(out["content"].(string), "    include /www/panel/nginx/panel-http.conf;\n", "", 1)
	if code, out := h.call(http.MethodPut, "/api/nginx/conf", map[string]any{"content": edited}); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	_, out = h.call(http.MethodGet, "/api/nginx/conf", nil)
	if links := out["missing_links"].([]any); len(links) != 1 || links[0] != "the common settings" {
		t.Errorf("missing_links = %v", out["missing_links"])
	}
}

func TestConfigHistoryRoutes(t *testing.T) {
	h := newNginxHarness(t)
	const p = "/www/panel/nginx/sites/a.test.conf"
	h.history.versions[p] = []confedit.Version{
		{Path: p, Version: 2, Reason: "edit site file a.test", Size: 10, CreatedAt: time.Unix(2000, 0).UTC()},
		{Path: p, Version: 1, Reason: "create site a.test", Size: 9, CreatedAt: time.Unix(1000, 0).UTC()},
	}
	h.history.content[p+"#1"] = "server { one }"

	code, out := h.call(http.MethodGet, "/api/config-history/sites%2Fa.test.conf", nil)
	if code != 200 || len(out["versions"].([]any)) != 2 || out["file"] != "sites/a.test.conf" {
		t.Fatalf("history: %d %v", code, out)
	}
	code, out = h.call(http.MethodGet, "/api/config-history/sites%2Fa.test.conf/1", nil)
	if code != 200 || out["content"] != "server { one }" {
		t.Fatalf("version: %d %v", code, out)
	}
	if code, _ := h.call(http.MethodGet, "/api/config-history/sites%2Fa.test.conf/9", nil); code != 404 {
		t.Errorf("unknown version = %d, want 404", code)
	}
	if code, _ := h.call(http.MethodGet, "/api/config-history/sites%2Fa.test.conf/x", nil); code != 400 {
		t.Errorf("non-numeric version = %d, want 400", code)
	}

	if code, out := h.call(http.MethodPost, "/api/config-history/sites%2Fa.test.conf/1/restore", nil); code != 200 {
		t.Fatalf("restore: %d %v", code, out)
	}
	if len(h.history.restored) != 1 || h.history.restored[0] != p+"#1 via nginx" {
		t.Errorf("restored = %v", h.history.restored)
	}
	h.history.err = rejected("nginx: [emerg] old version no longer valid")
	rec := h.do(http.MethodPost, "/api/config-history/sites%2Fa.test.conf/1/restore", h.s.cookie, h.s.csrf, nil)
	if env := h.errorEnvelopeOf(rec); rec.Code != 422 || !strings.Contains(env.Output, "no longer valid") {
		t.Errorf("rejected restore: %d %+v", rec.Code, env)
	}
	h.history.err = nil

	// Only the raw files have history routes, and the key cannot leave the tree.
	for _, file := range []string{
		"panel-http.conf", "includes%2Fa.test%2Fnames.conf", "sites%2F..%2Fnginx.conf",
		"sites%2F..%2F..%2F..%2Fetc%2Fpasswd", "..%2Fdata%2Fpanel.db", "sites%2FA.test.conf",
	} {
		if code, _ := h.call(http.MethodGet, "/api/config-history/"+file, nil); code != 404 {
			t.Errorf("history of %s = %d, want 404", file, code)
		}
		if code, _ := h.call(http.MethodPost, "/api/config-history/"+file+"/1/restore", nil); code != 404 {
			t.Errorf("restore of %s = %d, want 404", file, code)
		}
	}
	if len(h.history.restored) != 1 {
		t.Errorf("a refused file was restored: %v", h.history.restored)
	}
}

func TestNginxServiceStatus(t *testing.T) {
	h := newNginxHarness(t)

	code, out := h.call(http.MethodGet, "/api/services/nginx", nil)
	c := out["container"].(map[string]any)
	if code != 200 || c["running"] != true || c["exists"] != true || out["reconcile"].(map[string]any)["ok"] != true {
		t.Fatalf("status: %d %v", code, out)
	}

	if c["status"] != "running" {
		t.Errorf("an active unit reports status %v, want running", c["status"])
	}

	h.svc.status = nil
	_, out = h.call(http.MethodGet, "/api/services/nginx", nil)
	if c := out["container"].(map[string]any); c["exists"] != false || c["status"] != "missing" {
		t.Errorf("missing unit = %v", c)
	}

	h.svc.stateErr = errors.New("systemctl: Failed to connect to bus: /run/dbus/system_bus_socket: No such file or directory")
	rec := h.do(http.MethodGet, "/api/services/nginx", h.s.cookie, h.s.csrf, nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "dbus") {
		t.Errorf("systemd down: %d %s (the cause belongs in the log)", rec.Code, rec.Body.String())
	}
}

func TestNginxWritesAreAudited(t *testing.T) {
	h := newNginxHarness(t)
	h.audit.entries = nil
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	h.applier.err = rejected("nginx: [emerg] nope")
	h.call(http.MethodPut, "/api/sites/a.test/file", map[string]any{"content": "x"})

	if len(h.audit.entries) != 2 {
		t.Fatalf("audit entries = %d, want 2", len(h.audit.entries))
	}
	if e := h.audit.entries[0]; e.Action != "POST /api/sites" || e.Result != "success" {
		t.Errorf("create entry = %+v", e)
	}
	if e := h.audit.entries[1]; e.Action != "PUT /api/sites/a.test/file" || e.Result != "failure" {
		t.Errorf("rejected edit entry = %+v", e)
	}
}

// A reverse-proxy site's proxy.conf is editable through the include routes, so
// every part of a site's nginx configuration can be changed from the UI.
func TestSiteIncludesAreEditable(t *testing.T) {
	h := newNginxHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "proxy", "proxy_target": "http://127.0.0.1:3000"})

	code, out := h.call(http.MethodGet, "/api/sites/a.test/includes", nil)
	if code != 200 {
		t.Fatalf("list includes: %d %v", code, out)
	}
	names := map[string]bool{}
	for _, inc := range out["includes"].([]any) {
		names[inc.(map[string]any)["name"].(string)] = true
	}
	if !names["proxy.conf"] {
		t.Errorf("a proxy site's includes must list proxy.conf: %v", names)
	}
	if names["php.conf"] {
		t.Errorf("a proxy site must not list php.conf: %v", names)
	}

	code, out = h.call(http.MethodGet, "/api/sites/a.test/includes/proxy.conf", nil)
	if code != 200 || !strings.Contains(out["content"].(string), "proxy_pass") {
		t.Fatalf("read proxy.conf: %d %v", code, out)
	}
	edited := strings.Replace(out["content"].(string), "http://127.0.0.1:3000", "http://127.0.0.1:8080", 1)
	if code, out := h.call(http.MethodPut, "/api/sites/a.test/includes/proxy.conf", map[string]any{"content": edited}); code != 200 {
		t.Fatalf("save proxy.conf: %d %v", code, out)
	}
	_, out = h.call(http.MethodGet, "/api/sites/a.test/includes/proxy.conf", nil)
	if !strings.Contains(out["content"].(string), "127.0.0.1:8080") {
		t.Errorf("proxy.conf was not saved: %v", out)
	}

	// A name that is not one of this build's include files is refused, whatever
	// path the client sends.
	if rec := h.do(http.MethodGet, "/api/sites/a.test/includes/secret.key", h.s.cookie, h.s.csrf, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown include name = %d, want 422", rec.Code)
	}
}
